package linkreader

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/netsafe"
)

// fake-ip 与代理的测试（docs/规格-链接解析.md 第五节“fake-ip 与代理”）。
// 假 DNS 解析器 + 本机回环上的假代理：假代理只记录收到的 CONNECT 目标，需要时在隧道里回一个写死的 HTTP 响应。

func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	return strings.ReplaceAll(string(b), "\r\n", "\n"), err
}

func defaultFakeRanges(t testing.TB) []netip.Prefix {
	t.Helper()
	ranges, err := netsafe.ParseFakeIPRanges(netsafe.DefaultFakeIPRanges)
	if err != nil {
		t.Fatal(err)
	}
	return ranges
}

var testProxyURL = &url.URL{Scheme: "http", Host: "127.0.0.1:7897"}

// testPolicy 是默认假地址段的规则；withProxy 决定有没有代理（这个代理地址是假的，不会真连）
func testPolicy(withProxy bool) netPolicy {
	ranges, _ := netsafe.ParseFakeIPRanges(netsafe.DefaultFakeIPRanges)
	p := netPolicy{fake: ranges}
	if withProxy {
		p.proxy = testProxyURL
	}
	return p
}

// allowedWithProxy 说：默认假地址段、有代理时，host 解析成 ip 能不能连
func allowedWithProxy(host string, ip netip.Addr) bool {
	_, fe := testPolicy(true).judge(host, "443", []netip.Addr{ip}, "dns")
	return fe == nil
}

func stubLookup(ips ...string) lookupIP {
	addrs := make([]netip.Addr, len(ips))
	for i, s := range ips {
		addrs[i] = netip.MustParseAddr(s)
	}
	return func(context.Context, string) ([]netip.Addr, error) { return addrs, nil }
}

// fakeProxy 是本机回环上的假 HTTP 代理：记下每个 CONNECT 的目标，回 200，然后按 tunnel 回一个 HTTP 响应（nil = 什么都不回，只挂着）
type fakeProxy struct {
	ln      net.Listener
	URL     *url.URL
	mu      sync.Mutex
	targets []string
	conns   []net.Conn
	tunnel  func(authority string, req *http.Request) (status int, location, body string)
	wg      sync.WaitGroup
}

func startFakeProxy(t *testing.T, tunnel func(authority string, req *http.Request) (int, string, string)) *fakeProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProxy{ln: ln, URL: &url.URL{Scheme: "http", Host: ln.Addr().String()}, tunnel: tunnel}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns = append(p.conns, c)
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				p.mu.Lock()
				p.targets = append(p.targets, req.Host)
				p.mu.Unlock()
				io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				if p.tunnel == nil {
					// 挂着，等对端关
					io.Copy(io.Discard, br)
					return
				}
				for {
					inner, err := http.ReadRequest(br)
					if err != nil {
						return
					}
					status, location, body := p.tunnel(req.Host, inner)
					resp := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
					if location != "" {
						resp.Header.Set("Location", location)
					}
					if resp.Write(c) != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}

func (p *fakeProxy) Targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

func dialReason(t *testing.T, err error) (kind, reason string) {
	t.Helper()
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("want FetchError, got %v", err)
	}
	return fe.Kind, fe.Reason
}

// 名单内主机 → fake-ip → 有代理：不连假地址，CONNECT 的是主机名
func TestFakeIPListedHostGoesByNameThroughProxy(t *testing.T) {
	for _, viaOrdinaryProxy := range []bool{false, true} {
		proxy := startFakeProxy(t, nil)
		policy := netPolicy{fake: defaultFakeRanges(t), proxy: proxy.URL}
		var ordinary *url.URL // 这条线路平时的出口：直连（nil）或同一个代理；fake-ip 回退都不看它，只用 policy.proxy
		if viaOrdinaryProxy {
			ordinary = proxy.URL
		}
		for _, c := range []struct{ address, want string }{
			{"api.fxtwitter.com:443", "api.fxtwitter.com:443"},
			{"API.FXTwitter.com:443", "api.fxtwitter.com:443"},  // 大小写归一
			{"api.fxtwitter.com.:443", "api.fxtwitter.com:443"}, // 结尾的点
			{"i7.hdslb.com:443", "i7.hdslb.com:443"},            // 下载许可名单的后缀项（hdslb.com）
			{"p16-common-sign.tiktokcdn-us.com:443", "p16-common-sign.tiktokcdn-us.com:443"},
			{"api.bilibili.com:80", "api.bilibili.com:80"},
		} {
			conn, err := safeDial(ordinary, stubLookup("198.18.0.5"), policy)(context.Background(), "tcp", c.address)
			if err != nil {
				t.Fatalf("%s: %v", c.address, err)
			}
			conn.Close()
			got := proxy.Targets()
			if last := got[len(got)-1]; last != c.want {
				t.Fatalf("%s: CONNECT 目标 %q，想要 %q", c.address, last, c.want)
			}
		}
		for _, target := range proxy.Targets() {
			if strings.HasPrefix(target, "198.") {
				t.Fatalf("CONNECT 带了假地址：%s", target)
			}
		}
	}
}

