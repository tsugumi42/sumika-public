// Package searxng 是自配 SearXNG 实例的搜索提供方（GET <url>/search?format=json），不要 key。
package searxng

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sumika-native/internal/web"
)

type Provider struct {
	base   string
	client *http.Client
	do     func(context.Context, *http.Client, *http.Request) (*http.Response, error)
}

// New 的 base 是实例地址，如 https://searx.example.org；末尾的 / 和 /search 会被去掉。
func New(base string, client *http.Client) *Provider {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/search")
	return &Provider{base: base, client: web.NoRedirectClient(client), do: web.DoHTTP}
}
func (*Provider) ID() string { return "searxng" }
func (p *Provider) Available() error {
	u, err := url.Parse(p.base)
	if p.base == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("SearXNG 未配置实例地址 web.searxng.url")
	}
	return nil
}

type response struct {
	Results []struct {
		URL           string `json:"url"`
		Title         string `json:"title"`
		Content       string `json:"content"`
		PublishedDate string `json:"publishedDate"`
	} `json:"results"`
}

var ranges = map[string]string{"day": "day", "week": "month", "month": "month", "year": "year"}

func (p *Provider) Search(ctx context.Context, q web.ProviderSearchRequest) (web.ProviderSearchResult, error) {
	out := web.ProviderSearchResult{Provider: p.ID(), Usage: web.FreeUsage(0)}
	v := url.Values{"q": {q.Query}, "format": {"json"}}
	if r, ok := ranges[q.TimeRange]; ok {
		v.Set("time_range", r)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/search?"+v.Encode(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := p.do(ctx, p.client, req)
	if err != nil {
		return out, err
	}
	if res.StatusCode == http.StatusForbidden {
		res.Body.Close()
		return out, fmt.Errorf("SearXNG 拒绝了请求（HTTP 403）：实例的 settings.yml 需在 search.formats 里启用 json")
	}
	data, cache, err := web.ReadBody(ctx, res, "SearXNG")
	if err != nil {
		return out, err
	}
	var r response
	if err = json.Unmarshal(data, &r); err != nil {
		return out, fmt.Errorf("SearXNG 响应格式错误")
	}
	out.Cacheable = cache
	for _, x := range r.Results {
		s := web.Source{URL: x.URL, Title: x.Title, Snippet: x.Content}
		if t, err := time.Parse(time.RFC3339, x.PublishedDate); err == nil {
			s.PublishedAt = &t
		}
		out.Sources = append(out.Sources, s)
	}
	out.Usage = web.FreeUsage(len(out.Sources))
	return out, nil
}
