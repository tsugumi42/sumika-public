package linkreader

// 规格-链接发送改版 C3：全部线路都超时 → 隔 5 秒整条再试一次；有明确失败不重试；第二轮按调用方剩余的时限算。
// 假线路按“第一次请求等到客户端超时、之后才可能回内容”演两轮；等待函数注入，测试不真等 5 秒。

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type retryRig struct {
	mu    sync.Mutex
	calls atomic.Int32
	waits []time.Duration
	seen  map[string]int // 每个主机被请求过几次：0 说明是这一轮第一次（演超时）
}

// timeoutRetryFetcher：每个主机的第一次请求都等到客户端超时；第二次起交给 give（返回 false 就继续超时）。
func timeoutRetryFetcher(t *testing.T, opts FetchOptions, give func(r *http.Request) (*http.Response, bool)) (*Fetcher, *retryRig) {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 50 * time.Millisecond
	}
	rig := &retryRig{seen: map[string]int{}}
	opts.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rig.calls.Add(1)
		host := r.URL.Hostname()
		rig.mu.Lock()
		n := rig.seen[host]
		rig.seen[host]++
		rig.mu.Unlock()
		if n > 0 {
			if resp, ok := give(r); ok {
				return resp, nil
			}
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	f, err := NewFetcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	// 媒体主机只解析不发请求：不碰真实 DNS
	f.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	f.retrySleep = func(ctx context.Context, d time.Duration) error {
		rig.mu.Lock()
		rig.waits = append(rig.waits, d)
		rig.mu.Unlock()
		return ctx.Err()
	}
	return f, rig
}

func (r *retryRig) waitList() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.waits...)
}

func attemptRounds(a []RouteAttempt) (first, second []string) {
	for _, it := range a {
		s := it.Route + " " + it.Kind
		switch it.Round {
		case 1:
			first = append(first, s)
		case 2:
			second = append(second, s)
		default:
			first = append(first, s+"（没标轮次）")
		}
	}
	return first, second
}

// 全超时 → 第二轮成功：结果成功，两轮线路记录都在，等待正好 5 秒。
func TestTimeoutRetrySecondRoundSucceeds(t *testing.T) {
	fxBody, vxBody := fixture(t, "x-video"), fixture(t, "vx-video")
	f, rig := timeoutRetryFetcher(t, FetchOptions{NoSyndication: true}, func(r *http.Request) (*http.Response, bool) {
		switch r.URL.Hostname() {
		case "api.fxtwitter.com":
			return response(200, fxBody), true
		case "api.vxtwitter.com":
			return response(200, vxBody), true
		}
		return nil, false
	})
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1900000000000000013"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.RetriedAfterTimeout {
		t.Fatal("结果没标“超时后重试过”")
	}
	if waits := rig.waitList(); len(waits) != 1 || waits[0] != timeoutRetryDelay {
		t.Fatalf("等待不对：%v", waits)
	}
	first, second := attemptRounds(r.Attempts)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("该有两轮各两条线路记录：%+v", r.Attempts)
	}
	for _, s := range first {
		if !strings.HasSuffix(s, " timeout") {
			t.Fatalf("第一轮都该是超时：%v", first)
		}
	}
	if !strings.Contains(strings.Join(second, ","), " ok") {
		t.Fatalf("第二轮该有一条成功：%v", second)
	}
	t.Logf("第二轮成功：来源 %s，第一轮 %v，第二轮 %v", r.Source, first, second)
}

