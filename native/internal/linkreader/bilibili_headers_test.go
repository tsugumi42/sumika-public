package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// B 站所有请求都用完整的桌面浏览器请求头：裸 Mozilla/5.0 会被风控（seasons_archives_list 实测回 -352），
// 只对一个接口换 UA 不够，别的接口同样会被盯上。别的平台的 UA 不变。
func TestBiliRequestsUseBrowserHeaders(t *testing.T) {
	seen := map[string]http.Header{}
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		seen[r.URL.Host+r.URL.Path] = r.Header.Clone()
		return response(200, `{"code":-1}`), nil
	})
	ctx := context.Background()
	for _, raw := range []string{
		"https://api.bilibili.com/x/web-interface/view?bvid=BV1GJ411x7h7",
		"https://api.live.bilibili.com/room/v1/Room/get_info?room_id=1",
		"https://mall.bilibili.com/mall-up-search/items/info",
		"https://api.github.com/repos/a/b",
	} {
		_, _, _, _ = f.request(ctx, raw, "test")
	}
	for host, h := range seen {
		ua := h.Get("User-Agent")
		if strings.Contains(host, "github") {
			if ua != "Mozilla/5.0" {
				t.Errorf("%s 的 UA 不该变：%q", host, ua)
			}
			continue
		}
		if !strings.Contains(ua, "Chrome/") || !strings.Contains(ua, "Windows NT 10.0") || !strings.Contains(ua, "Safari/537.36") {
			t.Errorf("%s UA 不是完整桌面浏览器写法：%q", host, ua)
		}
		if !strings.HasPrefix(h.Get("Accept-Language"), "zh-CN") {
			t.Errorf("%s 缺 Accept-Language：%q", host, h.Get("Accept-Language"))
		}
		if h.Get("Referer") != "https://www.bilibili.com/" {
			t.Errorf("%s Referer=%q", host, h.Get("Referer"))
		}
	}
	// 浏览器从 www.bilibili.com 的页面向 api.bilibili.com 发跨域请求，会带 Origin
	if got := seen["api.bilibili.com/x/web-interface/view"].Get("Origin"); got != "https://www.bilibili.com" {
		t.Errorf("api.bilibili.com 缺 Origin：%q", got)
	}
	if got := seen["api.github.com/repos/a/b"].Get("Origin"); got != "" {
		t.Errorf("别的平台不该带 Origin：%q", got)
	}
}

// 调用方明确指定 UA 就照它的（测试和排查用）；不指定才用浏览器写法。
func TestBiliExplicitUserAgentWins(t *testing.T) {
	var ua string
	f, err := NewFetcher(FetchOptions{UserAgent: "probe/1", Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		ua = r.Header.Get("User-Agent")
		return response(200, `{}`), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = f.request(context.Background(), "https://api.bilibili.com/x/web-interface/view", "test")
	if ua != "probe/1" {
		t.Fatalf("UA=%q", ua)
	}
}