// 名单内主机 → fake-ip → 没有代理：拒绝，原因写明
func TestFakeIPListedHostWithoutProxyRefused(t *testing.T) {
	conn, err := safeDial(nil, stubLookup("198.18.0.5"), testPolicy(false))(context.Background(), "tcp", "api.fxtwitter.com:443")
	if conn != nil {
		conn.Close()
		t.Fatal("没有代理却连上了假地址")
	}
	kind, reason := dialReason(t, err)
	if kind != "unsafe_target" || reason != ReasonFakeIPNoProxy {
		t.Fatalf("kind=%s reason=%s", kind, reason)
	}
	var fe *FetchError
	errors.As(err, &fe)
	if !strings.Contains(fe.Detail, "没有代理可走") {
		t.Fatalf("日志原因不明确：%q", fe.Detail)
	}
}

// 名单外的主机 → fake-ip：拒绝，哪怕有代理；假代理一个连接都不该收到
func TestFakeIPUnlistedHostRefused(t *testing.T) {
	proxy := startFakeProxy(t, nil)
	policy := netPolicy{fake: defaultFakeRanges(t), proxy: proxy.URL}
	for _, host := range []string{
		"example.com", "internal.corp.example", "api.fxtwitter.com.evil.example", "evil-api.fxtwitter.com", "hdslb.com.evil.example",
		"i0.hdslb.com.evil.example", "evilhdslb.com", "p16-common-sign.tiktokcdn-us.com.evil.example", "localhost",
		"198.18.0.9", "10.0.0.1", "", "a_b.example",
	} {
		conn, err := safeDial(nil, stubLookup("198.18.0.5"), policy)(context.Background(), "tcp", net.JoinHostPort(host, "443"))
		if conn != nil {
			conn.Close()
			t.Fatalf("名单外主机 %q 连上了", host)
		}
		kind, reason := dialReason(t, err)
		if kind != "unsafe_target" || reason != ReasonFakeIPNotListed {
			t.Fatalf("%q: kind=%s reason=%s", host, kind, reason)
		}
	}
	if got := proxy.Targets(); len(got) != 0 {
		t.Fatalf("名单外主机的请求到了代理：%v", got)
	}
}

// 名单内主机 → 真私有地址：照旧一律拒绝，和代理有没有无关，原因不是 fake-ip 的两种
func TestListedHostRealPrivateAddressRefused(t *testing.T) {
	proxy := startFakeProxy(t, nil)
	for _, withProxy := range []bool{false, true} {
		policy := netPolicy{fake: defaultFakeRanges(t)}
		if withProxy {
			policy.proxy = proxy.URL
		}
		for _, ip := range []string{"10.0.0.1", "127.0.0.1", "169.254.169.254", "::1", "fe80::1", "fc00::1", "192.168.1.1", "172.16.0.1", "100.64.0.1", "0.0.0.0", "224.0.0.1", "::ffff:10.0.0.1", "::ffff:127.0.0.1", "192.0.2.1", "198.51.100.1"} {
			conn, err := safeDial(nil, stubLookup(ip), policy)(context.Background(), "tcp", "api.fxtwitter.com:443")
			if conn != nil {
				conn.Close()
				t.Fatalf("真私有地址 %s 连上了", ip)
			}
			kind, reason := dialReason(t, err)
			if kind != "unsafe_target" || reason != "" {
				t.Fatalf("%s: kind=%s reason=%q", ip, kind, reason)
			}
		}
	}
	if got := proxy.Targets(); len(got) != 0 {
		t.Fatalf("私有地址的请求到了代理：%v", got)
	}
}

