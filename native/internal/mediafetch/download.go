package mediafetch

// 下载一个地址（规格-QQ收图下载和断线补录 4.2、4.4）。地址由协议端给，协议端会把发送者能影响的字段原样带出来，
// 所以地址不可信：只认白名单里的主机，域名解析到内网的也不连，跳转每一跳重新核对，图片和视频分别限额、认头。
// 出错都归成几个固定的短原因，不把 net/http 的错误文字（里面有完整地址和查询串，查询串里有 rkey）往外带。

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"sumika-native/internal/mediastore"
	"sumika-native/internal/netsafe"
	"sumika-native/internal/store"
)

// 终局状态借用 store 里的常量
const (
	StateExpired  = store.MediaExpired
	StateTooBig   = store.MediaTooBig
	StateNotImage = store.MediaNotImage
	StateBlocked  = store.MediaBlocked
	StateFailed   = store.MediaFailed
)

// Error 是一次下载失败的归类。Reason 是短原因，不含地址和查询串
type Error struct {
	State   string // 不再重试时记成的终局状态
	Retry   bool   // 网络错误、超时、5xx、429、写盘出错：值得过一阵再试
	Refresh bool   // 400、403、404、410：地址可能只是过期了，值得向协议端要一个新的
	Reason  string
	Status  int   // HTTP 状态码，没有响应时为 0
	Size    int64 // 响应头已声明的完整大小；流式超限时仍未知
}

func (e *Error) Error() string { return e.Reason }

// DefaultHosts 是能访问的主机：来自 SnowLuma 生成图片地址的几处（规格 4.2），加上真机日志里被拦下的两个腾讯图床
// （qq.ugcimg.cn、tianquan.gtimg.cn，2026-11-03 主人同意），都按完整域名放，不放宽后缀。
// .ftn.qq.com 是 QQ 文件中转（群文件、视频文件，例如 gzc-download.ftn.qq.com），机房前缀不固定，按后缀放
// （2026-11-06 主人截图报"主机不在白名单：gzc-download.ftn.qq.com"）
var DefaultHosts = HostRule{
	Exact:  []string{"multimedia.nt.qq.com.cn", "gchat.qpic.cn", "p.qpic.cn", "gxh.vip.qq.com", "qq.ugcimg.cn", "tianquan.gtimg.cn"},
	Suffix: []string{".qpic.cn", ".nt.qq.com.cn", ".ftn.qq.com"},
}

// HostRule 是主机白名单：完全匹配，或者以 Suffix（带前导点）结尾且前面还有内容
type HostRule struct {
	Exact  []string
	Suffix []string
}

