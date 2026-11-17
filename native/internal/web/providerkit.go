package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Paid 说明提供方是否按次计费；计费的每次出网按 1 个额度单位记入每月额度，免费的记 0。
func Paid(id string) bool {
	switch id {
	case "tavily", "exa", "brave", "firecrawl", "deepseek":
		return true
	}
	return false
}

// PaidUsage 给按次计费的提供方：一次请求记 1 个额度单位（1e6 微额度）。
func PaidUsage(successfulURLs int) Usage {
	v := int64(1_000_000)
	return Usage{UpstreamRequests: 1, SuccessfulURLs: successfulURLs, MicroCredits: &v}
}

// FreeUsage 给不计费的提供方：额度记 0。
func FreeUsage(successfulURLs int) Usage {
	v := int64(0)
	return Usage{UpstreamRequests: 1, SuccessfulURLs: successfulURLs, MicroCredits: &v}
}

// NoRedirectClient 复制一份不跟随重定向的客户端；提供方的出网都经 DoHTTP。
func NoRedirectClient(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{Transport: http.DefaultTransport}
	}
	cp := *c
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// ReadBody 读响应体：非 2xx 报 "<name> HTTP <code>"，超过配置上限报错；返回是否允许缓存。
func ReadBody(ctx context.Context, res *http.Response, name string) ([]byte, bool, error) {
	defer res.Body.Close()
	if res.StatusCode == http.StatusPaymentRequired {
		return nil, false, &ExhaustedError{Provider: name}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, false, fmt.Errorf("%s HTTP %d", name, res.StatusCode)
	}
	limit := ResponseLimit(ctx)
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, false, fmt.Errorf("%s 响应超过配置上限", name)
	}
	return data, !strings.Contains(strings.ToLower(res.Header.Get("Cache-Control")), "no-store"), nil
}
