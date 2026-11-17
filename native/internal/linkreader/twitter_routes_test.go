package linkreader

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// xRace 是一个只回 X 两条线路的假传输：hold 里的那条线路一直等到请求被取消，另一条照 body 回。
func xRace(t *testing.T, hold string, body map[string]func() *http.Response) *Fetcher {
	t.Helper()
	f, err := NewFetcher(FetchOptions{NoSyndication: true, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		route := map[string]string{"api.fxtwitter.com": RouteFX, "api.vxtwitter.com": RouteVX}[r.URL.Hostname()]
		if route == hold {
			<-r.Context().Done() // 等另一条先到、把这条取消
			return nil, r.Context().Err()
		}
		if b := body[route]; b != nil {
			return b(), nil
		}
		return response(503, ""), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// 两条同时取，先到的成功结果为准，另一条被取消；被取消的不算那条线路失败，也不和赢的那份合并字段。
func TestXFetchRaceFirstWins(t *testing.T) {
	const id = "1900000000000000013"
	fx := func() *http.Response { return response(200, fixture(t, "x-video")) }
	vx := func() *http.Response { return response(200, fixture(t, "vx-video")) }
	for _, c := range []struct{ hold, win string }{{RouteFX, RouteVX}, {RouteVX, RouteFX}} {
		t.Run(c.win, func(t *testing.T) {
			f := xRace(t, c.hold, map[string]func() *http.Response{RouteFX: fx, RouteVX: vx})
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/"+id))
			if err != nil {
				t.Fatal(err)
			}
			if r.Source != c.win {
				t.Fatalf("该用 %s 的结果，用了 %s", c.win, r.Source)
			}
			// 两份数据不混：vx 不给作者 ID，赢的是 vx 时作者 ID 必须是“没返回”，不能从 fx 那份补进来
			if c.win == RouteVX && r.Author.ID.State != "not_returned" {
				t.Fatalf("两条线路的字段混在一起了：%+v", r.Author)
			}
			if len(r.Attempts) != 2 || r.Attempts[0].Route != c.win || r.Attempts[0].Kind != "ok" || r.Attempts[1].Route != c.hold || r.Attempts[1].Kind != "canceled" {
				t.Fatalf("线路记录：%+v", r.Attempts)
			}
			if got := len(f.xStats.hist[c.hold]); got != 0 {
				t.Fatalf("被取消的 %s 不该记成败，记了 %d 次", c.hold, got)
			}
		})
	}
}

// 两条都失败才算失败：报答上来了的那条（fx 的 404），线路记录里两条都有、各带耗时。
func TestXFetchBothFailTrail(t *testing.T) {
	f := xRace(t, "", map[string]func() *http.Response{
		RouteFX: func() *http.Response { return response(404, `{}`) },
		RouteVX: func() *http.Response { return response(500, `{}`) },
	})
	_, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/90001"))
	wantError(t, err, "not_found")
	var fe *FetchError
	errors.As(err, &fe)
	if fe.Source != RouteFX || !strings.Contains(fe.Trail, "fx_v2 not_found") || !strings.Contains(fe.Trail, "vx interface_error") || len(fe.Attempts) != 2 {
		t.Fatalf("线路记录不全：%+v", fe)
	}
	if f.xStats.rate(RouteFX) >= 0.5 || f.xStats.rate(RouteVX) >= 0.5 {
		t.Fatal("两条失败都该记进成功率")
	}
}

// fx_v2 有自己的短时限（linkReader.x.fxTimeoutSeconds），到点算 fx 超时，不拖到取数客户端的 10 秒。
// vx 回 503、没有备用路线：结果只能等 fx 自己到点，所以时限没接上这条就会等满 10 秒。
func TestXFetchFXTimeout(t *testing.T) {
	f, err := NewFetcher(FetchOptions{NoSyndication: true, FXTimeout: 50 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "api.fxtwitter.com" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return response(503, ""), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	_, err = f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1900000000000000013"))
	took := time.Since(began)
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("两条都失败该报错：%v", err)
	}
	var fx *RouteAttempt
	for i := range fe.Attempts {
		if fe.Attempts[i].Route == RouteFX {
			fx = &fe.Attempts[i]
		}
	}
	if fx == nil || fx.Kind != "timeout" {
		t.Fatalf("fx 该记成超时：%+v", fe.Attempts)
	}
	if took > 3*time.Second {
		t.Fatalf("fx 的时限没生效，等了 %v", took)
	}
	if _, err := NewFetcher(FetchOptions{FXTimeout: -time.Second}); err == nil {
		t.Fatal("负的时限该报错")
	}
}

func TestXRouteOrderBySuccess(t *testing.T) {
	f, err := NewFetcher(FetchOptions{NoSyndication: true, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(503, ""), nil })})
	if err != nil {
		t.Fatal(err)
	}
	names := func() string {
		var s []string
		for _, r := range f.xRoutes() {
			s = append(s, r.name)
		}
		return strings.Join(s, ",")
	}
	if got := names(); got != "fx_v2,vx" {
		t.Fatalf("没有记录时按默认顺序：%s", got)
	}
	for range 60 {
		f.xStats.record(RouteFX, false)
		f.xStats.record(RouteVX, true)
	}
	if got := names(); got != "vx,fx_v2" {
		t.Fatalf("成功率高的该排前面：%s", got)
	}
	if n := len(f.xStats.hist[RouteFX]); n != routeWindow {
		t.Fatalf("只留最近 %d 次，留了 %d", routeWindow, n)
	}
	// 最近 50 次里 fx 翻身：只看窗口里的，早先的失败不再算
	for range routeWindow {
		f.xStats.record(RouteFX, true)
	}
	f.xStats.record(RouteVX, false)
	if got := names(); got != "fx_v2,vx" {
		t.Fatalf("窗口外的旧记录不该再算：%s", got)
	}
}

// sync32 是测试里并发记主机名用的小集合
type sync32 struct {
	mu sync.Mutex
	s  []string
}

func (h *sync32) add(v string) { h.mu.Lock(); h.s = append(h.s, v); h.mu.Unlock() }
func (h *sync32) has(v string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.s, v)
}
func (h *sync32) list() []string { h.mu.Lock(); defer h.mu.Unlock(); return slices.Clone(h.s) }
