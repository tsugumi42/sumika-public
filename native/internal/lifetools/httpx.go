package lifetools

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// 日历和天气共用的取数：自己建客户端（不复用 linkreader.Fetcher，它绑了 X、B 站的地址校验），
// 代理照 linkReader.proxy 的规矩（只认 http、不带用户名），空就回退 HTTPS_PROXY、HTTP_PROXY。
// 不跟随跳转，响应体有大小上限，每次请求都有时限。

const holidayBodyMax = 1 << 20

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// clients 按代理地址缓存客户端；Override 非空时一律用它（测试注入，不读环境代理）
type clients struct {
	override *http.Client
	getenv   func(string) string
	mu       sync.Mutex
	byProxy  map[string]*http.Client
}

func newClients(override *http.Client) *clients {
	if override != nil {
		cp := *override // 测试注入的客户端也一样不跟随跳转
		cp.CheckRedirect = noRedirect
		override = &cp
	}
	return &clients{override: override, getenv: os.Getenv, byProxy: map[string]*http.Client{}}
}

// proxyURL 校验代理地址：只认 http、要有主机、不带用户名密码
func proxyURL(raw string) (*url.URL, error) {
	p, err := url.Parse(raw)
	if err != nil || p.Scheme != "http" || p.Host == "" || p.User != nil {
		return nil, errors.New("代理地址不安全或写错了")
	}
	return p, nil
}

func (c *clients) get(proxy string) (*http.Client, error) {
	if c.override != nil {
		return c.override, nil
	}
	if proxy == "" {
		if proxy = c.getenv("HTTPS_PROXY"); proxy == "" {
			proxy = c.getenv("HTTP_PROXY")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.byProxy[proxy]; ok {
		return cl, nil
	}
	tr := &http.Transport{TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 8 * time.Second, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	if proxy != "" {
		p, err := proxyURL(proxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(p)
	}
	cl := &http.Client{Transport: tr, CheckRedirect: noRedirect}
	c.byProxy[proxy] = cl
	return cl, nil
}

// direct 是不走代理的客户端（国内站用）；测试注入了客户端就用它
func (c *clients) direct() (*http.Client, error) {
	if c.override != nil {
		return c.override, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	const key = "direct-route"
	if cl, ok := c.byProxy[key]; ok {
		return cl, nil
	}
	tr := &http.Transport{TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 8 * time.Second, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	cl := &http.Client{Transport: tr, CheckRedirect: noRedirect}
	c.byProxy[key] = cl
	return cl, nil
}

func (c *clients) closeIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cl := range c.byProxy {
		cl.CloseIdleConnections()
	}
}
