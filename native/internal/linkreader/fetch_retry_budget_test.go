package linkreader

import (
	"context"
	"io"
	"net/http"
	"testing"
)

// 网络重发也是一次请求：冷却中、预算用完时不重发。
func TestNetworkRetryRespectsBudgetAndCooldown(t *testing.T) {
	calls := 0
	f, err := NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 1}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, io.EOF
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = f.request(context.Background(), "https://api.bilibili.com/x/web-interface/view", "test")
	wantError(t, err, "network")
	if calls != 1 {
		t.Fatalf("预算只有 1 次，断连后不该再重发：calls=%d", calls)
	}
	// 预算够时仍然重发一次（原行为）
	calls = 0
	f, err = NewFetcher(FetchOptions{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, io.EOF
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = f.request(context.Background(), "https://api.bilibili.com/x/web-interface/view", "test")
	if calls != 2 {
		t.Fatalf("预算够时应重发一次：calls=%d", calls)
	}
}
