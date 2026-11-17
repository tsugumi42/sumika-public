package exa

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"sumika-native/internal/web"
)

func TestPaymentRequiredMarksExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	bal := web.NewBalances()
	p := New("k", nil)
	p.base, p.do, p.bal = srv.URL, plain, bal
	_, err := p.Search(t.Context(), web.ProviderSearchRequest{Query: "q", MaxResults: 5})
	if !web.IsExhausted(err) {
		t.Fatalf("402 应报额度用完: %v", err)
	}
	if !bal.Exhausted("exa", false) {
		t.Fatal("应记为用完")
	}
}