func (r HostRule) allows(host string) bool {
	for _, h := range r.Exact {
		if host == h {
			return true
		}
	}
	for _, s := range r.Suffix {
		if len(host) > len(s) && strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// PutFunc 把下载到的内容存进媒体库，返回 hash 和字节数（mediastore.Put 加上数据目录和平台）
type PutFunc func(r io.Reader, maxBytes int64) (hash string, size int64, err error)

// DownloadOptions 是下载器的参数。零值都用规格 4.2 的数；LookupIP、Dial 只有测试换（把连接引到假服务器上）
type DownloadOptions struct {
	Hosts          HostRule
	MaxBytes       int64         // 单张上限，默认 20 MB
	VideoMaxBytes  int64         // 视频自动下载独立上限，默认 100 MiB
	Timeout        time.Duration // 一次请求总时限，默认 20 秒
	ConnectTimeout time.Duration // 建连，默认 5 秒
	// FakeIPRanges 是代理的假地址段（app 从 linkReader.fakeIPRanges 解析好传进来）。nil 用 netsafe 的默认段；空切片表示没有假地址段
	FakeIPRanges []netip.Prefix
	LookupIP     func(ctx context.Context, host string) ([]net.IP, error)
	Dial         func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Downloader 下载一个地址。可以并发用
type Downloader struct {
	hosts    HostRule
	max      int64
	videoMax int64
	timeout  time.Duration
	fake     []netip.Prefix
	lookup   func(ctx context.Context, host string) ([]net.IP, error)
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	client   *http.Client
}

const (
	defaultMaxBytes      = 20 << 20
	defaultTimeout       = 20 * time.Second
	DefaultVideoMaxBytes = 100 << 20
	defaultConnect       = 5 * time.Second
	maxRedirects         = 3
	maxURLLen            = 2048
)

func NewDownloader(o DownloadOptions) *Downloader {
	d := &Downloader{hosts: o.Hosts, max: o.MaxBytes, timeout: o.Timeout, lookup: o.LookupIP, dial: o.Dial}
	if len(d.hosts.Exact) == 0 && len(d.hosts.Suffix) == 0 {
		d.hosts = DefaultHosts
	}
	if o.FakeIPRanges == nil {
		d.fake = netsafe.DefaultFakeIPPrefixes()
	} else {
		d.fake = slices.Clone(o.FakeIPRanges)
	}
	if d.max <= 0 {
		d.max = defaultMaxBytes
	}
	if o.VideoMaxBytes > 0 {
		d.videoMax = o.VideoMaxBytes
	} else {
		d.videoMax = DefaultVideoMaxBytes
	}
	if d.timeout <= 0 {
		d.timeout = defaultTimeout
	}
	if d.lookup == nil {
		d.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	connect := o.ConnectTimeout
	if connect <= 0 {
		connect = defaultConnect
	}
	if d.dial == nil {
		nd := &net.Dialer{Timeout: connect}
		d.dial = nd.DialContext
	}
	tr := &http.Transport{
		Proxy:                 nil, // 不走环境里的代理：拨号要连的就是核对过的那个地址
		DialContext:           d.dialChecked,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: d.timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		DisableCompression:    true, // 图片不压缩传输；也省得把"解压后的大小"和上限的关系再想一遍
	}
	d.client = &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return &Error{State: StateFailed, Reason: "跳转太多"}
			}
			_, err := d.checkURL(req.URL)
			return err
		},
	}
	return d
}

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// checkURL 核对地址能不能访问，返回小写的主机名。不合格的是 blocked
func (d *Downloader) checkURL(u *url.URL) (string, error) {
	blocked := func(why string) (string, error) {
		return "", &Error{State: StateBlocked, Reason: why}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return blocked("地址不是 http 或 https")
	}
	if u.User != nil {
		return blocked("地址带了用户名")
	}
	host := strings.ToLower(u.Hostname())
	if len(u.String()) > maxURLLen || !hostRe.MatchString(host) {
		return blocked("主机名不合格")
	}
	switch port := u.Port(); {
	case port == "":
	case port == "80" && u.Scheme == "http", port == "443" && u.Scheme == "https":
	default:
		return blocked("端口不是默认端口")
	}
	if !d.hosts.allows(host) {
		if len(host) > 80 {
			host = host[:80]
		}
		return blocked("主机不在白名单：" + host)
	}
	return host, nil
}

// blockedIP：不是外网的地址不连。判断用 netsafe.PublicIP（和链接取数同一份白名单：回环、私网、链路本地、CGNAT、保留段、
// 组播、NAT64 等一概不连），只在它上面放行代理的假地址段（fake：和 linkReader.fakeIPRanges 同一份配置，规格 4.4）。
// 开着代理 TUN 的机器上，名单内的 QQ 主机会解析成假地址，挡了所有下载都会失败；IPv4、IPv6 两种假地址都可能出现，
// 解析结果里只要有一个既不是外网、也不在假地址段里，就整个不连
func blockedIP(fake []netip.Prefix, ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	return !netsafe.InFakeRanges(fake, a) && !netsafe.PublicIP(a)
}

// dialChecked 是 Transport 的拨号：自己解析，解析出来的地址里有一个不该连的就整个不连，再按核对过的地址去拨
// （不让 Transport 再解析一遍，核对和连接之间名字换了指向就没用了）。TLS 由 Transport 在这条连接上按原主机名握手
func (d *Downloader) dialChecked(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, &Error{State: StateBlocked, Reason: "地址不合格"}
	}
	var ips []net.IP
	if lit := net.ParseIP(host); lit != nil {
		ips = []net.IP{lit}
	} else if ips, err = d.lookup(ctx, host); err != nil || len(ips) == 0 {
		return nil, &Error{State: StateFailed, Retry: true, Reason: "域名解析失败"}
	}
	for _, ip := range ips {
		if blockedIP(d.fake, ip) {
			return nil, &Error{State: StateBlocked, Reason: "域名解析到了内网或本机地址"}
		}
	}
	for _, ip := range ips {
		if c, err := d.dial(ctx, network, net.JoinHostPort(ip.String(), port)); err == nil {
			return c, nil
		}
	}
	return nil, &Error{State: StateFailed, Retry: true, Reason: "连接失败"}
}

// Download 下载 rawURL 的内容，交给 put 存下。失败返回 *Error（ctx 被取消返回 ctx 的错误）
func (d *Downloader) Download(ctx context.Context, rawURL string, put PutFunc) (hash string, size int64, err error) {
	return d.download(ctx, rawURL, put, false, d.max, d.timeout, nil)
}

// DownloadVideo 与图片共用网络边界，自动下载固定上限 100 MiB（ADR-255）。
func (d *Downloader) DownloadVideo(ctx context.Context, rawURL string, put PutFunc) (string, int64, error) {
	return d.download(ctx, rawURL, put, true, d.videoMax, d.timeout, nil)
}

// 手动不套图片的总时限：大视频允许持续传输，仍限制建连、响应头，并由任务取消中断正文。
func (d *Downloader) download(ctx context.Context, rawURL string, put PutFunc, video bool, maxBytes int64, timeout time.Duration, progress func(int64, int64)) (hash string, size int64, err error) {
	return d.downloadChecked(ctx, rawURL, put, video, maxBytes, timeout, progress, 512, nil)
}

