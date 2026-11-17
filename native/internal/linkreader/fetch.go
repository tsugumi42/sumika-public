package linkreader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sumika-native/internal/netsafe"
)

type FetchOptions struct {
	// Transport 注入离线回放；生产默认使用绑定已校验 IP 的传输。
	Transport   http.RoundTripper
	Proxy       string
	Timeout     time.Duration
	MaxBody     int64
	UserAgent   string
	MinInterval time.Duration
	Rate        RateOptions
	// DoHTransport 只供测试：替代经代理连公共 DoH 的传输（需要同时给 Proxy）。
	DoHTransport http.RoundTripper
	// FXTimeout 是 X 的 fx_v2 线路主请求的单独时限（linkReader.x.fxTimeoutSeconds）；0 用 4 秒
	FXTimeout time.Duration
	// NoSyndication 关掉 X 的备用路线 syndication（linkReader.x.syndication: false）
	NoSyndication bool
	// FakeIPRanges 是代理 fake-ip 模式的假地址段（linkReader.fakeIPRanges，CIDR 文字）：解析到这些段 = 真实地址未知。
	// nil 用默认（netsafe.DefaultFakeIPRanges）；非 nil 的空列表 = 没有假地址段，保留段地址一律拒绝
	FakeIPRanges []string
	// directLookup 只供测试：替代系统 DNS（国内直连线路和没配代理时用它）
	directLookup lookupIP
}
type Fetcher struct {
	client          *http.Client
	biliClient      *http.Client
	mediaClient     *http.Client
	mediaBiliClient *http.Client
	maxBody         int64
	userAgent       string
	biliUA          string // B 站请求用的 UA：调用方没指定就用完整的桌面浏览器写法（bili_extra.go）
	noVisitor       bool   // 不取、不带匿名访客标识（只在 noVisitorCookie 为真时，即测试里）
	buvid           buvidCache
	interval        time.Duration
	nextAt          map[string]time.Time // 每个主机下一次请求最早的时刻；不同主机互不等待
	requestMu       sync.Mutex
	requests        atomic.Int64
	lookup          lookupIP
	policy          netPolicy // fake-ip 规则；媒体候选检查和安全拨号用同一份（network.go）
	rate            *rateLimiter
	fxTimeout       time.Duration
	syndication     bool
	xStats          routeStats // X 各线路最近的成败，排先后用（twitter_routes.go）
	// retrySleep 只供测试：替换“全部超时后隔 5 秒整条再试”前的等待（timeout_retry.go）；nil 真等
	retrySleep func(context.Context, time.Duration) error
	// roundTime 只供测试：第一轮取数的时限，NewFetcher 填 30 秒（重试按调用方的时限另算，见 timeout_retry.go）
	roundTime time.Duration
	// retry 是“全部超时后重试”的时限；只供测试按比例缩小，NewFetcher 填生产值
	retry retryTiming
}

