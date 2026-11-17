package linkreader

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// dohBarrier 是假的 DoH 服务：每个请求都要等到 want 个请求同时到达才放行，
// 所以请求串行发出时会卡住（超时返回 500），并发发出才通过。
type dohBarrier struct {
	want    int
	calls   atomic.Int32
	mu      sync.Mutex
	arrived int
	gate    chan struct{}
	once    sync.Once
}

func newDohBarrier(want int) *dohBarrier { return &dohBarrier{want: want, gate: make(chan struct{})} }
func (d *dohBarrier) RoundTrip(r *http.Request) (*http.Response, error) {
	d.calls.Add(1)
	d.mu.Lock()
	d.arrived++
	gate := d.gate
	if d.arrived%d.want == 0 {
		close(d.gate)
		d.gate = make(chan struct{})
	}
	d.mu.Unlock()
	select {
	case <-gate:
	case <-time.After(1500 * time.Millisecond):
		return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	kind := r.URL.Query().Get("type")
	body := `{"Status":0,"Answer":[{"type":1,"data":"93.184.216.34"}]}`
	if kind == "AAAA" {
		body = `{"Status":0,"Answer":[{"type":28,"data":"2606:2800:220:1::1"}]}`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func proxyFetcher(t *testing.T, doh http.RoundTripper) *Fetcher {
	t.Helper()
	f, err := NewFetcher(FetchOptions{Proxy: "http://127.0.0.1:1", DoHTransport: doh})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// 经代理解析时 A 和 AAAA 同时发出，一轮往返而不是两轮（取数时限被串行 DoH 吃掉过：Spotify 超时）。
func TestProxyLookupQueriesAandAAAAConcurrently(t *testing.T) {
	doh := newDohBarrier(2)
	f := proxyFetcher(t, doh)
	ips, err := f.lookup(context.Background(), "open.spotify.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 2 || doh.calls.Load() != 2 {
		t.Fatalf("ips=%v calls=%d", ips, doh.calls.Load())
	}
}

// 同一主机的解析结果缓存，一次取数里重复解析不再重复请求 DoH。
func TestProxyLookupCachesByHost(t *testing.T) {
	doh := newDohBarrier(2)
	f := proxyFetcher(t, doh)
	for range 3 {
		if _, err := f.lookup(context.Background(), "open.spotify.com"); err != nil {
			t.Fatal(err)
		}
	}
	if got := doh.calls.Load(); got != 2 {
		t.Fatalf("同一主机解析三次应只查一轮 A+AAAA，实际 DoH 请求 %d 次", got)
	}
	if _, err := f.lookup(context.Background(), "i.scdn.co"); err != nil {
		t.Fatal(err)
	}
	if got := doh.calls.Load(); got != 4 {
		t.Fatalf("换主机要重新查：%d", got)
	}
}

// 限速按主机算：不同主机互不等待，同一主机仍然隔开。
func TestRequestIntervalIsPerHost(t *testing.T) {
	const gap = 700 * time.Millisecond
	f, err := NewFetcher(FetchOptions{MinInterval: gap, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, "{}"), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, _, err = f.request(ctx, "https://open.spotify.com/a", "t"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, _, _, err = f.request(ctx, "https://t.me/b", "t"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > gap/2 {
		t.Fatalf("另一个主机不该等：%v", d)
	}
	start = time.Now()
	if _, _, _, err = f.request(ctx, "https://open.spotify.com/c", "t"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < gap*8/10 {
		t.Fatalf("同一主机应隔开：%v", d)
	}
}

// 封面等媒体主机的检查并发解析：两个主机同时到达才放行，串行就卡住。
func TestInspectMediaResolvesHostsConcurrently(t *testing.T) {
	f, err := NewFetcher(FetchOptions{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, "{}"), nil })})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	arrived := 0
	gate := make(chan struct{})
	var calls atomic.Int32
	f.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		calls.Add(1)
		mu.Lock()
		arrived++
		if arrived == 2 {
			close(gate)
		}
		mu.Unlock()
		select {
		case <-gate:
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		case <-time.After(1500 * time.Millisecond):
			return nil, failure("network", "test")
		}
	}
	r := Result{Media: available([]Media{{Type: "image", URL: available("https://i.scdn.co/a"), Thumbnail: available("https://image-cdn-ak.spotifycdn.com/b")}})}
	f.inspectMedia(context.Background(), &r)
	m := r.Media.Value[0]
	if m.URL.State != "available" || m.Thumbnail.State != "available" {
		t.Fatalf("url=%+v thumb=%+v", m.URL, m.Thumbnail)
	}
	if calls.Load() != 2 {
		t.Fatalf("每个主机只解析一次：%d", calls.Load())
	}
}

// 取数各路都超时：错误里要写清是哪几路、各自花了多久，不能只剩最后一路的来源（日志只写“timeout，来源 vx”时分不清）。
// 全部超时会整条重试一次（规格 C3），两轮的线路记录都接在 Trail 里。
func TestXFallbackErrorKeepsRouteTrail(t *testing.T) {
	var mu sync.Mutex
	var hosts []string
	f, err := NewFetcher(FetchOptions{NoSyndication: true, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		hosts = append(hosts, r.URL.Hostname())
		mu.Unlock()
		return nil, context.DeadlineExceeded
	})})
	if err != nil {
		t.Fatal(err)
	}
	f.retrySleep = func(context.Context, time.Duration) error { return nil } // 不真等 5 秒
	_, err = f.Fetch(context.Background(), link(t, "https://x.com/someone/status/90001"))
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != "timeout" || (fe.Source != "vx" && fe.Source != "fx_v2") {
		t.Fatalf("应是超时: %v", err)
	}
	if len(hosts) != 4 || !strings.Contains(fe.Trail, "fx_v2 timeout") || !strings.Contains(fe.Trail, "vx timeout") || !strings.Contains(fe.Trail, retryTrailMarker) || len(fe.Attempts) != 4 {
		t.Fatalf("线路记录不对: hosts=%v trail=%q", hosts, fe.Trail)
	}
}