func (d *Downloader) downloadChecked(ctx context.Context, rawURL string, put PutFunc, video bool, maxBytes int64, timeout time.Duration, progress func(int64, int64), headBytes int, check func([]byte) error) (hash string, size int64, err error) {
	u, perr := url.Parse(rawURL)
	if perr != nil {
		return "", 0, &Error{State: StateBlocked, Reason: "地址解析不了"}
	}
	if _, err := d.checkURL(u); err != nil {
		return "", 0, err
	}
	var rctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		rctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		rctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	req, perr := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	if perr != nil {
		return "", 0, &Error{State: StateBlocked, Reason: "地址解析不了"}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 Sumika")
	if video {
		req.Header.Set("Accept", "video/*,*/*;q=0.5")
	} else {
		req.Header.Set("Accept", "image/*,*/*;q=0.5")
	}
	resp, derr := d.client.Do(req)
	if derr != nil {
		return "", 0, d.classify(ctx, rctx, derr)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	// QQ 图片服务器的地址过期（rkey 失效）回的是 400，正文 {"retcode":-5503007,"retmsg":"download url has expired"}，
	// 2026-11-02 真机上一次补扫 256 张全是这样；和 403、404、410 一样先向协议端换新地址
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		return "", 0, &Error{State: StateExpired, Refresh: true, Status: resp.StatusCode, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode >= 500:
		return "", 0, &Error{State: StateFailed, Retry: true, Status: resp.StatusCode, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	default:
		return "", 0, &Error{State: StateFailed, Status: resp.StatusCode, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	if resp.ContentLength > maxBytes {
		return "", 0, &Error{State: StateTooBig, Reason: "超过大小上限", Size: resp.ContentLength}
	}
	if progress != nil {
		progress(0, max(resp.ContentLength, 0))
		resp.Body = &progressBody{ReadCloser: resp.Body, total: max(resp.ContentLength, 0), progress: progress}
	}
	// 认不出目标种类的文件头就不落盘，避免把错误页或另一类媒体存成视频。
	head := make([]byte, min(int64(headBytes), maxBytes+1))
	n, rerr := io.ReadFull(resp.Body, head)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		return "", 0, d.classify(ctx, rctx, rerr)
	}
	head = head[:n]
	accepted := true
	if check != nil {
		if err := check(head); err != nil {
			var de *Error
			if errors.As(err, &de) && resp.ContentLength > 0 {
				de.Size = resp.ContentLength
			}
			return "", 0, err
		}
	} else if video {
		accepted = mediastore.VideoExtension(head) != ""
	} else {
		accepted = isBitmap(head)
	}
	if !accepted {
		if bytes.Contains(bytes.ToLower(head), []byte("expired")) {
			return "", 0, &Error{State: StateExpired, Refresh: true, Reason: "返回的是已过期的提示"}
		}
		if video {
			return "", 0, &Error{State: store.MediaNotVideo, Reason: "内容不是已支持的视频容器"}
		}
		return "", 0, &Error{State: StateNotImage, Reason: "内容不是位图"}
	}
	body := &errTrack{r: resp.Body}
	hash, size, perr = put(io.MultiReader(bytes.NewReader(head), body), maxBytes)
	switch {
	case perr == nil:
		return hash, size, nil
	case errors.Is(perr, mediastore.ErrMediaTooBig):
		return "", 0, &Error{State: StateTooBig, Reason: "超过大小上限"}
	case ctx.Err() != nil:
		return "", 0, ctx.Err()
	case body.err != nil:
		return "", 0, d.classify(ctx, rctx, body.err)
	}
	return "", 0, &Error{State: StateFailed, Retry: true, Reason: "写入媒体库失败"}
}

type progressBody struct {
	io.ReadCloser
	n, total int64
	progress func(int64, int64)
}

func (r *progressBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.n += int64(n)
	if n > 0 {
		r.progress(r.n, r.total)
	}
	return n, err
}

// errTrack 记下读响应体时出的错，好分清 Put 的失败是网络断了还是写盘出错
type errTrack struct {
	r   io.Reader
	err error
}

func (e *errTrack) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

// classify 把 net/http 的错误归成固定的短原因（原错误里有完整地址，不往外带）
func (d *Downloader) classify(parent, rctx context.Context, err error) error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case parent.Err() != nil:
		return parent.Err()
	case errors.Is(rctx.Err(), context.DeadlineExceeded), isTimeout(err):
		return &Error{State: StateFailed, Retry: true, Reason: "超时"}
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return &Error{State: StateFailed, Retry: true, Reason: "域名解析失败"}
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return &Error{State: StateFailed, Retry: true, Reason: "连接失败"}
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return &Error{State: StateFailed, Reason: "证书校验失败"}
	}
	return &Error{State: StateFailed, Retry: true, Reason: "网络错误"}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// HostOf 是日志里能写的地址部分：只有主机名（小写，只留字母数字点横线，最长 80），查询串和路径一概不要
func HostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	h := strings.ToLower(u.Hostname())
	if len(h) > 80 || !hostRe.MatchString(h) {
		return "?"
	}
	return h
}
