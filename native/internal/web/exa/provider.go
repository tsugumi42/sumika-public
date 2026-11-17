// Package exa 是 Exa 搜索提供方（POST https://api.exa.ai/search，x-api-key）。
package exa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sumika-native/internal/web"
)

type Provider struct {
	key    string
	client *http.Client
	base   string
	now    func() time.Time
	bal    *web.Balances
	do     func(context.Context, *http.Client, *http.Request) (*http.Response, error)
}

func New(key string, client *http.Client) *Provider {
	return &Provider{key: key, client: web.NoRedirectClient(client), base: "https://api.exa.ai", now: time.Now, do: web.DoHTTP}
}
func (*Provider) ID() string { return "exa" }

// SetBalances 接上余额记录：Exa 没有余额接口，只在收到 402 时记“用完”。
func (p *Provider) SetBalances(b *web.Balances) { p.bal = b }
func (p *Provider) Available() error {
	if strings.TrimSpace(p.key) == "" {
		return fmt.Errorf("Exa 未配置凭据")
	}
	return nil
}

type response struct {
	RequestID string `json:"requestId"`
	Results   []struct {
		Title         string   `json:"title"`
		URL           string   `json:"url"`
		PublishedDate string   `json:"publishedDate"`
		Text          string   `json:"text"`
		Highlights    []string `json:"highlights"`
		Summary       string   `json:"summary"`
	} `json:"results"`
}

var ranges = map[string]time.Duration{"day": 24 * time.Hour, "week": 7 * 24 * time.Hour, "month": 31 * 24 * time.Hour, "year": 366 * 24 * time.Hour}

func (p *Provider) Search(ctx context.Context, q web.ProviderSearchRequest) (web.ProviderSearchResult, error) {
	out := web.ProviderSearchResult{Provider: p.ID(), Usage: web.PaidUsage(0)}
	body := map[string]any{"query": q.Query, "numResults": q.MaxResults, "type": "auto", "contents": map[string]any{"text": map[string]any{"maxCharacters": 1000}}}
	if d, ok := ranges[q.TimeRange]; ok {
		body["startPublishedDate"] = p.now().Add(-d).UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/search", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("x-api-key", p.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.do(ctx, p.client, req)
	if err != nil {
		return out, err
	}
	data, cache, err := web.ReadBody(ctx, res, "Exa")
	if err != nil {
		if web.IsExhausted(err) {
			p.bal.MarkExhausted(p.ID())
		}
		return out, err
	}
	var r response
	if err = json.Unmarshal(data, &r); err != nil {
		return out, fmt.Errorf("Exa 响应格式错误")
	}
	out.RequestID, out.Cacheable = r.RequestID, cache
	for _, x := range r.Results {
		snippet := x.Text
		if snippet == "" {
			snippet = strings.Join(x.Highlights, " ")
		}
		if snippet == "" {
			snippet = x.Summary
		}
		s := web.Source{URL: x.URL, Title: x.Title, Snippet: snippet}
		if t, err := time.Parse(time.RFC3339, x.PublishedDate); err == nil {
			s.PublishedAt = &t
		}
		out.Sources = append(out.Sources, s)
	}
	out.Usage = web.PaidUsage(len(out.Sources))
	return out, nil
}