func NewFetcher(o FetchOptions) (*Fetcher, error) {
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	if o.MaxBody == 0 {
		o.MaxBody = 4 << 20
	}
	biliUA := o.UserAgent
	if o.UserAgent == "" {
		o.UserAgent = "Mozilla/5.0"
		biliUA = browserUA
	}
	if o.Timeout < 0 || o.MaxBody < 1 {
		return nil, failure("invalid_options", "")
	}
	if o.FXTimeout < 0 {
		return nil, failure("invalid_options", "")
	}
	f := &Fetcher{maxBody: o.MaxBody, userAgent: o.UserAgent, biliUA: biliUA, noVisitor: noVisitorCookie, interval: o.MinInterval, nextAt: map[string]time.Time{}, fxTimeout: o.FXTimeout, syndication: !o.NoSyndication,
		roundTime: roundTimeout, retry: retryTiming{delay: timeoutRetryDelay, minRound: retryMinRound, maxRound: retryMaxRound}}
	fakeRanges := o.FakeIPRanges
	if fakeRanges == nil {
		fakeRanges = netsafe.DefaultFakeIPRanges
	}
	var err error
	if f.policy.fake, err = netsafe.ParseFakeIPRanges(fakeRanges); err != nil {
		return nil, failure("invalid_options", "fake_ip_ranges")
	}
	f.rate, err = newRateLimiter(o.Rate)
	if err != nil {
		return nil, err
	}
	rt := o.Transport
	if rt == nil {
		proxy := o.Proxy
		if proxy == "" {
			proxy = os.Getenv("HTTPS_PROXY")
			if proxy == "" {
				proxy = os.Getenv("HTTP_PROXY")
			}
		}
		var p *url.URL
		if proxy != "" {
			var err error
			p, err = url.Parse(proxy)
			if err != nil || p.Scheme != "http" || p.Host == "" || p.User != nil {
				return nil, failure("unsafe_proxy", "")
			}
		}
		// fake-ip 下名单内主机经这个代理按主机名连；国内直连的线路也一样（平时直连，只有碰到假地址才用它）
		f.policy.proxy = p
		directLookup := lookupIP(func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		})
		if o.directLookup != nil {
			directLookup = o.directLookup
		}
		xLookup := lookupIP(directLookup)
		if p != nil {
			xLookup = f.proxyLookup(p, o.Timeout, o.DoHTransport)
		}
		f.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			if domesticHost(host) {
				return directLookup(ctx, host)
			}
			return xLookup(ctx, host)
		}
		mainTransport := newLinkTransport(safeDial(p, xLookup, f.policy), o.Timeout)
		mainTransport.ResponseHeaderTimeout = o.Timeout
		mainTransport.ForceAttemptHTTP2 = true
		rt = mainTransport
		// 国内平台直连，不读取环境代理；所有请求仍绑定已校验地址。
		biliTransport := newLinkTransport(safeDial(nil, directLookup, f.policy), o.Timeout)
		biliTransport.ResponseHeaderTimeout = o.Timeout
		biliTransport.ForceAttemptHTTP2 = true
		f.biliClient = &http.Client{Transport: biliTransport, Timeout: o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	f.client = &http.Client{Transport: rt, Timeout: o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if f.biliClient == nil {
		f.biliClient = f.client
	}
	media, directMedia := *f.client, *f.biliClient
	media.Timeout, directMedia.Timeout = 0, 0
	f.mediaClient, f.mediaBiliClient = &media, &directMedia
	return f, nil
}

// Requests 统计实际发起的 HTTP 请求，包含回退和短链跳。
func (f *Fetcher) Requests() int64 { return f.requests.Load() }

// newLinkTransport 造取数传输，带连接健康参数：
// 空闲连接 30 秒回收；HTTP/2 上 15 秒没收到任何帧就发 ping 探活，5 秒内没有 pong 就关掉连接
// （http.HTTP2Config 的语义：SendPingTimeout 是“多久没帧就探活”，PingTimeout 是“ping 多久不回就断”）。
// 代理换节点或隧道断掉后，池里的连接可能已经死了但 Transport 发现不了；HTTP/2 还会把新请求继续放进这条死连接，
// 一直等到 ResponseHeaderTimeout（超时也只重置流、不断连接）。拨号、响应头时限、是否启用 HTTP/2 由各线路自己补。
func newLinkTransport(dial func(context.Context, string, string) (net.Conn, error), timeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext:         dial,
		TLSHandshakeTimeout: timeout,
		IdleConnTimeout:     30 * time.Second,
		HTTP2:               &http.HTTP2Config{SendPingTimeout: 15 * time.Second, PingTimeout: 5 * time.Second},
	}
}

type lookupIP func(context.Context, string) ([]netip.Addr, error)

