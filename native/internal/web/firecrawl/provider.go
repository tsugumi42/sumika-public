// Package firecrawl 是 Firecrawl 搜索和网页读取提供方（v2：/search、/scrape，Bearer）。
package firecrawl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	return &Provider{key: key, client: web.NoRedirectClient(client), base: "https://api.firecrawl.dev", do: web.DoHTTP}
}
func (*Provider) ID() string { return "firecrawl" }

// SetBalances 接上余额记录。
func (p *Provider) SetBalances(b *web.Balances) { p.bal = b }

// FetchBalance 查团队剩余 credits：GET /v2/team/credit-usage，字段 data.remainingCredits。
func (p *Provider) FetchBalance(ctx context.Context) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/v2/team/credit-usage", nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	res, err := p.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("Firecrawl 余额查询 HTTP %d", res.StatusCode)
	}
	var r struct {
		Success bool `json:"success"`
		Data    struct {
			RemainingCredits *int64 `json:"remainingCredits"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r); err != nil || r.Data.RemainingCredits == nil {
		return 0, false, fmt.Errorf("Firecrawl 余额响应格式错误")
	}
	return *r.Data.RemainingCredits, false, nil
}
func (p *Provider) Available() error {
	if strings.TrimSpace(p.key) == "" {
		return fmt.Errorf("Firecrawl 未配置凭据")
	}
	return nil
}

func (p *Provider) post(ctx context.Context, path string, payload any, out any) (bool, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+path, bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.do(ctx, p.client, req)
	if err != nil {
		return false, err
	}
	data, cache, err := web.ReadBody(ctx, res, "Firecrawl")
	if err != nil {
		if web.IsExhausted(err) {
			p.bal.MarkExhausted(p.ID())
		}
		return false, err
	}
	if err = json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("Firecrawl 响应格式错误")
	}
	return cache, nil
}

var tbs = map[string]string{"day": "qdr:d", "week": "qdr:w", "month": "qdr:m", "year": "qdr:y"}

func (p *Provider) Search(ctx context.Context, q web.ProviderSearchRequest) (web.ProviderSearchResult, error) {
	out := web.ProviderSearchResult{Provider: p.ID(), Usage: web.PaidUsage(0)}
	body := map[string]any{"query": q.Query, "limit": q.MaxResults}
	if t, ok := tbs[q.TimeRange]; ok {
		body["tbs"] = t
	}
	var r struct {
		Success bool   `json:"success"`
		ID      string `json:"id"`
		Data    struct {
			Web []struct{ Title, URL, Description string } `json:"web"`
		} `json:"data"`
	}
	cache, err := p.post(ctx, "/v2/search", body, &r)
	if err != nil {
		return out, err
	}
	if !r.Success {
		return out, fmt.Errorf("Firecrawl 搜索失败")
	}
	out.RequestID, out.Cacheable = r.ID, cache
	for _, x := range r.Data.Web {
		out.Sources = append(out.Sources, web.Source{URL: x.URL, Title: x.Title, Snippet: x.Description})
	}
	out.Usage = web.PaidUsage(len(out.Sources))
	return out, nil
}

func (p *Provider) Fetch(ctx context.Context, q web.FetchRequest) (web.ProviderFetchResult, error) {
	out := web.ProviderFetchResult{Result: web.FetchResult{Provider: p.ID(), RequestedURL: q.URL, ContentKind: q.Format, Usage: web.PaidUsage(0)}}
	var r struct {
		Success bool `json:"success"`
		Data    struct {
			Markdown string `json:"markdown"`
			Metadata struct {
				URL        string `json:"url"`
				StatusCode *int   `json:"statusCode"`
			} `json:"metadata"`
		} `json:"data"`
	}
	cache, err := p.post(ctx, "/v2/scrape", map[string]any{"url": q.URL, "formats": []string{"markdown"}, "onlyMainContent": true, "timeout": 20000}, &r)
	out.Cacheable = cache
	if err != nil {
		return out, err
	}
	if !r.Success || r.Data.Markdown == "" {
		return out, fmt.Errorf("Firecrawl 读取失败")
	}
	out.Result.Usage = web.PaidUsage(1)
	out.Result.ReturnedURL = q.URL
	if u := r.Data.Metadata.URL; u != "" {
		out.Result.ReturnedURL = u
		out.Result.FinalURL = &u
	}
	out.Result.OriginStatus = r.Data.Metadata.StatusCode
	out.Result.Content = r.Data.Markdown
	return out, nil
}