// 混合 A 记录和 IPv6 映射写法
func TestFakeIPMixedAndMappedAnswers(t *testing.T) {
	proxy := startFakeProxy(t, nil)
	policy := netPolicy{fake: defaultFakeRanges(t), proxy: proxy.URL}
	refused := [][]string{
		{"198.18.0.4", "10.0.0.1"},            // fake + 私网
		{"10.0.0.1", "198.18.0.4"},            // 顺序反过来
		{"198.18.0.4", "::ffff:127.0.0.1"},    // fake + 映射的回环
		{"198.18.0.4", "169.254.169.254"},     // fake + 链路本地（云元数据）
		{"1.1.1.1", "198.18.0.4", "10.1.1.1"}, // 公网 + fake + 私网
		{"::ffff:198.18.0.4", "fc00::1"},      // 映射 fake + IPv6 私网
	}
	for _, ips := range refused {
		conn, err := safeDial(nil, stubLookup(ips...), policy)(context.Background(), "tcp", "api.fxtwitter.com:443")
		if conn != nil {
			conn.Close()
			t.Fatalf("混合答案 %v 连上了", ips)
		}
		if kind, reason := dialReason(t, err); kind != "unsafe_target" || reason != "" {
			t.Fatalf("%v: kind=%s reason=%q", ips, kind, reason)
		}
	}
	if got := proxy.Targets(); len(got) != 0 {
		t.Fatalf("混合答案到了代理：%v", got)
	}
	allowed := [][]string{{"::ffff:198.18.0.4"}, {"1.1.1.1", "198.18.0.4"}, {"198.18.0.4", "198.19.255.255"}}
	for _, ips := range allowed {
		conn, err := safeDial(nil, stubLookup(ips...), policy)(context.Background(), "tcp", "api.fxtwitter.com:443")
		if err != nil {
			t.Fatalf("%v: %v", ips, err)
		}
		conn.Close()
	}
	// 映射的 fake 地址对名单外主机同样拒绝
	conn, err := safeDial(nil, stubLookup("::ffff:198.18.0.4"), policy)(context.Background(), "tcp", "example.com:443")
	if conn != nil {
		conn.Close()
		t.Fatal("映射 fake 地址对名单外主机放行了")
	}
	if _, reason := dialReason(t, err); reason != ReasonFakeIPNotListed {
		t.Fatalf("reason=%s", reason)
	}
}

// 段是配置项：改成别的范围就按新范围判；旧的默认段不再算 fake（回到普通保留段，一律拒绝）
func TestFakeIPRangesAreConfigurable(t *testing.T) {
	proxy := startFakeProxy(t, nil)
	ranges, err := netsafe.ParseFakeIPRanges([]string{"28.99.0.1/16", "fdfe:dcba:9876::/48"})
	if err != nil {
		t.Fatal(err)
	}
	policy := netPolicy{fake: ranges, proxy: proxy.URL}
	for _, ip := range []string{"28.99.3.4", "fdfe:dcba:9876::7"} {
		conn, err := safeDial(nil, stubLookup(ip), policy)(context.Background(), "tcp", "api.fxtwitter.com:443")
		if err != nil {
			t.Fatalf("%s: %v", ip, err)
		}
		conn.Close()
	}
	if got := proxy.Targets(); len(got) != 2 || got[0] != "api.fxtwitter.com:443" {
		t.Fatalf("targets=%v", got)
	}
	for _, c := range []struct{ host, ip string }{{"api.fxtwitter.com", "198.18.0.5"}, {"api.fxtwitter.com", "198.51.100.1"}, {"example.com", "28.99.3.4"}} {
		conn, err := safeDial(nil, stubLookup(c.ip), policy)(context.Background(), "tcp", c.host+":443")
		if conn != nil {
			conn.Close()
			t.Fatalf("%v 连上了", c)
		}
		kind, reason := dialReason(t, err)
		wantReason := ""
		if c.host == "example.com" {
			wantReason = ReasonFakeIPNotListed
		}
		if kind != "unsafe_target" || reason != wantReason {
			t.Fatalf("%v: kind=%s reason=%q", c, kind, reason)
		}
	}
	// 空段表 = 没有假地址段：198.18 也只是普通保留段
	none := netPolicy{proxy: proxy.URL}
	if conn, err := safeDial(nil, stubLookup("198.18.0.5"), none)(context.Background(), "tcp", "api.fxtwitter.com:443"); err == nil {
		conn.Close()
		t.Fatal("空段表仍放行 fake-ip")
	} else if _, reason := dialReason(t, err); reason != "" {
		t.Fatalf("空段表 reason=%q", reason)
	}
}

