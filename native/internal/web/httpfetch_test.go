package web_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/web"
	"sumika-native/internal/web/httpfetch"
	"sumika-native/internal/web/tavily"
)

// hostResolver 按域名给解析结果，测试里模拟不同站点落在公网、内网或代理假地址。
type hostResolver map[string][]netip.Addr

func (r hostResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	return r[host], nil
}

type fetchHarness struct {
	service *web.Service
	access  web.Access
	mu      sync.Mutex
	dialed  []string
	headers []http.Header
	hosts   []string
}

// newFetchHarness 起一个假站点，provider 的拨号全部转到它，同时记录拨了哪些地址。
func newFetchHarness(t *testing.T, resolver hostResolver, handler http.HandlerFunc, proxy func(*http.Request) (*url.URL, error), unlimited ...bool) *fetchHarness {
	t.Helper()
	h := &fetchHarness{}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.headers = append(h.headers, r.Header.Clone())
		h.hosts = append(h.hosts, r.Host+"|"+r.URL.String())
		h.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(site.Close)
	policy := web.URLPolicy{Resolver: resolver}
	fetch := &httpfetch.Provider{Policy: policy, Proxy: proxy, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		h.mu.Lock()
		h.dialed = append(h.dialed, address)
		h.mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(site.URL, "http://"))
	}}
	journal, grant, inv := webFixture(t)
	limit := int64(100)
	credits := int64(10 * 1_000_000)
	opts := web.Options{Enabled: true, Lifetime: webLifetime{}, Policy: policy, Journal: journal, DailySearch: &limit, DailyFetch: &limit, MonthlyMicroCredits: &credits}
	if len(unlimited) > 0 && unlimited[0] {
		opts.DailySearch, opts.DailyFetch, opts.MonthlyMicroCredits = nil, nil, nil
	}
	service, err := web.New(opts, tavily.New("fixture-key", nil), fetch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close(context.Background()) })
	h.service = service
	h.access, err = web.NewAccess(grant, inv)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func page(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, body)
}

var (
	publicAddr = netip.MustParseAddr("93.184.216.34")
	fakeAddr   = netip.MustParseAddr("198.18.4.160")
)

func TestFetchAllowsCrossSiteRedirectAndSendsBrowserHeaders(t *testing.T) {
	r := hostResolver{"example.com": {publicAddr}, "www.example.com": {publicAddr}}
	h := newFetchHarness(t, r, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/start" {
			http.Redirect(w, req, "http://www.example.com/final", 301)
			return
		}
		page(w, "final-body")
	}, nil)
	res, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/start", Format: "text"})
	if err != nil || res.Content != "final-body" {
		t.Fatalf("跨站跳转应成功: %+v %v", res, err)
	}
	if res.ReturnedURL != "http://www.example.com/final" {
		t.Fatalf("最终地址: %s", res.ReturnedURL)
	}
	hd := h.headers[0]
	if !strings.HasPrefix(hd.Get("User-Agent"), "Mozilla/5.0") || strings.Contains(hd.Get("User-Agent"), "Sumika") {
		t.Fatalf("User-Agent: %q", hd.Get("User-Agent"))
	}
	if hd.Get("Accept") == "" || hd.Get("Accept-Language") == "" {
		t.Fatalf("缺 Accept 头: %v", hd)
	}
}

func TestFetchRedirectToInternalHostIsRefused(t *testing.T) {
	r := hostResolver{"example.com": {publicAddr}, "intranet.example": {netip.MustParseAddr("10.0.0.5")}}
	h := newFetchHarness(t, r, func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "http://intranet.example/secret", 302)
	}, nil)
	if _, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/start"}); err == nil || !strings.Contains(err.Error(), "WEB_ADDRESS_BLOCKED") {
		t.Fatalf("跳到内网应被拒: %v", err)
	}
	if len(h.dialed) != 1 {
		t.Fatalf("内网主机不该被拨号: %v", h.dialed)
	}
}

func TestFetchFakeIPNotTreatedAsInternal(t *testing.T) {
	r := hostResolver{"example.com": {fakeAddr}}
	// 无代理：直连假地址（本机 TUN 接管）。
	h := newFetchHarness(t, r, func(w http.ResponseWriter, req *http.Request) { page(w, "via-tun") }, func(*http.Request) (*url.URL, error) { return nil, nil })
	res, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/x", Format: "text"})
	if err != nil || res.Content != "via-tun" {
		t.Fatalf("假 IP 不该被当内网: %+v %v", res, err)
	}
	if len(h.dialed) != 1 || h.dialed[0] != "198.18.4.160:80" {
		t.Fatalf("应直连假地址: %v", h.dialed)
	}
	// 有代理：交给代理按域名解析，不再钉假地址。
	proxied := newFetchHarness(t, r, func(w http.ResponseWriter, req *http.Request) { page(w, "via-proxy") }, func(*http.Request) (*url.URL, error) {
		return url.Parse("http://127.0.0.1:7897")
	})
	res, err = proxied.service.Fetch(t.Context(), proxied.access, web.FetchRequest{URL: "http://example.com/x", Format: "text"})
	if err != nil || res.Content != "via-proxy" {
		t.Fatalf("走代理: %+v %v", res, err)
	}
	if len(proxied.dialed) != 1 || proxied.dialed[0] != "127.0.0.1:7897" || !strings.HasPrefix(proxied.hosts[0], "example.com|http://example.com/x") {
		t.Fatalf("应连代理并发绝对地址: %v %v", proxied.dialed, proxied.hosts)
	}
}

