package linkreader

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 同一个视频短时间内不重复请求：并发的同一链接合成一次取数，取到的结果缓存 24 小时，之后不再发请求；
// 取数撞了风控，之后别的 B 站链接和同一链接都不出门，直到冷却结束。

func biliService(t *testing.T, now *time.Time, handler func(*http.Request) (*http.Response, error)) *Service {
	t.Helper()
	f, err := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return *now }}, Transport: roundTripFunc(handler)})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := cacheStores(t)
	return cacheService(t, st, f, func() time.Time { return *now })
}

func TestBiliServiceDedupesAndCaches(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var views atomic.Int64
	gate := make(chan struct{})
	s := biliService(t, &now, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/x/web-interface/view" {
			views.Add(1)
			<-gate
			return response(200, fixture(t, "bili-single")), nil
		}
		return response(200, `{"code":0,"data":[]}`), nil
	})
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Get(context.Background(), riskVideo); err != nil {
				t.Error(err)
			}
		}()
	}
	waitFlight(t, s, link(t, riskVideo).Clean, 5) // 五个等待者都挂到同一个取数上再放行
	close(gate)
	wg.Wait()
	if _, err := s.Get(context.Background(), riskVideo); err != nil {
		t.Fatal(err)
	}
	if got := views.Load(); got != 1 {
		t.Fatalf("同一个视频的 view 只该请求一次：%d", got)
	}
}

func TestBiliServiceRiskStopsLaterLinks(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	calls := 0
	s := biliService(t, &now, func(*http.Request) (*http.Response, error) {
		calls++
		return response(412, "{}"), nil
	})
	ctx := context.Background()
	_, err := s.Get(ctx, riskVideo)
	wantError(t, err, "risk_control")
	for _, raw := range []string{riskVideo, "https://www.bilibili.com/video/BV1xx411c7mD", "https://www.bilibili.com/read/cv1", "https://live.bilibili.com/1"} {
		_, err = s.Get(ctx, raw)
		wantError(t, err, "rate_limited")
	}
	if calls != 1 {
		t.Fatalf("风控之后别的链接和同一链接都不该出门：%d", calls)
	}
}