// 形状：端口、IP 字面量、超长或带控制字符的主机名、空主机
func TestFakeIPDialShapes(t *testing.T) {
	proxy := startFakeProxy(t, nil)
	policy := netPolicy{fake: defaultFakeRanges(t), proxy: proxy.URL}
	refuse := func(t *testing.T, lookup lookupIP, address string) {
		t.Helper()
		before := len(proxy.Targets())
		conn, err := safeDial(nil, lookup, policy)(context.Background(), "tcp", address)
		if conn != nil {
			conn.Close()
			t.Fatalf("%s 连上了", address)
		}
		if kind, _ := dialReason(t, err); kind != "unsafe_target" {
			t.Fatalf("%s: kind=%s", address, kind)
		}
		if len(proxy.Targets()) != before {
			t.Fatalf("%s 的请求到了代理", address)
		}
	}
	fake := stubLookup("198.18.0.5")
	t.Run("port", func(t *testing.T) {
		refuse(t, fake, "api.fxtwitter.com:8443")
		refuse(t, fake, "api.fxtwitter.com:22")
		refuse(t, fake, "api.fxtwitter.com:0")
	})
	t.Run("empty_host", func(t *testing.T) { refuse(t, fake, ":443") })
	t.Run("no_port", func(t *testing.T) { refuse(t, fake, "api.fxtwitter.com") })
	t.Run("long_host", func(t *testing.T) {
		refuse(t, fake, strings.Repeat("a", 250)+".hdslb.com:443")
		refuse(t, fake, strings.Repeat("a.", 200)+"hdslb.com:443")
	})
	t.Run("control_chars", func(t *testing.T) {
		refuse(t, fake, "x\r\nProxy-Authorization: y.hdslb.com:443")
		refuse(t, fake, "x y.hdslb.com:443")
		refuse(t, fake, "a\x00.hdslb.com:443")
	})
	t.Run("ip_literals", func(t *testing.T) {
		literal := func(_ context.Context, host string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(host)}, nil
		}
		refuse(t, literal, "198.18.0.9:443") // fake-ip 字面量
		refuse(t, literal, "[::ffff:198.18.0.9]:443")
		refuse(t, literal, "127.0.0.1:443")
		refuse(t, literal, "169.254.169.254:80")
		refuse(t, literal, "[::1]:443")
	})
	t.Run("public_literal_unchanged", func(t *testing.T) {
		// 公网 IP 字面量不涉及 fake-ip，行为照旧：绑定这个地址，经平常的出口连
		literal := func(_ context.Context, host string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(host)}, nil
		}
		conn, err := safeDial(proxy.URL, literal, policy)(context.Background(), "tcp", "1.2.3.4:443")
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if got := proxy.Targets(); got[len(got)-1] != "1.2.3.4:443" {
			t.Fatalf("targets=%v", got)
		}
	})
	t.Run("public_nondefault_port_unchanged", func(t *testing.T) {
		conn, err := safeDial(proxy.URL, stubLookup("1.1.1.1"), policy)(context.Background(), "tcp", "example.com:8443")
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if got := proxy.Targets(); got[len(got)-1] != "1.1.1.1:8443" {
			t.Fatalf("targets=%v", got)
		}
	})
}

