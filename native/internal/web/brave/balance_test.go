package brave

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"sumika-native/internal/web"
)

func TestMonthlyRemainingFromHeaders(t *testing.T) {
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "1, 2000")
		w.Header().Set("X-RateLimit-Remaining", "0, 1990")
		w.WriteHeader(status)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	bal := web.NewBalances()
	p := New("k", nil)
	p.base, p.do, p.bal = srv.URL, plain, bal
	if _, err := p.Search(t.Context(), web.ProviderSearchRequest{Query: "q", MaxResults: 5}); err != nil {
		t.Fatal(err)
	}
	if b, ok := bal.Get("brave"); !ok || b.Remaining != 1990 || b.Unlimited {
		t.Fatalf("应读到本月剩余 1990（第二个值）：%+v", b)
	}
	// 月度用完：429 且本月剩余为 0。
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "1, 0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := p.Search(t.Context(), web.ProviderSearchRequest{Query: "q", MaxResults: 5})
	if !web.IsExhausted(err) {
		t.Fatalf("月度耗尽应报额度用完: %v", err)
	}
	if b, _ := bal.Get("brave"); b.Remaining != 0 {
		t.Fatalf("%+v", b)
	}
	// 只是每秒限流（本月还有）：不当作用完。
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0, 500")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err = p.Search(t.Context(), web.ProviderSearchRequest{Query: "q", MaxResults: 5}); err == nil || web.IsExhausted(err) {
		t.Fatalf("每秒限流不是额度用完: %v", err)
	}
}
