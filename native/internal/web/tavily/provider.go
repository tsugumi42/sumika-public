package tavily

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
}

func New(key string, client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Transport: http.DefaultTransport}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Provider{key: key, client: &copy, base: "https://api.tavily.com"}
}
func (*Provider) ID() string { return "tavily" }

// SetBalances 接上余额记录。
func (p *Provider) SetBalances(b *web.Balances) { p.bal = b }

// FetchBalance 查这把 key 的剩余额度：GET /usage，剩余 = key.limit - key.usage；key.limit 为 null 表示不限。
func (p *Provider) FetchBalance(ctx context.Context) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/usage", nil)
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
		return 0, false, fmt.Errorf("Tavily 余额查询 HTTP %d", res.StatusCode)
	}
	var r struct {
		Key struct {
			Usage *int64 `json:"usage"`
			Limit *int64 `json:"limit"`
		} `json:"key"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r); err != nil || r.Key.Usage == nil {
		return 0, false, fmt.Errorf("Tavily 余额响应格式错误")
	}
	if r.Key.Limit == nil {
		return 0, true, nil
	}
	return max(*r.Key.Limit-*r.Key.Usage, 0), false, nil
}
func (p *Provider) Available() error {
	if strings.TrimSpace(p.key) == "" {
		return fmt.Errorf("Tavily 未配置凭据")
	}
	return nil
}

type response struct {
	RequestID string `json:"request_id"`
	Results   []struct {
		URL, Title, Content, RawContent string
		PublishedDate                   string `json:"published_date"`
	} `json:"results"`
	Failed []struct{ URL, Error string } `json:"failed_results"`
	Usage  *struct {
		Credits *json.Number `json:"credits"`
	} `json:"usage"`
}

func (p *Provider) call(ctx context.Context, method string, payload any) (response, bool, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return response{}, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return response{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := web.DoHTTP(ctx, p.client, req)
	if err != nil {
		return response{}, false, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 402, 432, 433: // 402 付款要求；432、433 是 Tavily 的计划或按量额度超限（官方 usage 文档未列，按其错误码约定处理）
		p.bal.MarkExhausted(p.ID())
		return response{}, false, &web.ExhaustedError{Provider: "Tavily"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return response{}, false, fmt.Errorf("Tavily HTTP %d", res.StatusCode)
	}
	limit := web.ResponseLimit(ctx)
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return response{}, false, err
	}
	if int64(len(data)) > limit {
		return response{}, false, fmt.Errorf("Tavily 响应超过配置上限")
	}
	var out response
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&out); err != nil {
		return response{}, false, fmt.Errorf("Tavily 响应格式错误")
	}
	return out, !strings.Contains(strings.ToLower(res.Header.Get("Cache-Control")), "no-store"), nil
}
func usage(r response) web.Usage {
	u := web.Usage{UpstreamRequests: 1, SuccessfulURLs: len(r.Results)}
	if r.Usage != nil && r.Usage.Credits != nil {
		if n, err := r.Usage.Credits.Int64(); err == nil && n >= 0 && n < 1<<40 {
			v := n * 1000000
			u.MicroCredits = &v
		}
	}
	return u
}
func (p *Provider) Search(ctx context.Context, q web.ProviderSearchRequest) (web.ProviderSearchResult, error) {
	r, cache, err := p.call(ctx, "search", map[string]any{"query": q.Query, "max_results": q.MaxResults, "time_range": q.TimeRange, "search_depth": "basic", "topic": "general", "include_answer": false, "include_raw_content": false, "include_images": false, "include_usage": true})
	out := web.ProviderSearchResult{Provider: p.ID(), RequestID: r.RequestID, Usage: usage(r), Cacheable: cache}
	if err != nil {
		return out, err
	}
	for _, x := range r.Results {
		out.Sources = append(out.Sources, web.Source{URL: x.URL, Title: x.Title, Snippet: x.Content})
	}
	return out, nil
}
func (p *Provider) Fetch(ctx context.Context, q web.FetchRequest) (web.ProviderFetchResult, error) {
	r, cache, err := p.call(ctx, "extract", map[string]any{"urls": []string{q.URL}, "extract_depth": "basic", "format": q.Format, "include_images": false, "include_usage": true, "timeout": 20})
	out := web.ProviderFetchResult{Result: web.FetchResult{Provider: p.ID(), RequestID: r.RequestID, RequestedURL: q.URL, ContentKind: q.Format, Usage: usage(r)}, Cacheable: cache}
	if err != nil {
		return out, err
	}
	if len(r.Failed) > 0 || len(r.Results) != 1 {
		return out, fmt.Errorf("Tavily 提取失败")
	}
	x := r.Results[0]
	out.Result.ReturnedURL = x.URL
	out.Result.Content = x.RawContent
	return out, nil
}