// 每一跳重新判断（拨号层）：net/http 自己跟随跳转，每一跳都是新的拨号。第一跳是名单内主机、走代理按主机名；
// 跳到名单外的 fake-ip 主机，第二跳的拨号被拒，代理只收到第一跳。
func TestFakeIPEveryRedirectHopRechecked(t *testing.T) {
	proxy := startFakeProxy(t, func(authority string, req *http.Request) (int, string, string) {
		switch authority {
		case "api.fxtwitter.com:80":
			return 302, "http://" + strings.TrimSpace(req.Header.Get("X-Next")) + "/next", ""
		case "pbs.twimg.com:80":
			return 200, "", "图"
		}
		return 500, "", "不该到这里：" + authority
	})
	policy := netPolicy{fake: defaultFakeRanges(t), proxy: proxy.URL}
	client := func() *http.Client {
		return &http.Client{Transport: &http.Transport{DialContext: safeDial(nil, stubLookup("198.18.0.5"), policy), DisableKeepAlives: true}, Timeout: 5 * time.Second}
	}
	get := func(next string) (*http.Response, error) {
		req, _ := http.NewRequest("GET", "http://api.fxtwitter.com/start", nil)
		req.Header.Set("X-Next", next)
		return client().Do(req)
	}
	// 跳到名单内的另一个主机：放行，经代理按它的主机名连
	resp, err := get("pbs.twimg.com")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "图" {
		t.Fatalf("body=%q", body)
	}
	if got := proxy.Targets(); len(got) != 2 || got[1] != "pbs.twimg.com:80" {
		t.Fatalf("targets=%v", got)
	}
	// 跳到名单外的 fake-ip 主机：第二跳拒绝
	before := len(proxy.Targets())
	for _, next := range []string{"internal.corp.example", "198.18.0.77", "api.fxtwitter.com.evil.example", "localhost"} {
		resp, err := get(next)
		if resp != nil {
			resp.Body.Close()
			t.Fatalf("跳到 %s 竟然成功了：%d", next, resp.StatusCode)
		}
		if _, reason := dialReason(t, err); reason != ReasonFakeIPNotListed {
			t.Fatalf("跳到 %s：%v", next, err)
		}
	}
	// 每个 get 的第一跳（名单内）各发一次 CONNECT，名单外的第二跳一次都没有
	if got := proxy.Targets(); len(got) != before+4 {
		t.Fatalf("targets=%v", got)
	}
	for _, target := range proxy.Targets()[before:] {
		if target != "api.fxtwitter.com:80" {
			t.Fatalf("名单外主机到了代理：%s", target)
		}
	}
}

