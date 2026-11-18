package linkreader

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// dohCacheTTL 是代理解析结果的缓存时长：同一个主机在这段时间内不再查 DoH。
// 经代理的每次 DoH 往返要几秒，一次取数里同一个主机会解析好几次（取数连接、封面检查），不缓存会把取数时限吃掉。
const dohCacheTTL = 5 * time.Minute

type dohEntry struct {
	ips []netip.Addr
	exp time.Time
}

// 代理环境可能使用假 IP；解析走 TLS 校验的公共 DoH，连接仍绑定数字 IP。
// A 和 AAAA 同时查（一轮往返而不是两轮），结果按主机缓存；DoH 是公共解析服务，不套目标站点的请求间隔。
// dohTransport 非空时替代“经代理连 1.1.1.1”（只供测试注入）。
func (f *Fetcher) proxyLookup(proxy *url.URL, timeout time.Duration, dohTransport http.RoundTripper) lookupIP {
	var rt http.RoundTripper = dohTransport
	if rt == nil {
		rt = newLinkTransport(func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialPinned(ctx, network, "1.1.1.1:443", proxy)
		}, timeout)
	}
	client := &http.Client{Transport: rt, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	const dohHost = "cloudflare-dns.com"
	var mu sync.Mutex
	cache := map[string]dohEntry{}
	query := func(ctx context.Context, host, kind string) ([]netip.Addr, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", "https://"+dohHost+"/dns-query?name="+url.QueryEscape(host)+"&type="+kind, nil)
		if err != nil {
			return nil, failure("network", "dns")
		}
		req.Header.Set("Accept", "application/dns-json")
		req.Header.Set("User-Agent", f.userAgent)
		f.requests.Add(1)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		resp.Body.Close()
		if err = httpFailure(resp.StatusCode, resp.Header, "dns"); err != nil {
			return nil, err
		}
		if readErr != nil || len(b) > 65536 {
			return nil, failure("invalid_response", "dns")
		}
		var answer struct {
			Status *int `json:"Status"`
			Answer []struct {
				Type int    `json:"type"`
				Data string `json:"data"`
			} `json:"Answer"`
		}
		if json.Unmarshal(b, &answer) != nil || answer.Status == nil || *answer.Status != 0 {
			return nil, failure("network", "dns")
		}
		var ips []netip.Addr
		for _, a := range answer.Answer {
			if a.Type == 1 || a.Type == 28 {
				ip, err := netip.ParseAddr(a.Data)
				if err != nil {
					return nil, failure("invalid_response", "dns")
				}
				ips = append(ips, ip)
			}
		}
		return ips, nil
	}
	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		if ip, err := netip.ParseAddr(host); err == nil {
			return []netip.Addr{ip}, nil
		}
		key := strings.ToLower(host)
		mu.Lock()
		if e, ok := cache[key]; ok && time.Now().Before(e.exp) {
			mu.Unlock()
			return append([]netip.Addr(nil), e.ips...), nil
		}
		mu.Unlock()
		kinds := []string{"A", "AAAA"}
		got := make([][]netip.Addr, len(kinds))
		errs := make([]error, len(kinds))
		var wg sync.WaitGroup
		for i, kind := range kinds {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got[i], errs[i] = query(ctx, host, kind)
			}()
		}
		wg.Wait()
		var ips []netip.Addr
		for i := range kinds {
			if errs[i] != nil {
				return nil, errs[i]
			}
			ips = append(ips, got[i]...)
		}
		if len(ips) > 0 {
			mu.Lock()
			cache[key] = dohEntry{ips: append([]netip.Addr(nil), ips...), exp: time.Now().Add(dohCacheTTL)}
			mu.Unlock()
		}
		return ips, nil
	}
}
