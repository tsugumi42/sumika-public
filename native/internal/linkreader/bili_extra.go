package linkreader

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/linktemplate"
)

// 个别 B 站接口要求特定的 Referer（小店商品要 gf.bilibili.com）或访客标识 Cookie（buvid3、buvid4）。
// 调用方把头放进 ctx，send 在写完各平台默认头之后覆盖上去；只在这一次请求有效，不进入全局状态。
type headerKey struct{}

// browserUA 是 B 站请求默认的 User-Agent：完整的桌面 Chrome 写法，版本固定不随机，隔一两个季度跟着稳定版往上改。
// 裸的 Mozilla/5.0 会被风控盯上（seasons_archives_list 实测回 -352，调研见 docs/notes/调研-B站412风控-2026-11-13.md）；
// 也不假冒 App 的 UA。调用方在 FetchOptions.UserAgent 里明确写了就用它的。
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

// setBiliHeaders 把 B 站请求补成浏览器自己会发的头：UA、Accept-Language，调用 api.* 时带上页面的 Origin。
// Referer 由 send 先写好（https://www.bilibili.com/）。只写浏览器本来就发的头，不写 Sec-Fetch、设备指纹这类假冒前端的东西。
func setBiliHeaders(req *http.Request, ua string) {
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if strings.HasPrefix(req.URL.Hostname(), "api.") {
		req.Header.Set("Origin", "https://www.bilibili.com")
	}
}

func withHeader(ctx context.Context, key, value string) context.Context {
	merged := http.Header{}
	for k, v := range ctxHeaders(ctx) {
		merged[k] = v
	}
	merged.Set(key, value)
	return context.WithValue(ctx, headerKey{}, merged)
}

func ctxHeaders(ctx context.Context) http.Header {
	h, _ := ctx.Value(headerKey{}).(http.Header)
	return h
}

// biliFetchable 说短链跳到的页面有没有取数分支（查 linktemplate 的清单；清单和取数分发的一致由 linkexpand 的测试对账）。
func biliFetchable(l Link) bool {
	if l.Platform != "bilibili" {
		return false
	}
	k, ok := linktemplate.LookupKind(l.Platform, l.Kind)
	return ok && k.Support != linktemplate.SupportNone
}

// 匿名访客标识：x/frontend/finger/spi 不需要登录，浏览器打开 B 站主页也会拿一份（buvid3、buvid4）。
// 第一次请求 B 站 API 时取一次，缓存六小时，之后带在所有发往 B 站 API 主机（api. 开头）的请求上；
// 只用服务端发的这一份，不本地伪造，也不碰任何登录态。取不到不挡主请求，5 分钟内不再试。
type buvidCache struct {
	sync.Mutex
	Cookie string
	Until  time.Time // Cookie 有效到
	Retry  time.Time // 上次失败后，这个时刻之前不再取
}

// noVisitorCookie 只供测试：测试包的 init 里置真，省得几十个假服务器都要认 finger/spi 这个多出来的请求；
// 取访客标识的测试自己置回 false（见 bilibili_visitor_test.go）。生产代码不会改它。
var noVisitorCookie bool

const (
	biliSPISource = "bili_spi"
	buvidTTL      = 6 * time.Hour
	buvidRetry    = 5 * time.Minute
)

// biliAPIHost 是带访客标识的主机：B 站的 API（api.bilibili.com、api.live.bilibili.com 等）。
// 页面、小店、短链、图床和别的平台不带。
func biliAPIHost(host string) bool {
	host = strings.ToLower(host)
	return strings.HasPrefix(host, "api.") && hostSuffix(host, "bilibili.com")
}

// visitorCookie 返回要带的访客标识 Cookie；取不到返回空串（原因不外传，请求照常发）。
func (f *Fetcher) visitorCookie(ctx context.Context) string {
	f.buvid.Lock()
	defer f.buvid.Unlock()
	now := f.rate.options.Now()
	if f.buvid.Cookie != "" && now.Before(f.buvid.Until) {
		return f.buvid.Cookie
	}
	if now.Before(f.buvid.Retry) {
		return ""
	}
	cookie, err := f.fetchVisitorCookie(ctx)
	if err != nil {
		var fe *FetchError
		// 我们自己的冷却、预算拒绝不算对端失败，不记“5 分钟不再取”
		if !errors.As(err, &fe) || fe.Kind != "rate_limited" {
			f.buvid.Retry = now.Add(buvidRetry)
		}
		return ""
	}
	f.buvid.Cookie, f.buvid.Until = cookie, now.Add(buvidTTL)
	return cookie
}

func (f *Fetcher) fetchVisitorCookie(ctx context.Context) (string, error) {
	var e struct {
		Code *int `json:"code"`
		Data *struct {
			B3 string `json:"b_3"`
			B4 string `json:"b_4"`
		} `json:"data"`
	}
	if err := f.getJSON(ctx, "https://api.bilibili.com/x/frontend/finger/spi", biliSPISource, &e); err != nil {
		return "", err
	}
	if e.Code == nil || *e.Code != 0 || e.Data == nil || e.Data.B3 == "" || e.Data.B4 == "" || strings.ContainsAny(e.Data.B3+e.Data.B4, "; \r\n") {
		return "", failure("invalid_response", biliSPISource)
	}
	return "buvid3=" + e.Data.B3 + "; buvid4=" + e.Data.B4, nil
}

// UpowerLabel 是充电专属视频的标注：只给能确认的（接口的 is_upower_exclusive 为真），有试看时一并写出；不是充电专属返回空串。
// 没充电能看到的是公开部分（标题、简介、统计和试看），这里不替人判断“能不能看”。
func (r *Result) UpowerLabel() string {
	flagTrue := func(key string) bool {
		f, ok := r.Flags[key]
		b, isBool := f.Value.(bool)
		return ok && f.State == "available" && isBool && b
	}
	switch {
	case !flagTrue("is_upower_exclusive"):
		return ""
	case flagTrue("is_upower_preview"):
		return "充电专属（有试看）"
	}
	return "充电专属"
}