// safeDial 是取数传输的拨号：proxy 是这条线路平时的出口（nil = 直连），policy 管 fake-ip（network.go 的 netPolicy.dial）
func safeDial(proxy *url.URL, lookup lookupIP, policy netPolicy) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return policy.dial(ctx, network, address, proxy, lookup)
	}
}
func dialPinned(ctx context.Context, network, pinned string, proxy *url.URL) (net.Conn, error) {
	d := net.Dialer{}
	if proxy == nil {
		return d.DialContext(ctx, network, pinned)
	}
	proxyAddr := proxy.Host
	if proxy.Port() == "" {
		proxyAddr = net.JoinHostPort(proxy.Hostname(), "80")
	}
	c, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	stopCancel := context.AfterFunc(ctx, func() { c.Close() })
	defer stopCancel()
	deadline := time.Now().Add(10 * time.Second)
	if v, ok := ctx.Deadline(); ok && v.Before(deadline) {
		deadline = v
	}
	_ = c.SetDeadline(deadline)
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: pinned}, Host: pinned, Header: make(http.Header)}
	if err = req.Write(c); err != nil {
		c.Close()
		return nil, err
	}
	reader := bufio.NewReader(c)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		c.Close()
		return nil, err
	}
	if resp.StatusCode != 200 || reader.Buffered() != 0 {
		c.Close()
		return nil, failure("proxy_failed", "connect")
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

// waitRequest 按主机限速：同一主机的两次请求至少隔 interval，不同主机互不等待。
// 先在锁里预约这个主机的下一个时刻，等待在锁外，所以一个主机慢不会堵住别的主机。
func (f *Fetcher) waitRequest(ctx context.Context, host string) error {
	host = strings.ToLower(host)
	f.requestMu.Lock()
	slot := f.nextAt[host]
	if now := time.Now(); slot.Before(now) {
		slot = now
	}
	f.nextAt[host] = slot.Add(f.interval)
	f.requestMu.Unlock()
	if wait := time.Until(slot); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return failure("timeout", "request_interval")
		case <-timer.C:
		}
	}
	return nil
}
func (f *Fetcher) request(ctx context.Context, raw, source string) ([]byte, int, http.Header, error) {
	return f.send(ctx, http.MethodGet, raw, source, nil, "")
}