func TestFakeIPMixedWithPrivateStillRefused(t *testing.T) {
	policy := web.URLPolicy{Resolver: hostResolver{
		"a.example": {fakeAddr, netip.MustParseAddr("192.168.1.2")},
		"b.example": {netip.MustParseAddr("169.254.169.254")},
		"c.example": {netip.MustParseAddr("127.0.0.1")},
		"d.example": {fakeAddr},
	}}
	for _, host := range []string{"a.example", "b.example", "c.example"} {
		if _, err := policy.Check(t.Context(), "http://"+host+"/"); err == nil {
			t.Fatalf("%s 应被拒", host)
		}
	}
	if _, err := policy.Check(t.Context(), "http://d.example/"); err != nil {
		t.Fatalf("纯假地址应放行: %v", err)
	}
}

// 三项本地额度都不写时不限出网；这是 2026-11-10 起的可选语义（旧代码要求必须显式给）。
func TestWebLocalQuotasAreOptional(t *testing.T) {
	r := hostResolver{"example.com": {publicAddr}}
	h := newFetchHarness(t, r, func(w http.ResponseWriter, req *http.Request) { page(w, "ok") }, nil, true)
	for i := range 3 {
		if _, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/" + string(rune('a'+i)), Format: "text"}); err != nil {
			t.Fatalf("未配置额度应不限: %v", err)
		}
	}
}

// 2026-11-18：订阅把 fake-ip-range6 设成 2001:2::/64 后，域名同时解析出 IPv4、IPv6 两个假地址；两个都要认成假地址，交给代理。
// 假地址里混了私网的，照旧拒。
func TestFetchIPv6FakeIPGoesToProxy(t *testing.T) {
	proxy := func(*http.Request) (*url.URL, error) { return url.Parse("http://127.0.0.1:7897") }
	for name, addrs := range map[string][]netip.Addr{
		"IPv4 加 IPv6 假地址": {fakeAddr, netip.MustParseAddr("2001:2::94")},
		"只有 IPv6 假地址":     {netip.MustParseAddr("2001:2::94")},
		"Clash 默认 ULA 段":  {netip.MustParseAddr("fdfe:dcba:9876::5")},
	} {
		h := newFetchHarness(t, hostResolver{"example.com": addrs}, func(w http.ResponseWriter, req *http.Request) { page(w, "via-proxy") }, proxy)
		res, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/x", Format: "text"})
		if err != nil || res.Content != "via-proxy" {
			t.Fatalf("%s：该走代理取到：%+v %v", name, res, err)
		}
	}
	for name, addrs := range map[string][]netip.Addr{
		"假地址混了私网":   {netip.MustParseAddr("2001:2::94"), netip.MustParseAddr("10.0.0.5")},
		"假地址段外的保留段": {netip.MustParseAddr("2001:2:1::1")},
		"差一位的真 ULA": {netip.MustParseAddr("fdfe:dcba:9877::1")},
	} {
		h := newFetchHarness(t, hostResolver{"example.com": addrs}, func(w http.ResponseWriter, req *http.Request) { page(w, "x") }, proxy)
		if _, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: "http://example.com/x"}); err == nil || !strings.Contains(err.Error(), "WEB_ADDRESS_BLOCKED") {
			t.Fatalf("%s：该拒：%v", name, err)
		}
	}
	// 直接写成 IP 的假地址：不是域名解析来的，一律拒（IPv4、IPv6、IPv4 映射写法）
	h := newFetchHarness(t, hostResolver{}, func(w http.ResponseWriter, req *http.Request) { page(w, "x") }, proxy)
	for _, raw := range []string{"http://198.18.0.5/", "http://[2001:2::5]/", "http://[fdfe:dcba:9876::5]/", "http://[::ffff:198.18.0.5]/"} {
		if _, err := h.service.Fetch(t.Context(), h.access, web.FetchRequest{URL: raw}); err == nil || !strings.Contains(err.Error(), "WEB_ADDRESS_BLOCKED") {
			t.Fatalf("%s：字面量假地址该拒：%v", raw, err)
		}
	}
}
