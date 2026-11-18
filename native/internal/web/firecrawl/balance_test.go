package firecrawl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"sumika-native/internal/web"
)

func TestFetchBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/team/credit-usage" || r.Header.Get("Authorization") != "Bearer k-test" {
			t.Errorf("请求不对：%s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"success":true,"data":{"remainingCredits":1000,"planCredits":500000}}`))
	}))
	defer srv.Close()
	p := New("k-test", nil)
	p.base = srv.URL
	remaining, unlimited, err := p.FetchBalance(t.Context())
	if err != nil || remaining != 1000 || unlimited {
		t.Fatalf("%d %v %v", remaining, unlimited, err)
	}
}

func TestPaymentRequiredMarksExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	bal := web.NewBalances()
	p := New("k-test", nil)
	p.base, p.bal = srv.URL, bal
	p.do = func(_ context.Context, c *http.Client, r *http.Request) (*http.Response, error) { return c.Do(r) }
	_, err := p.Search(t.Context(), web.ProviderSearchRequest{Query: "q", MaxResults: 3})
	if !web.IsExhausted(err) {
		t.Fatalf("402 应报额度用完: %v", err)
	}
	if b, ok := bal.Get("firecrawl"); !ok || b.Remaining != 0 || !b.Known {
		t.Fatalf("应记为 0：%+v", b)
	}
}
