package web_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/web"
	"sumika-native/internal/web/httpfetch"
	"sumika-native/internal/web/tavily"
)

type resolver struct{ ips []netip.Addr }
type webLifetime struct{}
type webActivity struct{}

func (webLifetime) Begin(ctx context.Context, _ kernel.Invocation) (context.Context, kernel.Activity, error) {
	return ctx, webActivity{}, nil
}
func (webLifetime) Seal()                       {}
func (webLifetime) Drain(context.Context) error { return nil }
func (webActivity) End(error)                   {}

func (r resolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.ips, nil
}
func webFixture(t *testing.T) (store.EcosystemStore, ecosystem.Access, kernel.Invocation) {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := set.Close(); err != nil {
			t.Error(err)
		}
	})
	s := kernel.Scope{CharacterID: "fixture", Conversation: "fixture", ScopeID: "fixture", Platform: "local", Channel: "console", Scene: "private", AccountID: "60001", ActorKind: "owner", ActorID: "60002", AuthorityID: ecosystem.ID(), AuthorityRevision: 1}
	a, err := ecosystem.NewAuthority().Issue(s, []string{"web:search", "web:fetch"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return store.EcosystemStore{DB: set.TraceStore().Trace}, a, kernel.Invocation{Scope: s, RootID: ecosystem.ID(), RunID: ecosystem.ID(), Kind: "foreground"}
}
func TestWebURLAndQuota(t *testing.T) {
	ctx := t.Context()
	public := netip.AddrFrom4([4]byte{93, 184, 216, 34})
	policy := web.URLPolicy{Resolver: resolver{[]netip.Addr{public}}}
	for _, raw := range []string{"http://127.0.0.1/x", "http://localhost/x", "http://example.local/x", "file:///private", "https://a:b@example.com/x", "http://example.com:3001/x", "https://example.com/?token=secret", "http://[::ffff:127.0.0.1]/x"} {
		if _, err := policy.Check(ctx, raw); err == nil {
			t.Fatalf("危险 URL 未拒绝 %s", raw)
		}
	}
	mixed := web.URLPolicy{Resolver: resolver{[]netip.Addr{public, netip.MustParseAddr("127.0.0.1")}}}
	if _, err := mixed.Check(ctx, "https://example.com/"); err == nil {
		t.Fatal("混合 DNS 未拒绝")
	}
	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "http://127.0.0.1/private", 302)
		case "/oversize":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, strings.Repeat("x", (5<<20)+1))
		case "/no-store":
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "fixture-body")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<html><body><p>fixture-body</p><script>evil()</script></body></html>")
		}
	}))
	defer fake.Close()
	fetch := &httpfetch.Provider{Policy: policy, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if !strings.HasPrefix(address, public.String()+":") {
			return nil, fmt.Errorf("未固定验证 IP: %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(fake.URL, "http://"))
	}, Version: "fixture"}
	journal, grant, inv := webFixture(t)
	limit := int64(100)
	credits := int64(10 * 1_000_000)
	service, err := web.New(web.Options{Enabled: true, Lifetime: webLifetime{}, Policy: policy, Journal: journal, DailySearch: &limit, DailyFetch: &limit, MonthlyMicroCredits: &credits}, tavily.New("fixture-key", nil), fetch)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	a, err := web.NewAccess(grant, inv)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/redirect", "/oversize"} {
		if _, err := service.Fetch(ctx, a, web.FetchRequest{URL: "http://example.com" + path}); err == nil {
			t.Fatal("危险抓取成功", path)
		}
	}
	before := calls.Load()
	for range 2 {
		res, err := service.Fetch(ctx, a, web.FetchRequest{URL: "http://example.com/no-store", Format: "text"})
		if err != nil || res.Content != "fixture-body" {
			t.Fatal(res, err)
		}
	}
	if calls.Load()-before != 2 {
		t.Fatal("no-store 被缓存")
	}
	zero := int64(0)
	blocked, err := web.New(web.Options{Enabled: true, Lifetime: webLifetime{}, Policy: policy, Journal: journal, DailySearch: &limit, DailyFetch: &zero, MonthlyMicroCredits: &credits}, tavily.New("fixture-key", nil), fetch)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close(context.Background())
	before = calls.Load()
	if _, err := blocked.Fetch(ctx, a, web.FetchRequest{URL: "http://example.com/zero"}); err == nil {
		t.Fatal("零额度出网")
	}
	if calls.Load() != before {
		t.Fatal("额度拒绝后仍派发")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.Fetch(cancelled, a, web.FetchRequest{URL: "http://example.com/cancel"}); err == nil {
		t.Fatal("取消后仍执行")
	}
	var dispatched int
	if err := journal.DB.QueryRow(`SELECT count(*) FROM kernel_web_usage`).Scan(&dispatched); err != nil {
		t.Fatal(err)
	}
	if dispatched != 4 {
		t.Fatalf("派发失败未记账: %d", dispatched)
	}
}