// send 是 request 的通用形式：Misskey（JSON）和 Steam 工坊（表单）这类必须 POST 的匿名接口复用同一套
// 目标校验、限速、预算和响应上限；body 非空时按 contentType 发，不带用户的 Cookie 或凭据（B 站 API 只带服务端发的匿名访客标识，见 bili_extra.go）。
func (f *Fetcher) send(ctx context.Context, method, raw, source string, body []byte, contentType string) ([]byte, int, http.Header, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, rd)
	if err != nil {
		return nil, 0, nil, failure("unsafe_target", source)
	}
	platform := requestPlatform(raw)
	// 访客标识要在冷却和预算检查之前取：取它自己撞了风控，主请求就不用发了
	cookie := ""
	if platform == "bilibili" && !f.noVisitor && source != biliSPISource && biliAPIHost(req.URL.Hostname()) && ctxHeaders(ctx).Get("Cookie") == "" {
		cookie = f.visitorCookie(ctx)
	}
	if f.interval > 0 {
		// 先拒绝冷却和超预算；实际请求前再扣，等待不挪动预算窗口。
		if err := f.rate.waitAllow(ctx, platform, req.URL.Hostname(), false); err != nil {
			return nil, 0, nil, err
		}
		if err := f.waitRequest(ctx, req.URL.Hostname()); err != nil {
			return nil, 0, nil, err
		}
	}
	// 访客标识那一次请求（每六小时一次）只受冷却约束、不占每分钟预算，免得它把后面链接的主请求挤成 rate_limited
	// 额度用满只排队等空位（RateOptions.MaxBudgetWait），平台真拦了（冷却）才立刻失败
	if err := f.rate.waitAllow(ctx, platform, req.URL.Hostname(), source != biliSPISource); err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://www.bilibili.com/")
	setMoreHeaders(req, platform)
	if platform == "bilibili" {
		setBiliHeaders(req, f.biliUA)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
	}
	for k, v := range ctxHeaders(ctx) {
		req.Header[k] = v // 个别接口要求特定的 Referer 或访客标识（bili_extra.go）
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType) // 放在各平台请求头之后，不被它们改掉
	}
	f.requests.Add(1)
	client := f.client
	if domesticHost(req.URL.Hostname()) {
		client = f.biliClient
	}
	resp, err := client.Do(req)
	// 网络抖一下（代理断连、EOF、连接被重置）就判失败太脆：没有请求体、没超时没取消的，原样再发一次（2026-11-11 B 站两次 4.6 秒断开）
	// 重发也是一次请求：要过冷却和预算，不然冷却里、预算用完了还会多打一次。
	if err != nil && body == nil && ctx.Err() == nil && retryableNetErr(err) && f.rate.allowHost(platform, req.URL.Hostname(), true) == nil {
		f.requests.Add(1)
		resp, err = client.Do(req.Clone(ctx))
	}
	if err != nil {
		var fe *FetchError
		if errors.As(err, &fe) {
			return nil, 0, nil, fe
		}
		kind := "network"
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			kind = "timeout"
		}
		if errors.Is(err, context.Canceled) {
			kind = "canceled"
		}
		fe = failure(kind, source)
		fe.Detail = netErrDetail(err)
		return nil, 0, nil, fe
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		f.rate.cooldownHost(platform, req.URL.Hostname(), resp.Header.Get("Retry-After"))
	}
	if s, ok := serviceOf(platform); ok && s.riskBase > 0 && resp.StatusCode == http.StatusPreconditionFailed {
		// B 站的 412 是风控（IP 级）：平台整体冷却，所有调用方统一拿到 risk_control，不再换路重发。其他平台的 412 照普通状态码走。
		f.rate.riskCool(platform, resp.Header.Get("Retry-After"))
		return nil, resp.StatusCode, resp.Header, &FetchError{Kind: "risk_control", Source: source, Status: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	if platform == "github" {
		f.githubBudget(resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBody+1))
	if err != nil {
		return nil, resp.StatusCode, resp.Header, failure("network", source)
	}
	if int64(len(b)) > f.maxBody {
		return nil, resp.StatusCode, resp.Header, failure("response_too_large", source)
	}
	if s, ok := serviceOf(platform); ok && s.riskBase > 0 {
		f.noteRiskCode(platform, resp.Header.Get("Retry-After"), b)
	}
	return b, resp.StatusCode, resp.Header, nil
}

// noteRiskCode 看有风控语义的平台（B 站）响应里的业务码，在最底层统一记账，调用方是否吞掉错误都一样：
// -352、-412 是风控，平台冷却并翻倍退避（riskCool）；-509、-799 是频率码，平台短冷却（freqCool）；
// 0 是成功，冷却结束后的第一次成功让连续风控的计数归零（riskReset）。
// 不是 JSON 的（比如 HTML 页）不看。
func (f *Fetcher) noteRiskCode(platform, retry string, body []byte) {
	var e struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(body, &e) != nil || e.Code == nil {
		return
	}
	switch *e.Code {
	case -352, -412:
		f.rate.riskCool(platform, retry)
	case -509, -799:
		f.rate.freqCool(platform, retry)
	case 0:
		f.rate.riskReset(platform)
	}
}
func httpFailure(status int, h http.Header, source string) error {
	if status >= 200 && status < 300 {
		return nil
	}
	kind := "interface_error"
	switch status {
	case 404, 410:
		kind = "not_found"
	case 401, 403:
		kind = "login_required"
	case 429:
		kind = "rate_limited"
	}
	return &FetchError{Kind: kind, Source: source, Status: status, RetryAfter: h.Get("Retry-After")}
}
func (f *Fetcher) getJSON(ctx context.Context, raw, source string, out any) error {
	b, status, h, err := f.request(ctx, raw, source)
	if err != nil {
		return err
	}
	if err = httpFailure(status, h, source); err != nil {
		return err
	}
	if err = json.Unmarshal(b, out); err != nil {
		return failure("invalid_response", source)
	}
	return nil
}

// Metadata 复用相同的目标校验和网络预算，pixiv 在页图、动图和小说正文映射之前返回。
func (f *Fetcher) Metadata(ctx context.Context, l Link) (Result, error) {
	return f.fetch(ctx, l, true)
}

func (f *Fetcher) Complete(ctx context.Context, l Link, _ Result) (Result, error) {
	return f.Fetch(ctx, l)
}

func (f *Fetcher) Fetch(ctx context.Context, l Link) (Result, error) {
	return f.fetch(ctx, l, false)
}

// roundTimeout 是单个链接一轮取数的时限；全部超时后的第二轮按调用方的剩余时间另算（timeout_retry.go）。
const roundTimeout = 30 * time.Second

func (f *Fetcher) fetch(ctx context.Context, l Link, metadata bool) (Result, error) {
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, f.roundTime)
	defer cancel()
	canonical, ok := Recognize(l.Clean)
	if !ok || canonical.Platform != l.Platform || canonical.Kind != l.Kind || canonical.ID != l.ID {
		return Result{}, failure("invalid_target", "")
	}
	raw := l.Raw // 用户原文：GitHub 发出去的链接按它保住大小写（canonical 是从小写的 Clean 认出来的）
	l = canonical
	r, err := f.fetchRound(ctx, l, metadata)
	// 每条线路都超时是“一时没答上来”（网络一阵慢），隔 5 秒整条再试一次；明确失败不重试（规格-链接发送改版 C3）。
	// 判断和第二轮的时限都按调用方传进来的时限算：第一轮的 30 秒里 X 的三条线路依次超时就用掉 20 秒左右，剩下的不够再跑一轮。
	// 等之前先丢池里的连接：全超时常是代理换节点/隧道断了，第二轮复用死连接只会再超时一次（见 dropIdleConnections）。
	if err != nil && allTimedOut(err) && f.retryInBudget(callerCtx) {
		f.dropIdleConnections()
		if f.waitRetry(callerCtx) == nil {
			ctx, cancel = f.retryContext(callerCtx)
			defer cancel()
			r, err = f.retryAfterTimeout(ctx, l, metadata, err)
		}
	}
	if err != nil {
		return Result{}, err
	}
	f.inspectMedia(ctx, &r)
	// 直接调 Fetch 的入口（样张、link-probe）也拿到展示链接；经 Service.Get 的按每次用户原文重写
	r.DisplayURL = githubDisplay(raw, l)
	return r, nil
}