// 整条消息 45 秒、第一轮 20 秒全超时（缩到外层 600ms、第一轮 200ms）：第二轮用外层剩下的时间，不从第一轮的 30 秒里挤。
func TestTimeoutRetrySecondRoundUsesCallerTime(t *testing.T) {
	fxBody, vxBody := fixture(t, "x-video"), fixture(t, "vx-video")
	var secondLeft atomic.Int64
	f, rig := timeoutRetryFetcher(t, FetchOptions{NoSyndication: true, Timeout: time.Second}, func(r *http.Request) (*http.Response, bool) {
		if r.URL.Hostname() == "api.vxtwitter.com" { // fx_v2 自己有 4 秒的短时限，看 vx 才看得出第二轮整体的时限
			if deadline, ok := r.Context().Deadline(); ok {
				secondLeft.Store(int64(time.Until(deadline)))
			}
		}
		switch r.URL.Hostname() {
		case "api.fxtwitter.com":
			return response(200, fxBody), true
		case "api.vxtwitter.com":
			return response(200, vxBody), true
		}
		return nil, false
	})
	f.roundTime = 200 * time.Millisecond
	// 生产值 5 秒等待、8 秒第二轮最短、15 秒封顶，按 1/100 缩小
	f.retry = retryTiming{delay: 50 * time.Millisecond, minRound: 80 * time.Millisecond, maxRound: 150 * time.Millisecond}
	f.retrySleep = func(ctx context.Context, d time.Duration) error {
		rig.mu.Lock()
		rig.waits = append(rig.waits, d)
		rig.mu.Unlock()
		timer := time.NewTimer(d) // 真等 50ms：第二轮的时限按等完之后的剩余算
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	r, err := f.Fetch(ctx, link(t, "https://x.com/AP/status/1900000000000000013"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.RetriedAfterTimeout {
		t.Fatal("第一轮 200ms 全超时后该重试")
	}
	if waits := rig.waitList(); len(waits) != 1 || waits[0] != 50*time.Millisecond {
		t.Fatalf("等待不对：%v", waits)
	}
	if d := time.Duration(secondLeft.Load()); d <= 0 || d > 150*time.Millisecond {
		t.Fatalf("第二轮时限该按外层剩余封顶 150ms：%v", d)
	}
}

// 外层没有截止时间（直接调 Fetch 的入口）：第二轮按 15 秒封顶。
func TestTimeoutRetrySecondRoundDefaultLimit(t *testing.T) {
	fxBody, vxBody := fixture(t, "x-video"), fixture(t, "vx-video")
	var secondLeft atomic.Int64
	f, rig := timeoutRetryFetcher(t, FetchOptions{NoSyndication: true, Timeout: 20 * time.Second}, func(r *http.Request) (*http.Response, bool) {
		if r.URL.Hostname() == "api.vxtwitter.com" { // fx_v2 自己有 4 秒的短时限，看 vx 才看得出第二轮整体的时限
			if deadline, ok := r.Context().Deadline(); ok {
				secondLeft.Store(int64(time.Until(deadline)))
			}
		}
		switch r.URL.Hostname() {
		case "api.fxtwitter.com":
			return response(200, fxBody), true
		case "api.vxtwitter.com":
			return response(200, vxBody), true
		}
		return nil, false
	})
	f.roundTime = 200 * time.Millisecond
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1900000000000000013"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.RetriedAfterTimeout {
		t.Fatal("没有外层时限时该重试")
	}
	if d := time.Duration(secondLeft.Load()); d < 14*time.Second || d > 15*time.Second {
		t.Fatalf("没有外层时限时第二轮该按 15 秒：%v", d)
	}
	if waits := rig.waitList(); len(waits) != 1 || waits[0] != 5*time.Second {
		t.Fatalf("等待不对：%v", waits)
	}
}

// 一条 403 + 其余超时 → 不重试（不为风控或权限重试）。
func TestTimeoutRetryNotWhenClearFailure(t *testing.T) {
	var calls atomic.Int32
	f, err := NewFetcher(FetchOptions{NoSyndication: true, Timeout: 50 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Hostname() == "api.fxtwitter.com" {
			return response(403, `{}`), nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	if err != nil {
		t.Fatal(err)
	}
	waited := 0
	f.retrySleep = func(context.Context, time.Duration) error { waited++; return nil }
	_, err = f.Fetch(context.Background(), link(t, "https://x.com/AP/status/90001"))
	wantError(t, err, "login_required")
	if calls.Load() != 2 || waited != 0 {
		t.Fatalf("明确失败不该重试：请求 %d 次，等了 %d 次", calls.Load(), waited)
	}
	var fe *FetchError
	errors.As(err, &fe)
	if strings.Contains(fe.Trail, retryTrailMarker) || fe.RetriedAfterTimeout {
		t.Fatalf("不该出现重试标记：%+v", fe)
	}
}

// 第二轮也全超时 → 失败、只两轮；Trail 中间写“5 秒后重试”。
func TestTimeoutRetrySecondRoundAlsoTimeout(t *testing.T) {
	f, rig := timeoutRetryFetcher(t, FetchOptions{NoSyndication: true}, func(*http.Request) (*http.Response, bool) { return nil, false })
	_, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/90001"))
	wantError(t, err, "timeout")
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatal(err)
	}
	if waits := rig.waitList(); len(waits) != 1 || waits[0] != timeoutRetryDelay {
		t.Fatalf("只该等一次 5 秒：%v", waits)
	}
	if got := int(rig.calls.Load()); got != 4 {
		t.Fatalf("只该试两轮、每轮两条线路：请求 %d 次", got)
	}
	first, second := attemptRounds(fe.Attempts)
	if len(first) != 2 || len(second) != 2 || !fe.RetriedAfterTimeout {
		t.Fatalf("两轮的线路记录都要在：%+v", fe.Attempts)
	}
	if want := "fx_v2 timeout"; !strings.Contains(fe.Trail, want) || !strings.Contains(fe.Trail, retryTrailMarker) {
		t.Fatalf("Trail 不对：%q", fe.Trail)
	}
	t.Logf("两轮 Trail：%s", fe.Trail)
}

// 外层剩余不到 5 秒等待 + 8 秒第二轮（按 1/100 缩到 50ms + 80ms）→ 不重试。
func TestTimeoutRetrySkippedWhenNoTimeLeft(t *testing.T) {
	f, rig := timeoutRetryFetcher(t, FetchOptions{NoSyndication: true, Timeout: time.Second}, func(*http.Request) (*http.Response, bool) {
		return response(200, fixture(t, "x-video")), true
	})
	f.roundTime = 200 * time.Millisecond
	f.retry = retryTiming{delay: 50 * time.Millisecond, minRound: 80 * time.Millisecond, maxRound: 150 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := f.Fetch(ctx, link(t, "https://x.com/AP/status/1900000000000000013"))
	wantError(t, err, "timeout")
	if waits := rig.waitList(); len(waits) != 0 {
		t.Fatalf("时间不够还等了：%v", waits)
	}
	if got := int(rig.calls.Load()); got != 2 {
		t.Fatalf("不该有第二轮：请求 %d 次", got)
	}
}

// Telegram 这类单线路站点超时也走同一规则：第一轮超时，5 秒后第二轮成功。
func TestTimeoutRetrySingleRouteTelegram(t *testing.T) {
	const page = `<html><body><div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="PaperChannel/300">
<div class="tgme_widget_message_author"><a class="tgme_widget_message_owner_name" href="https://t.me/paperchannel"><span dir="auto">纸云频道</span></a></div>
<div class="tgme_widget_message_text js-message_text">超时后重试取到的正文</div>
<div class="tgme_widget_message_footer"><span class="tgme_widget_message_views">1.2K</span><span class="tgme_widget_message_meta"><a class="tgme_widget_message_date" href="https://t.me/paperchannel/300"><time datetime="2026-11-01T10:30:00+00:00" class="time">10:30</time></a></span></div>
</div></div></body></html>`
	f, rig := timeoutRetryFetcher(t, FetchOptions{}, func(r *http.Request) (*http.Response, bool) {
		if r.URL.Hostname() == "t.me" {
			return response(200, page), true
		}
		return nil, false
	})
	r, err := f.Fetch(context.Background(), link(t, "https://t.me/paperchannel/300"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.RetriedAfterTimeout || r.Text.Value != "超时后重试取到的正文" {
		t.Fatalf("没取到第二轮的正文：%+v", r)
	}
	if waits := rig.waitList(); len(waits) != 1 || waits[0] != timeoutRetryDelay {
		t.Fatalf("等待不对：%v", waits)
	}
	if got := int(rig.calls.Load()); got != 2 {
		t.Fatalf("单线路只该请求两次（每轮一次）：%d", got)
	}
}
