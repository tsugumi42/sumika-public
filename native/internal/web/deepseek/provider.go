// Package deepseek 接 DeepSeek 官方的搜索和抓网页接口（https://api-docs.deepseek.com/guides/search/）。
// 同一把 key 同时做搜索和抓网页，按成功请求计费；失败、空结果、超时不计费。
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"sumika-native/internal/web"
)

type Provider struct {
	key    string
	client *http.Client
	base   string
	bal    *web.Balances
	now    func() time.Time
	do     func(context.Context, *http.Client, *http.Request) (*http.Response, error)
}

func New(key string, client *http.Client) *Provider {
	return &Provider{key: key, client: web.NoRedirectClient(client), base: "https://api.deepseek.com", now: time.Now, do: web.DoHTTP}
}

func (*Provider) ID() string { return "deepseek" }

// SetBalances 接上余额记录。DeepSeek 没有“剩余请求数”接口（/user/balance 给的是钱，且文档没说搜索是否走它），
// 所以只在收到 402 时记“用完”。
func (p *Provider) SetBalances(b *web.Balances) { p.bal = b }

func (p *Provider) Available() error {
	if strings.TrimSpace(p.key) == "" {
		return fmt.Errorf("DeepSeek 未配置凭据")
	}
	return nil
}

// 官方：query 1～128 个字符。
const maxQueryRunes = 128

type searchResponse struct {
	Results []struct {
		Title     string  `json:"title"`
		URL       string  `json:"url"`
		Snippet   string  `json:"snippet"`
		Summary   string  `json:"summary"`
		Content   string  `json:"content"`
		SiteName  string  `json:"site_name"`
		Date      *string `json:"date"`
		Authority int     `json:"authority"`
	} `json:"results"`
}

type fetchResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

func (p *Provider) post(ctx context.Context, path string, payload, out any) (bool, error) {
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
	// 402 余额不足 → 用完；429 是限流（QPS），不是用完，当普通失败，交给调用方换一家或稍后再试。
	data, cacheable, err := web.ReadBody(ctx, res, "DeepSeek")
	if err != nil {
		if web.IsExhausted(err) {
			p.bal.MarkExhausted(p.ID())
			return false, &web.ExhaustedError{Provider: "DeepSeek"}
		}
		return false, err
	}
	if err = json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("DeepSeek 响应格式错误")
	}
	return cacheable, nil
}

// afterDate 把 time_range 译成官方查询语法里的 after:YYYY-MM-DD。
func (p *Provider) afterDate(rng string) string {
	var days int
	switch rng {
	case "day":
		days = 1
	case "week":
		days = 7
	case "month":
		days = 31
	case "year":
		days = 366
	default:
		return ""
	}
	return p.now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
}

func (p *Provider) Search(ctx context.Context, q web.ProviderSearchRequest) (web.ProviderSearchResult, error) {
	query := strings.TrimSpace(q.Query)
	if after := p.afterDate(q.TimeRange); after != "" {
		if with := query + " after:" + after; utf8.RuneCountInString(with) <= maxQueryRunes {
			query = with
		}
	}
	if utf8.RuneCountInString(query) > maxQueryRunes {
		query = string([]rune(query)[:maxQueryRunes])
	}
	var r searchResponse
	cache, err := p.post(ctx, "/v1/tools/search", map[string]any{"query": query, "count": q.MaxResults, "need_content": false}, &r)
	out := web.ProviderSearchResult{Provider: p.ID(), Cacheable: cache}
	if err != nil {
		return out, err
	}
	out.Usage = web.FreeUsage(0) // 空结果不计费
	if len(r.Results) > 0 {
		out.Usage = web.PaidUsage(len(r.Results)) // 只有 200 且结果非空才计费
	}
	for _, x := range r.Results {
		snippet := x.Summary // summary 是给模型读的较长版本，snippet 是给界面的短版本
		if strings.TrimSpace(snippet) == "" {
			snippet = x.Snippet
		}
		s := web.Source{URL: x.URL, Title: x.Title, Snippet: snippet, SiteName: x.SiteName}
		if x.Date != nil {
			if t, e := time.Parse(time.RFC3339, *x.Date); e == nil {
				s.PublishedAt = &t
			}
		}
		out.Sources = append(out.Sources, s)
	}
	return out, nil
}

func (p *Provider) Fetch(ctx context.Context, q web.FetchRequest) (web.ProviderFetchResult, error) {
	var r fetchResponse
	cache, err := p.post(ctx, "/v1/tools/fetch", map[string]any{"url": q.URL}, &r)
	out := web.ProviderFetchResult{Result: web.FetchResult{Provider: p.ID(), RequestedURL: q.URL, ReturnedURL: q.URL, ContentKind: "markdown"}, Cacheable: cache}
	if err != nil {
		return out, err
	}
	if strings.TrimSpace(r.Content) == "" {
		out.Result.Usage = web.FreeUsage(0)
		out.Result.CompletenessKnown = true
		return out, nil
	}
	out.Result.Usage = web.PaidUsage(1)
	out.Result.Title = r.Title
	out.Result.Content = r.Content
	out.Result.CompletenessKnown = true
	if runes := []rune(r.Content); len(runes) > 12000 {
		out.Result.Content = string(runes[:12000])
		out.Result.Truncated = true
		out.Result.TruncationReason = "model_limit"
	}
	return out, nil
}