// 整条链：NewFetcher 的接线（国内直连线路 + fake-ip 回退代理）、Stream 的逐跳 mediaURL 检查、经代理按主机名取回内容
func TestFetcherFakeIPEndToEnd(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	proxy := startFakeProxy(t, func(authority string, req *http.Request) (int, string, string) {
		switch authority + req.URL.Path {
		case "i0.hdslb.com:80/a.png":
			return 302, "http://i1.hdslb.com/b.png", ""
		case "i1.hdslb.com:80/b.png":
			return 200, "", "二号图"
		case "i2.hdslb.com:80/c.png":
			return 302, "http://evil.example/x.png", ""
		case "api.bilibili.com:80/ok":
			return 200, "", `{"code":0}`
		}
		return 500, "", "不该到这里：" + authority + req.URL.Path
	})
	fakeDNS := stubLookup("198.18.7.7")
	newFetcher := func(proxyURL string) *Fetcher {
		f, err := NewFetcher(FetchOptions{Proxy: proxyURL, directLookup: fakeDNS})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// 配了代理：国内主机（直连线路）碰到假地址，经代理按主机名连
	f := newFetcher(proxy.URL.String())
	body, status, _, err := f.request(context.Background(), "http://api.bilibili.com/ok", "test")
	if err != nil || status != 200 || string(body) != `{"code":0}` {
		t.Fatalf("取数 %s %d %v", body, status, err)
	}
	var got strings.Builder
	if err := f.Stream(context.Background(), "bilibili", "http://i0.hdslb.com/a.png", &got, &ByteBudget{Limit: 64}); err != nil || got.String() != "二号图" {
		t.Fatalf("Stream 跳转：%q %v", got.String(), err)
	}
	// 跳到名单外：mediaURL 逐跳检查在拨号之前就拒了；代理没收到 evil.example
	err = f.Stream(context.Background(), "bilibili", "http://i2.hdslb.com/c.png", io.Discard, &ByteBudget{Limit: 64})
	wantError(t, err, "unsafe_target")
	for _, target := range proxy.Targets() {
		if strings.Contains(target, "evil") || strings.HasPrefix(target, "198.") {
			t.Fatalf("不该到代理的目标：%s", target)
		}
	}
	// 国内线路的出口仍是直连：fake-ip 回退才用代理，真公网地址不经代理（TestPlatformRouting 覆盖）

	// 没配代理：同样的请求被拒，原因写明；代理一个新连接都没有
	before := len(proxy.Targets())
	g := newFetcher("")
	_, _, _, err = g.request(context.Background(), "http://api.bilibili.com/ok", "test")
	if kind, reason := dialReason(t, err); kind != "unsafe_target" || reason != ReasonFakeIPNoProxy {
		t.Fatalf("无代理：kind=%s reason=%s", kind, reason)
	}
	err = g.Stream(context.Background(), "bilibili", "http://i0.hdslb.com/a.png", io.Discard, &ByteBudget{Limit: 64})
	if kind, reason := dialReason(t, err); kind != "unsafe_target" || reason != ReasonFakeIPNoProxy {
		t.Fatalf("无代理 Stream：kind=%s reason=%s", kind, reason)
	}
	// 非国内主机在没配代理时走系统 DNS（这里是假 DNS），同样拒绝
	_, _, _, err = g.request(context.Background(), "http://api.fxtwitter.com/x", "test")
	if _, reason := dialReason(t, err); reason != ReasonFakeIPNoProxy {
		t.Fatalf("非国内主机无代理：%v", err)
	}
	if len(proxy.Targets()) != before {
		t.Fatalf("无代理时有请求到了代理：%v", proxy.Targets()[before:])
	}
}

// 配置接线：默认段、显式空表、非法段
func TestNewFetcherFakeIPRanges(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	f, err := NewFetcher(FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !netsafe.InFakeRanges(f.policy.fake, netip.MustParseAddr("198.19.1.1")) || f.policy.proxy != nil {
		t.Fatalf("默认：%+v", f.policy)
	}
	f, err = NewFetcher(FetchOptions{FakeIPRanges: []string{}})
	if err != nil || len(f.policy.fake) != 0 {
		t.Fatalf("显式空表：%+v %v", f.policy, err)
	}
	f, err = NewFetcher(FetchOptions{FakeIPRanges: []string{"28.99.0.0/16"}, Proxy: "http://127.0.0.1:7897"})
	if err != nil || !netsafe.InFakeRanges(f.policy.fake, netip.MustParseAddr("28.99.1.1")) || netsafe.InFakeRanges(f.policy.fake, netip.MustParseAddr("198.18.0.1")) || f.policy.proxy == nil {
		t.Fatalf("自定义：%+v %v", f.policy, err)
	}
	for _, bad := range [][]string{{"nope"}, {"0.0.0.0/0"}, {"198.18.0.0/7"}, {""}, {"::/0"}, {"::ffff:198.18.0.0/111"}, {"127.0.0.0/8"}, {"198.18.0.0/15", "10.0.0.0/8"}, {"fd00::/16"}} {
		if _, err := NewFetcher(FetchOptions{FakeIPRanges: bad}); err == nil {
			t.Fatalf("%v 应被拒绝", bad)
		} else {
			wantError(t, err, "invalid_options")
		}
	}
	// 环境变量里的代理也算“配置的代理”
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7897")
	f, err = NewFetcher(FetchOptions{})
	if err != nil || f.policy.proxy == nil {
		t.Fatalf("环境代理：%+v %v", f.policy, err)
	}
}

// trustedHost 的名单：精确表、下载许可名单的后缀项都算；伪后缀、IP、空不算
func TestTrustedHost(t *testing.T) {
	for host, want := range map[string]bool{
		"api.bilibili.com": true, "B23.TV": true, "i0.hdslb.com": true, "anything.hdslb.com": true, "p16-common-sign.tiktokcdn-us.com": true,
		"cdn3.cdn-telegram.org": true, "cdn9.telesco.pe": true, "rr1---sn-x.googlevideo.com": true, "xyz.bilivideo.com": true,
		"api.fxtwitter.com": true, "api.fxtwitter.com.": true, "pbs.twimg.com": true,
		"hdslb.com.evil.example": false, "evilhdslb.com": false, "x.tiktokcdn-us.com.evil": false, "pbs.twimg.com.evil.example": false,
		"twimg.com": false, "other.twimg.com": false, "example.com": false, "198.18.0.1": false, "": false, "image-cdn-zz.spotifycdn.com": false,
		// 以前写死放行的两个 TikTok 主机现在由后缀项覆盖，不再在精确表里
		"p19-common-sign.tiktokcdn-us.com": true,
	} {
		if got := trustedHost(host); got != want {
			t.Errorf("trustedHost(%q)=%v want %v", host, got, want)
		}
	}
	if fetchHosts["p16-common-sign.tiktokcdn-us.com"] || fetchHosts["p19-common-sign.tiktokcdn-us.com"] {
		t.Fatal("两个 TikTok 主机不该再写死在精确表里")
	}
}

// mediaPlatforms 要和 mediaHost 认识的平台同步：从源码里数 case 项
func TestMediaPlatformsListed(t *testing.T) {
	src, err := readSource("stream.go")
	if err != nil {
		t.Fatal(err)
	}
	body := src[strings.Index(src, "func mediaHost("):]
	body = body[:strings.Index(body, "\n}\n")]
	listed := map[string]bool{}
	for _, p := range mediaPlatforms {
		listed[p] = true
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "case \"") || !strings.HasSuffix(line, ":") {
			continue
		}
		name := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(line, "case "), ":"), `"`)
		if !listed[name] {
			t.Errorf("mediaHost 认识平台 %q，mediaPlatforms 里没有", name)
		}
		delete(listed, name)
	}
	for p := range listed {
		t.Errorf("mediaPlatforms 里的 %q 不在 mediaHost", p)
	}
}

