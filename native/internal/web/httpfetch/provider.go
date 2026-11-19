package httpfetch

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	readability "codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"sumika-native/internal/web"
)

type Provider struct {
	Policy  web.URLPolicy
	Dial    func(context.Context, string, string) (net.Conn, error)
	Version string
	// Proxy 只在域名解析到代理的假地址（netsafe 的默认假地址段，IPv4 198.18.0.0/15、IPv6 2001:2::/48 等，fake-ip）时才用：让代理按域名自己解析并出网。
	// 为空用环境变量里的代理；没有代理就直接连假地址（TUN 会接住）。
	Proxy func(*http.Request) (*url.URL, error)
}

// browserUA 用常见桌面浏览器的写法：很多站点会直接拒绝带程序名的 User-Agent。
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

func (*Provider) ID() string       { return "http" }
func (*Provider) Available() error { return nil }
func (p *Provider) Fetch(ctx context.Context, q web.FetchRequest) (web.ProviderFetchResult, error) {
	target, err := p.Policy.Check(ctx, q.URL)
	if err != nil {
		return web.ProviderFetchResult{}, err
	}
	cacheable := true
	for hop := range 6 {
		host := target.URL.Hostname()
		dial := p.Dial
		if dial == nil {
			dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
		}
		tr := p.transport(host, target, dial)
		client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL.String(), nil)
		if err != nil {
			return web.ProviderFetchResult{}, err
		}
		req.Header.Set("User-Agent", browserUA)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		res, err := web.DoHTTP(ctx, client, req)
		if err != nil {
			tr.CloseIdleConnections()
			return web.ProviderFetchResult{}, err
		}
		if strings.Contains(strings.ToLower(res.Header.Get("Cache-Control")), "no-store") {
			cacheable = false
		}
		switch res.StatusCode {
		case 301, 302, 303, 307, 308:
			location := res.Header.Get("Location")
			res.Body.Close()
			tr.CloseIdleConnections()
			if hop == 5 {
				return web.ProviderFetchResult{}, fmt.Errorf("WEB_REDIRECT_LIMIT")
			}
			next, err := url.Parse(location)
			if err != nil {
				return web.ProviderFetchResult{}, err
			}
			next = target.URL.ResolveReference(next)
			// 允许跨站跳转（example.com -> www.example.com、http -> https）；每一跳都重新过 Policy.Check，内网照拒。
			target, err = p.Policy.Check(ctx, next.String())
			if err != nil {
				return web.ProviderFetchResult{}, err
			}
			continue
		}
		status := res.StatusCode
		final := target.URL.String()
		out := web.ProviderFetchResult{Result: web.FetchResult{Provider: "http", RequestedURL: q.URL, ReturnedURL: final, FinalURL: &final, OriginStatus: &status, ContentKind: q.Format, Usage: web.Usage{UpstreamRequests: hop + 1}}, Cacheable: cacheable}
		if status < 200 || status >= 300 || status == 206 {
			res.Body.Close()
			return out, fmt.Errorf("WEB_HTTP_STATUS_%d", status)
		}
		limit := web.ResponseLimit(ctx)
		data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		res.Body.Close()
		tr.CloseIdleConnections()
		if err != nil {
			return out, err
		}
		if int64(len(data)) > limit {
			return out, fmt.Errorf("WEB_RESPONSE_TOO_LARGE")
		}
		if len(data) == 0 {
			out.Result.CompletenessKnown = true
			return out, nil
		}
		media, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
		if err != nil {
			return out, fmt.Errorf("WEB_CONTENT_TYPE")
		}
		switch media {
		case "text/html", "application/xhtml+xml", "text/plain", "application/json", "application/xml", "text/xml":
		default:
			return out, fmt.Errorf("WEB_CONTENT_TYPE")
		}
		if bytes.HasPrefix(data, []byte("%PDF")) || bytes.IndexByte(data, 0) >= 0 {
			return out, fmt.Errorf("WEB_BINARY_CONTENT")
		}
		if label := params["charset"]; label != "" {
			if encoding, _ := charset.Lookup(label); encoding == nil {
				return out, fmt.Errorf("WEB_CHARSET_UNSUPPORTED")
			}
		}
		reader, err := charset.NewReader(bytes.NewReader(data), media+"; charset="+params["charset"])
		if err != nil {
			return out, err
		}
		text, err := io.ReadAll(io.LimitReader(reader, limit+1))
		if err != nil {
			return out, err
		}
		if int64(len(text)) > limit || !utf8.Valid(text) {
			return out, fmt.Errorf("WEB_DECODE_LIMIT")
		}
		content := string(text)
		if media == "text/html" || media == "application/xhtml+xml" {
			doc, err := html.Parse(strings.NewReader(content))
			if err != nil {
				return out, err
			}
			count := 0
			if err = clean(doc, 0, &count); err != nil {
				return out, err
			}
			var b bytes.Buffer
			if err = html.Render(&b, doc); err != nil {
				return out, err
			}
			// 先用 Readability 算法挑出正文；挑不出（太短，或只是页面的一小角）就退回整页清洗。
			var plain strings.Builder
			plainText(doc, &plain)
			wholeText := strings.TrimSpace(plain.String())
			out.Result.Title = pageTitle(doc)
			pageHTML, pageText := b.String(), wholeText
			if art, ok := pickArticle(b.String(), target.URL, wholeText); ok {
				pageHTML, pageText = art.html, art.text
				if t := strings.TrimSpace(art.title); t != "" {
					out.Result.Title = t
				}
			}
			content, err = htmltomarkdown.NewConverter(final, true, nil).ConvertString(pageHTML)
			if err != nil {
				return out, err
			}
			if q.Format == "text" {
				content = pageText
			}
		}
		runes := []rune(content)
		if len(runes) > 12000 {
			content = string(runes[:12000])
			out.Result.Truncated = true
			out.Result.TruncationReason = "model_limit"
		}
		out.Result.Content = content
		out.Result.CompletenessKnown = true
		return out, nil
	}
	return web.ProviderFetchResult{}, fmt.Errorf("WEB_REDIRECT_LIMIT")
}

