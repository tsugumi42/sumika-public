package linkreader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func TestPinnedProxyDial(t *testing.T) {
	targets := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets <- r.Host
		if r.Method != "CONNECT" {
			t.Error("expected CONNECT")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	proxy, _ := url.Parse(server.URL)
	calls := 0
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		calls++
		if calls > 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	conn, err := safeDial(proxy, lookup, netPolicy{})(context.Background(), "tcp", "api.fxtwitter.com:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if target := <-targets; target != "1.1.1.1:443" || calls != 1 {
		t.Fatalf("proxy target=%s lookups=%d", target, calls)
	}
}
func TestMixedDNSRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	proxy, _ := url.Parse(server.URL)
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("::ffff:127.0.0.1")}, nil
	}
	conn, err := safeDial(proxy, lookup, netPolicy{})(context.Background(), "tcp", "x.com:443")
	if conn != nil {
		conn.Close()
		t.Fatal("mixed DNS connected")
	}
	wantError(t, err, "unsafe_target")
}
func TestResolveForgedTarget(t *testing.T) {
	calls := 0
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { calls++; return response(200, ""), nil })
	_, err := f.Resolve(context.Background(), Link{Platform: "x", Kind: "short", Clean: "http://127.0.0.1/private"})
	wantError(t, err, "invalid_target")
	if calls != 0 {
		t.Fatal("forged target reached transport")
	}
}