// 假地址段配错成真私有地址段：解析时拒绝；绕开解析、直接拼出来的规则裁决时也不认，真私有地址照旧整次拒绝，也不会送到代理
func TestFakeRangesCoveringRealPrivateAreIgnored(t *testing.T) {
	proxy := startFakeProxy(t, nil)
	for _, c := range []struct{ fake, ip string }{
		{"127.0.0.0/8", "127.0.0.1"}, {"10.0.0.0/8", "10.1.2.3"}, {"192.168.0.0/16", "192.168.1.1"}, {"172.16.0.0/12", "172.20.0.1"},
		{"169.254.0.0/16", "169.254.169.254"}, {"100.64.0.0/10", "100.100.0.1"}, {"0.0.0.0/8", "0.1.2.3"}, {"224.0.0.0/8", "224.0.0.1"},
		{"::/16", "::1"}, {"fe80::/16", "fe80::1"}, {"ff02::/64", "ff02::1"}, {"fe80::/64", "fe80::1"}, {"::1/128", "::1"}, {"fd00::/16", "fd12:3456::1"}, {"ff00::/16", "ff02::1"},
		{"10.0.0.0/8", "::ffff:10.0.0.1"},
	} {
		policy := netPolicy{fake: []netip.Prefix{netip.MustParsePrefix(c.fake)}, proxy: proxy.URL}
		for _, host := range []string{"api.fxtwitter.com", "example.com"} {
			conn, err := safeDial(nil, stubLookup(c.ip), policy)(context.Background(), "tcp", host+":443")
			if conn != nil {
				conn.Close()
				t.Fatalf("假地址段 %s 盖住的真私有地址 %s（%s）连上了", c.fake, c.ip, host)
			}
			if kind, reason := dialReason(t, err); kind != "unsafe_target" || reason != "" {
				t.Fatalf("%s %s %s: kind=%s reason=%q", c.fake, c.ip, host, kind, reason)
			}
		}
		// 混着真公网地址也一样
		if _, fe := policy.judge("api.fxtwitter.com", "443", []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr(c.ip)}, "dns"); fe == nil || fe.Reason != "" {
			t.Fatalf("%s 混公网答案 %s：%+v", c.fake, c.ip, fe)
		}
	}
	if got := proxy.Targets(); len(got) != 0 {
		t.Fatalf("私有地址的请求到了代理：%v", got)
	}
	// 解析过的合法段（fc00::/8 里的 sing-box 默认段、fd 里写到 /48 的随机前缀）不受影响
	ranges, err := netsafe.ParseFakeIPRanges([]string{"fc00::/18", "fdfe:dcba:9876::/48"})
	if err != nil {
		t.Fatal(err)
	}
	policy := netPolicy{fake: ranges, proxy: proxy.URL}
	for _, ip := range []string{"fc00::7", "fdfe:dcba:9876::7"} {
		if _, fe := policy.judge("api.fxtwitter.com", "443", []netip.Addr{netip.MustParseAddr(ip)}, "dns"); fe != nil {
			t.Fatalf("%s: %+v", ip, fe)
		}
	}
	if _, fe := policy.judge("api.fxtwitter.com", "443", []netip.Addr{netip.MustParseAddr("fd12::1")}, "dns"); fe == nil {
		t.Fatal("段外的 fd 地址该拒绝")
	}
}
