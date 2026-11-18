package linkreader

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// 风控（HTTP 412，JSON 的 -352、-412）和频率码（-509、-799）让整个 B 站平台冷却：冷却期内不再发请求，到期才恢复。

const riskVideo = "https://www.bilibili.com/video/BV1xx411c7mD"

// riskFetcher 返回一个可以切换风控的 B 站假服务器，时间由 now 指针控制。
func riskFetcher(t *testing.T, now *time.Time, risk *bool, calls *int) *Fetcher {
	t.Helper()
	f, err := NewFetcher(FetchOptions{
		Rate: RateOptions{Now: func() time.Time { return *now }},
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			*calls++
			if r.URL.Path == "/x/web-interface/view" {
				if *risk {
					return response(200, `{"code":-352,"message":"风控校验失败"}`), nil
				}
				return response(200, fixture(t, "bili-single")), nil
			}
			return response(200, `{"code":0,"data":[]}`), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// HTTP 412 是风控：报 risk_control，并且同一条链接不再继续发（不做签名重试、不换标识重试）。
func TestBiliHTTP412StopsAtOnce(t *testing.T) {
	calls := 0
	f := testFetcher(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(412, "{}"), nil
	})
	_, err := f.Fetch(context.Background(), link(t, riskVideo))
	wantError(t, err, "risk_control")
	if calls != 1 {
		t.Fatalf("412 之后不该再发请求，实际 %d 次", calls)
	}
}

// 风控后冷却期内，别的 B 站链接也不发请求，直接报 rate_limited；到期后恢复。
func TestBiliRiskCooldownBlocksPlatform(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	risk, calls := true, 0
	f := riskFetcher(t, &now, &risk, &calls)
	ctx := context.Background()
	_, err := f.Fetch(ctx, link(t, riskVideo))
	wantError(t, err, "risk_control")
	n := calls
	_, err = f.Fetch(ctx, link(t, riskVideo))
	wantError(t, err, "rate_limited")
	if calls != n {
		t.Fatalf("冷却期内不该发请求：%d -> %d", n, calls)
	}
	now = now.Add(16 * time.Minute)
	risk = false
	if _, err = f.Fetch(ctx, link(t, riskVideo)); err != nil {
		t.Fatalf("到期后应恢复：%v", err)
	}
}

// 连续撞风控，冷却翻倍（15 分钟、30 分钟）；取到一次成功的数据后，计数归零，重新从 15 分钟起算。
func TestBiliRiskBackoffDoublesAndResets(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	risk, calls := true, 0
	f := riskFetcher(t, &now, &risk, &calls)
	ctx := context.Background()
	fetch := func() error {
		_, err := f.Fetch(ctx, link(t, riskVideo))
		return err
	}
	wantError(t, fetch(), "risk_control")
	now = now.Add(15*time.Minute + time.Second)
	wantError(t, fetch(), "risk_control") // 第二次：30 分钟
	now = now.Add(29 * time.Minute)
	wantError(t, fetch(), "rate_limited")
	now = now.Add(2 * time.Minute)
	risk = false
	if err := fetch(); err != nil {
		t.Fatalf("第二轮冷却到期后应恢复：%v", err)
	}
	risk = true
	wantError(t, fetch(), "risk_control")
	now = now.Add(15*time.Minute + time.Second)
	risk = false
	if err := fetch(); err != nil {
		t.Fatalf("成功后计数应归零，15 分钟后恢复：%v", err)
	}
}

// 冷却时长封顶在 riskMax（2 小时），再撞也不会更长。
func TestRiskCoolCapsAtMax(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r, err := newRateLimiter(RateOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		r.riskCool("bilibili", "")
		now = r.platforms["bilibili"].until.Add(time.Second)
	}
	r.riskCool("bilibili", "")
	if got := r.platforms["bilibili"].until.Sub(now); got != 2*time.Hour {
		t.Fatalf("冷却应封顶 2 小时，实际 %v", got)
	}
}

// Retry-After 比退避时长长就取它（上限 riskMax，再长也只按 2 小时算），比退避短就还是退避时长。
func TestRiskCoolRetryAfter(t *testing.T) {
	for _, c := range []struct {
		retry string
		want  time.Duration
	}{{"60", 15 * time.Minute}, {"3600", time.Hour}, {"99999", 2 * time.Hour}, {"soon", 15 * time.Minute}} {
		now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
		r, err := newRateLimiter(RateOptions{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		r.riskCool("bilibili", c.retry)
		now = now.Add(c.want - time.Second)
		if err := r.allow("bilibili", false); err == nil {
			t.Fatalf("Retry-After=%q：%v 内应还在冷却", c.retry, c.want)
		}
		now = now.Add(2 * time.Second)
		if err := r.allow("bilibili", false); err != nil {
			t.Fatalf("Retry-After=%q：%v 后应恢复：%v", c.retry, c.want, err)
		}
	}
}

// 频率码（-799）同样让平台冷却；被吞掉的子请求（比如专栏的 view）也要记账，不能因为调用方没报错就漏掉。
func TestBiliFrequencyCodeCools(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	calls := 0
	f, err := NewFetcher(FetchOptions{
		Rate: RateOptions{Now: func() time.Time { return now }},
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return response(200, `{"code":-799,"message":"请求过于频繁"}`), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := f.Fetch(ctx, link(t, riskVideo)); err == nil {
		t.Fatal("-799 应报错")
	}
	n := calls
	_, err = f.Fetch(ctx, link(t, riskVideo))
	wantError(t, err, "rate_limited")
	if calls != n {
		t.Fatalf("频率码后冷却期内不该发请求：%d -> %d", n, calls)
	}
}

// 没有 riskBase 的平台（比如 GitHub）遇到 412 或风控码，不冷却，照原来的走。
func TestRiskCoolOnlyBilibili(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r, err := newRateLimiter(RateOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	r.riskCool("github", "")
	if err := r.allow("github", false); err != nil {
		t.Fatalf("GitHub 不该因风控冷却：%v", err)
	}
}

// 形状 × 入口：HTTP 412 在 B 站每个入口（视频、直播、小店、专栏页、短链）都认成风控，
// 整个平台一起冷却：之后发往任何 B 站主机的请求都不出门。别的平台的 412 不受影响。
func TestBiliHTTP412EveryEntryCoolsWholePlatform(t *testing.T) {
	hosts := []string{
		"https://api.bilibili.com/x/web-interface/view?bvid=BV1GJ411x7h7",
		"https://api.live.bilibili.com/room/v1/Room/get_info?room_id=1",
		"https://mall.bilibili.com/mall-up-search/items/info?itemsId=1",
		"https://www.bilibili.com/read/cv1/",
		"https://space.bilibili.com/1",
		"https://b23.tv/abc",
	}
	for _, first := range hosts {
		t.Run(first, func(t *testing.T) {
			now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
			calls := 0
			f, err := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(412, "<html>风控</html>"), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			_, status, _, err := f.request(ctx, first, "test")
			wantError(t, err, "risk_control")
			if status != 412 || calls != 1 {
				t.Fatalf("status=%d calls=%d", status, calls)
			}
			for _, other := range hosts {
				_, _, _, err = f.request(ctx, other, "test")
				wantError(t, err, "rate_limited")
			}
			if calls != 1 {
				t.Fatalf("冷却期内不该再有请求出门：%d", calls)
			}
			now = now.Add(15*time.Minute + time.Second)
			if _, _, _, err = f.request(ctx, first, "test"); err == nil || calls != 2 {
				t.Fatalf("15 分钟后应放行一次（这次还是 412）：calls=%d err=%v", calls, err)
			}
		})
	}
}

// 别的平台的 412 还是普通状态码：不报 risk_control，不冷却，也不影响 B 站。
func TestOther412IsNotBiliRisk(t *testing.T) {
	calls := 0
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "api.github.com" {
			return response(412, "{}"), nil
		}
		return response(200, `{"code":0}`), nil
	})
	ctx := context.Background()
	for range 3 {
		_, status, _, err := f.request(ctx, "https://api.github.com/repos/a/b", "test")
		if err != nil || status != 412 {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
	if _, _, _, err := f.request(ctx, "https://api.bilibili.com/x/web-interface/view", "test"); err != nil {
		t.Fatalf("GitHub 的 412 不该连累 B 站：%v", err)
	}
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
}

// 业务码：-352、-412 是风控（冷却 15 分钟起、翻倍）；-509、-799 是频率码（冷却 5 分钟、不翻倍）；0 和不是 JSON 的不冷却。
func TestBiliBodyCodesCoolByKind(t *testing.T) {
	for _, c := range []struct {
		body string
		cool time.Duration
	}{
		{`{"code":-352}`, 15 * time.Minute}, {`{"code":-412}`, 15 * time.Minute},
		{`{"code":-509}`, 5 * time.Minute}, {`{"code":-799}`, 5 * time.Minute},
		{`{"code":0,"data":{}}`, 0}, {`{"code":-404}`, 0}, {`<html>code -352</html>`, 0}, {`{"data":{"code":-352}}`, 0},
	} {
		t.Run(c.body, func(t *testing.T) {
			now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
			f, err := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(200, c.body), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			const raw = "https://api.bilibili.com/x/web-interface/view"
			_, _, _, _ = f.request(context.Background(), raw, "test") // 这一层只看记账，不看调用方怎么解读
			if c.cool == 0 {
				if _, _, _, err = f.request(context.Background(), raw, "test"); err != nil {
					t.Fatalf("不该冷却：%v", err)
				}
				return
			}
			now = now.Add(c.cool - time.Second)
			if _, _, _, err = f.request(context.Background(), raw, "test"); err == nil {
				t.Fatalf("%v 内应还在冷却", c.cool)
			}
			now = now.Add(2 * time.Second)
			if _, _, _, err = f.request(context.Background(), raw, "test"); err != nil {
				t.Fatalf("%v 后应恢复：%v", c.cool, err)
			}
		})
	}
}

// 频率码连着撞不翻倍；比风控的冷却短，已经在更长的风控冷却里就不缩短。
func TestFreqCoolDoesNotDoubleOrShorten(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r, err := newRateLimiter(RateOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		r.freqCool("bilibili", "")
		now = r.platforms["bilibili"].until.Add(time.Second)
	}
	r.freqCool("bilibili", "")
	if got := r.platforms["bilibili"].until.Sub(now); got != 5*time.Minute {
		t.Fatalf("频率码冷却应一直是 5 分钟，实际 %v", got)
	}
	r.riskCool("bilibili", "")
	until := r.platforms["bilibili"].until
	r.freqCool("bilibili", "")
	if !r.platforms["bilibili"].until.Equal(until) {
		t.Fatal("频率码不该缩短风控冷却")
	}
}

// 冷却前发出的在途请求迟到的成功，不能把连续风控的计数清零；冷却结束后的成功才清零。
func TestLateSuccessDoesNotResetStreak(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r, err := newRateLimiter(RateOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	r.riskCool("bilibili", "")
	r.riskReset("bilibili") // 冷却中迟到的成功
	if r.platforms["bilibili"].streak != 1 {
		t.Fatalf("冷却期内的成功不该清零：%d", r.platforms["bilibili"].streak)
	}
	now = now.Add(15*time.Minute + time.Second)
	r.riskCool("bilibili", "") // 第二轮：30 分钟
	if got := r.platforms["bilibili"].until.Sub(now); got != 30*time.Minute {
		t.Fatalf("第二轮应 30 分钟：%v", got)
	}
	now = now.Add(31 * time.Minute)
	r.riskReset("bilibili")
	if r.platforms["bilibili"].streak != 0 {
		t.Fatal("冷却结束后的成功应清零")
	}
}

// 同一批在途请求陆续报回风控，只算一轮：冷却不会被它们滚成 30 分钟。
func TestRiskBurstCountsOnce(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	r, err := newRateLimiter(RateOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		r.riskCool("bilibili", "")
		now = now.Add(time.Second)
	}
	if got := r.platforms["bilibili"].streak; got != 1 {
		t.Fatalf("同一批只算一轮：streak=%d", got)
	}
}

// 短链解析（b23.tv）也走同一个 send：撞 412 记账冷却，冷却中不发请求。
func TestBiliShortLinkResolveRespectsCooldown(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	calls := 0
	f, err := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(412, "<html></html>"), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	short := link(t, "https://b23.tv/AbCd123")
	_, err = f.Resolve(context.Background(), short)
	wantError(t, err, "risk_control")
	_, err = f.Resolve(context.Background(), short)
	wantError(t, err, "rate_limited")
	_, err = f.Fetch(context.Background(), link(t, riskVideo))
	wantError(t, err, "rate_limited")
	if calls != 1 {
		t.Fatalf("冷却中不该再发请求：%d", calls)
	}
}
