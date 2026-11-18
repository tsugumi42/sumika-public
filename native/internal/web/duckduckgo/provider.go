// Package duckduckgo 是不要 key 的兜底搜索提供方：请求 html.duckduckgo.com/html 并解析结果。
// 遇到人机校验页只报"暂时不可用"，不尝试绕过。
package duckduckgo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"

	"golang.org/x/net/html"
	"sumika-native/internal/web"
)

type Provider struct {
	client *http.Client
	base   string
	do     func(context.Context, *http.Client, *http.Request) (*http.Response, error)
}

func New(client *http.Client) *Provider {
	return &Provider{client: web.NoRedirectClient(client), base: "https://html.duckduckgo.com", do: web.DoHTTP}
}
func (*Provider) ID() string       { return "duckduckgo" }
func (*Provider) Available() error { return nil }

// ErrUnavailable 表示 DuckDuckGo 返回了人机校验或限流页。
var ErrUnavailable = fmt.Errorf("DuckDuckGo 搜索服务暂时不可用（对方要求人机校验或限流）")

var df = map[string]string{"day": "d", "week": "w", "month": "m", "year": "y"}

func (p *Provider) Search(ctx context.Context, q web.ProviderSearchRequest) (web.ProviderSearchResult, error) {
	out := web.ProviderSearchResult{Provider: p.ID(), Usage: web.FreeUsage(0)}
	form := url.Values{"q": {q.Query}}
	if d, ok := df[q.TimeRange]; ok {
		form.Set("df", d)
	}
	send := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/html/", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
		req.Header.Set("Accept", "text/html")
		return p.do(ctx, p.client, req)
	}
	res, err := send()
	// 对方偶尔直接断开连接（实测 Post …: EOF）：只对断连重试一次，额度、限流、取消这些不重试
	if err != nil && ctx.Err() == nil && droppedConn(err) {
		res, err = send()
	}
	if err != nil {
		return out, err
	}
	status := res.StatusCode
	data, _, err := web.ReadBody(ctx, res, "DuckDuckGo")
	if err != nil {
		if status == http.StatusTooManyRequests || status == http.StatusForbidden {
			return out, ErrUnavailable
		}
		return out, err
	}
	if status == http.StatusAccepted || challenge(data) {
		return out, ErrUnavailable
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return out, fmt.Errorf("DuckDuckGo 响应格式错误")
	}
	out.Sources = parse(doc)
	out.Cacheable = true
	out.Usage = web.FreeUsage(len(out.Sources))
	return out, nil
}

// droppedConn 是连接被对方断开（没拿到任何响应）。
func droppedConn(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED)
}

func challenge(b []byte) bool {
	s := strings.ToLower(string(b))
	return strings.Contains(s, "anomaly-modal") || strings.Contains(s, "anomaly.js") || strings.Contains(s, "g-recaptcha") || strings.Contains(s, "are you a human") || strings.Contains(s, "unfortunately, bots use duckduckgo too")
}

func hasClass(n *html.Node, c string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, f := range strings.Fields(a.Val) {
				if f == c {
					return true
				}
			}
		}
	}
	return false
}
func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}
func text(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(sb.String()), " ")
}

// target 还原 DuckDuckGo 的跳转链接 //duckduckgo.com/l/?uddg=<编码后的真实地址>。
func target(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		href = u.Query().Get("uddg")
		if u, err = url.Parse(href); err != nil {
			return ""
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || strings.HasSuffix(u.Hostname(), "duckduckgo.com") {
		return ""
	}
	return href
}

func parse(doc *html.Node) []web.Source {
	var out []web.Source
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result") && !hasClass(n, "result--ad") {
			var link, snippet *html.Node
			var find func(*html.Node)
			find = func(m *html.Node) {
				if m.Type == html.ElementNode {
					if link == nil && m.Data == "a" && hasClass(m, "result__a") {
						link = m
					}
					if snippet == nil && hasClass(m, "result__snippet") {
						snippet = m
					}
				}
				for c := m.FirstChild; c != nil; c = c.NextSibling {
					find(c)
				}
			}
			find(n)
			if link != nil {
				if u := target(attr(link, "href")); u != "" {
					s := web.Source{URL: u, Title: text(link)}
					if snippet != nil {
						s.Snippet = text(snippet)
					}
					out = append(out, s)
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}