// transport 按目标选传输：普通地址钉住已检查过的 IP 直连；含代理假地址时，有代理就交给代理按域名解析，
// 没代理就直连假地址（本机 TUN 接管）。
func (p *Provider) transport(host string, target web.Target, dial func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	tlsConf := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if web.HasFakeIP(target.IPs) {
		proxy := p.Proxy
		if proxy == nil {
			proxy = http.ProxyFromEnvironment
		}
		probe := &http.Request{URL: target.URL}
		if u, err := proxy(probe); err == nil && u != nil {
			return &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true, TLSClientConfig: tlsConf, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return dial(ctx, network, address)
			}}
		}
	}
	return &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: tlsConf, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var last error
		for _, ip := range target.IPs {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}}
}
func clean(n *html.Node, depth int, count *int) error {
	*count++
	if depth > 512 || *count > 100000 {
		return fmt.Errorf("WEB_HTML_LIMIT")
	}
	for child := n.FirstChild; child != nil; {
		next := child.NextSibling
		drop := false
		if child.Type == html.ElementNode {
			switch child.Data {
			case "script", "style", "template", "iframe", "object", "embed", "noscript":
				drop = true
			}
			for _, a := range child.Attr {
				if a.Key == "hidden" || a.Key == "aria-hidden" && a.Val == "true" || a.Key == "style" && (strings.Contains(strings.ReplaceAll(a.Val, " ", ""), "display:none") || strings.Contains(strings.ReplaceAll(a.Val, " ", ""), "visibility:hidden")) {
					drop = true
				}
			}
		}
		if drop {
			n.RemoveChild(child)
		} else if err := clean(child, depth+1, count); err != nil {
			return err
		}
		child = next
	}
	return nil
}

func plainText(n *html.Node, b *strings.Builder) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		plainText(child, b)
	}
	if n.Type == html.ElementNode {
		switch n.Data {
		case "p", "div", "li", "br", "h1", "h2", "h3":
			b.WriteByte('\n')
		}
	}
}

// 挑出的正文少于这么多字，或不到整页文字的这个比例（整页又很长），就不信它：多半是目录页、列表页，
// Readability 只抓到了其中一小块。
const (
	articleMinRunes = 200
	articleMinRatio = 0.10
	ratioFloorRunes = 2000
)

type pickedArticle struct{ html, text, title string }

// pickArticle 用 Readability 挑正文。返回 ok=false 表示该退回整页清洗。
func pickArticle(pageHTML string, base *url.URL, wholeText string) (pickedArticle, bool) {
	article, err := readability.FromReader(strings.NewReader(pageHTML), base)
	if err != nil || article.Node == nil {
		return pickedArticle{}, false
	}
	var h, t strings.Builder
	if article.RenderHTML(&h) != nil || article.RenderText(&t) != nil {
		return pickedArticle{}, false
	}
	text := strings.TrimSpace(t.String())
	n := utf8.RuneCountInString(text)
	whole := utf8.RuneCountInString(wholeText)
	if n < articleMinRunes || strings.TrimSpace(h.String()) == "" {
		return pickedArticle{}, false
	}
	if whole > ratioFloorRunes && float64(n) < articleMinRatio*float64(whole) {
		return pickedArticle{}, false
	}
	return pickedArticle{html: h.String(), text: text, title: article.Title()}, true
}

// pageTitle 取 <title> 的文字。
func pageTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "title" {
		var b strings.Builder
		plainText(n, &b)
		return strings.Join(strings.Fields(b.String()), " ")
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if t := pageTitle(child); t != "" {
			return t
		}
	}
	return ""
}
