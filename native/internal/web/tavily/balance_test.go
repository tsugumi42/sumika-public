package tavily

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchBalance(t *testing.T) {
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/usage" || r.Header.Get("Authorization") != "Bearer k-test" {
			t.Errorf("请求不对：%s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	p := New("k-test", nil)
	p.base = srv.URL
	for _, tc := range []struct {
		body      string
		remaining int64
		unlimited bool
		fail      bool
	}{
		{`{"key":{"usage":150,"limit":1000},"account":{"plan_usage":500,"plan_limit":15000}}`, 850, false, false},
		{`{"key":{"usage":150,"limit":null}}`, 0, true, false},
		{`{"key":{"usage":2000,"limit":1000}}`, 0, false, false},
		{`{"account":{}}`, 0, false, true},
	} {
		body = tc.body
		remaining, unlimited, err := p.FetchBalance(t.Context())
		if (err != nil) != tc.fail || remaining != tc.remaining || unlimited != tc.unlimited {
			t.Fatalf("%s -> %d %v %v", tc.body, remaining, unlimited, err)
		}
	}
}