// fetchRound 跑一轮：解短链、交给对应站点。线路级失败在这里补总 Trail（重试时两轮各记各的耗时）。
func (f *Fetcher) fetchRound(ctx context.Context, l Link, metadata bool) (Result, error) {
	started := time.Now()
	if l.Kind == "short" {
		var err error
		l, err = f.Resolve(ctx, l)
		if err != nil {
			return Result{}, err
		}
	}
	var r Result
	var err error
	switch {
	case l.Platform == "x" && l.Kind == "post":
		r, err = f.fetchX(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "video":
		r, err = f.fetchBili(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "mall":
		r, err = f.fetchMall(ctx, l)
	case l.Platform == "bilibili" && (l.Kind == "episode" || l.Kind == "season" || l.Kind == "media"):
		r, err = f.fetchBangumi(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "course":
		r, err = f.fetchCourse(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "audio":
		r, err = f.fetchAudio(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "collection":
		r, err = f.fetchCollection(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "article":
		r, err = f.fetchArticle(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "workshop":
		r, err = f.fetchWorkshop(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "toy":
		r, err = f.fetchBiliToy(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "live":
		r, err = f.fetchBiliLive(ctx, l)
	case l.Platform == "bilibili" && l.Kind == "user":
		r, err = f.fetchBiliSpace(ctx, l)
	case l.Platform == "bilibili" && (l.Kind == "opus" || l.Kind == "dynamic"):
		r, err = f.fetchOpus(ctx, l)
	case l.Platform == "steam":
		r, err = f.fetchSteam(ctx, l)
	case l.Platform == "github":
		r, err = f.fetchGitHub(ctx, l)
	case l.Platform == "youtube":
		r, err = f.fetchYouTube(ctx, l)
	case l.Platform == "douyin":
		r, err = f.fetchDouyin(ctx, l)
	case l.Platform == "netease":
		r, err = f.fetchNetease(ctx, l)
	case l.Platform == "booth" && l.Kind == "item":
		r, err = f.fetchBooth(ctx, l)
	case l.Platform == "afdian":
		r, err = f.fetchAfdian(ctx, l)
	case l.Platform == "pixiv":
		r, err = f.fetchPixiv(ctx, l, metadata)
	case l.Platform == "bluesky":
		r, err = f.fetchBluesky(ctx, l)
	case l.Platform == "telegram":
		r, err = f.fetchTelegram(ctx, l)
	case l.Platform == "misskey" || l.Platform == "mastodon":
		r, err = f.fetchFediverse(ctx, l)
	case l.Platform == "spotify":
		r, err = f.fetchSpotify(ctx, l)
	case l.Platform == "tiktok":
		r, err = f.fetchTikTok(ctx, l)
	case l.Platform == "xiaoheihe" || l.Platform == "xiaohongshu":
		r, err = f.fetchCN(ctx, l)
	default:
		return Result{}, failure("unsupported", l.Platform)
	}
	if err != nil {
		var fe *FetchError
		if errors.As(err, &fe) && fe.Trail == "" {
			fe.Trail = routeTrail("", fe.Source, fe.Kind, time.Since(started))
		}
		return Result{}, err
	}
	return r, nil
}
func (f *Fetcher) Resolve(ctx context.Context, l Link) (Link, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	canonical, ok := Recognize(l.Clean)
	if !ok || canonical.Kind != "short" || canonical.Platform != l.Platform {
		return Link{}, failure("invalid_target", "short")
	}
	if l.Kind != "short" {
		return Link{}, failure("invalid_target", "short")
	}
	current := canonical.Clean
	seen := map[string]bool{}
	for range 5 {
		if seen[current] {
			return Link{}, failure("redirect_loop", "short")
		}
		seen[current] = true
		_, status, h, err := f.request(ctx, current, "short")
		if err != nil {
			return Link{}, err
		}
		if status != 301 && status != 302 && status != 303 && status != 307 && status != 308 {
			if err = httpFailure(status, h, "short"); err != nil {
				return Link{}, err
			}
			return Link{}, failure("invalid_redirect", "short")
		}
		u, _ := url.Parse(current)
		next, err := u.Parse(h.Get("Location"))
		if err != nil || h.Get("Location") == "" {
			return Link{}, failure("invalid_redirect", "short")
		}
		target, ok := Recognize(next.String())
		if !ok && tiktokHomeRedirect(next) {
			// 失效的 TikTok 短链落到首页，按平台没有该内容处理，不当成不安全的跳转。
			return Link{}, failure("not_found", "short")
		}
		if !ok && tiktokDomainTarget(next) {
			return Link{}, failure("unsupported", "short")
		}
		if !ok {
			return Link{}, failure("unsafe_target", "short")
		}
		// 最终页不请求；只交给已支持的匿名接口。
		if target.Kind != "short" {
			if fetchableMore(target) || (target.Platform == "x" && target.Kind == "post") || biliFetchable(target) || target.Platform == "xiaohongshu" {
				return target, nil
			}
			return Link{}, failure("unsupported", "short")
		}
		current = target.Clean
	}
	return Link{}, failure("too_many_redirects", "short")
}
func stringField(p *string) Field[string] {
	if p == nil {
		return missing[string]("interface_omitted")
	}
	return available(*p)
}
func intField(p *int) Field[int] {
	if p == nil {
		return missing[int]("interface_omitted")
	}
	return available(*p)
}
func floatField(p *float64) Field[float64] {
	if p == nil {
		return missing[float64]("interface_omitted")
	}
	return available(*p)
}
func unixField(p *int64) Field[time.Time] {
	if p == nil {
		return missing[time.Time]("interface_omitted")
	}
	return available(time.Unix(*p, 0).UTC())
}
func trimID(s string) string { return strings.TrimPrefix(s, "av") }

// retryableNetErr 是连接被对方或代理断开、没拿到响应的错误（EOF、connection reset、aborted）；超时、取消、DNS、我们自己判的不算。
func retryableNetErr(err error) bool {
	var fe *FetchError
	if errors.As(err, &fe) {
		return false
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || (errors.As(err, &ne) && ne.Timeout()) {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED)
}

// netErrDetail 取网络错误的原文给日志看：剥掉 url.Error 外层（那层带完整网址，查询串里可能有令牌），限长。
func netErrDetail(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}
	s := strings.TrimSpace(err.Error())
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}
