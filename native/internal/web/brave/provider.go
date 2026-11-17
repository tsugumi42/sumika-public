// Package brave 是 Brave Search 提供方（GET https://api.search.brave.com/res/v1/web/search，X-Subscription-Token）。
package brave

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"sumika-native/internal/web"
)

type Provider struct {
	key    string
	client *http.Client
	base   string
	bal    *web.Balances
	do     func(context.Context, *http.Client, *http.Request) (*http.Response, error)
}

func New(key string, client *http.Client) *Provider {
	return &Provider{key: key, client: web.NoRedirectClient(client), base: "https://api.search.brave.com", do: web.DoHTTP}
}
func (*Provider) ID() string { return "brave" }

// SetBalances 接上余额记录：Brave 每次响应头 X-RateLimit-Remaining 的第二个值是本月剩余，不另发请求。
func (p *Provider) SetBalances(b *web.Balances) { p.bal = b }

// monthlyRemaining 解析 "每秒剩余, 本月剩余" 形式的头；Limit 头第二个值为 0 表示不限。
func monthlyRemaining(h http.Header) (remaining int64, unlimited, ok bool) {
	parts := strings.Split(h.Get("X-RateLimit-Remaining"), ",")
	if len(parts) < 2 {
		return 0, false, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || n < 0 {
		return 0, false, false
	}
	if limits := strings.Split(h.Get("X-RateLimit-Limit"), ","); len(limits) >= 2 && strings.TrimSpace(limits[1]) == "0" {
		return 0, true, true
	}
	return n, false, true
}
func (p *Provider) Available() error {
	if strings.TrimSpace(p.key) == "" {
		return fmt.Errorf("Brave 未配置凭据")
	}
	return nil
}

type response struct {
	Web struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			Description   string   `json:"description"`
			ExtraSnippets []string `json:"extra_snippets"`
		} `json:"results"`
	} `json:"web"`
}

var freshness = map[string]string{"day": "pd", "week": "pw", "month": "pm", "year": "py"}

func (p *Provider) Search(ctx context.Context, q web.ProviderSearchRequest) (web.ProviderSearchResult, error) {
	out := web.ProviderSearchResult{Provider: p.ID(), Usage: web.PaidUsage(0)}
	v := url.Values{"q": {q.Query}, "count": {strconv.Itoa(min(max(q.MaxResults, 1), 20))}}
	if f, ok := freshness[q.TimeRange]; ok {
		v.Set("freshness", f)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/res/v1/web/search?"+v.Encode(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("X-Subscription-Token", p.key)
	req.Header.Set("Accept", "application/json")
	res, err := p.do(ctx, p.client, req)
	if err != nil {
		return out, err
	}
	if remaining, unlimited, ok := monthlyRemaining(res.Header); ok {
		p.bal.Set(p.ID(), remaining, unlimited)
		if res.StatusCode == http.StatusTooManyRequests && remaining == 0 && !unlimited {
			res.Body.Close()
			return out, &web.ExhaustedError{Provider: "Brave"}
		}
	}
	data, cache, err := web.ReadBody(ctx, res, "Brave")
	if err != nil {
		if web.IsExhausted(err) {
			p.bal.MarkExhausted(p.ID())
		}
		return out, err
	}
	var r response
	if err = json.Unmarshal(data, &r); err != nil {
		return out, fmt.Errorf("Brave 响应格式错误")
	}
	out.Cacheable = cache
	for _, x := range r.Web.Results {
		out.Sources = append(out.Sources, web.Source{URL: x.URL, Title: x.Title, Snippet: x.Description})
	}
	out.Usage = web.PaidUsage(len(out.Sources))
	return out, nil
}
