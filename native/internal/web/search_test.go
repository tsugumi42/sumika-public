package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sumika-native/internal/web"
	"sumika-native/internal/web/tavily"
	"testing"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWebSearchBatchBounds(t *testing.T) {
	repo, grant, inv := webFixture(t)
	var queries []string
	provider := tavily.New("fixture-key", &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		queries = append(queries, request.Query)
		results := []map[string]string{}
		for rank := 0; rank < 3; rank++ {
			results = append(results, map[string]string{"url": fmt.Sprintf("https://example.com/%s/%d", request.Query, rank), "title": request.Query, "content": "fixture"})
		}
		body, err := json.Marshal(map[string]any{"results": results, "usage": map[string]int{"credits": 1}})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})})
	limit := int64(100)
	credits := int64(10 * 1_000_000)
	svc, err := web.New(web.Options{Enabled: true, Journal: repo, Lifetime: webLifetime{}, Policy: web.URLPolicy{Resolver: resolver{[]netip.Addr{netip.AddrFrom4([4]byte{93, 184, 216, 34})}}}, DailySearch: &limit, DailyFetch: &limit, MonthlyMicroCredits: &credits}, provider, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close(context.Background())
	access, err := web.NewAccess(grant, inv)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []web.SearchRequest{{Queries: []string{"fixture"}, MaxResults: 11}, {Queries: []string{strings.Repeat("汉", 513)}}} {
		if _, err = svc.Search(t.Context(), access, q); err == nil {
			t.Fatal("输入边界被放宽")
		}
	}
	if len(queries) != 0 {
		t.Fatal("坏参数已出网")
	}
	result, err := svc.Search(t.Context(), access, web.SearchRequest{Queries: []string{" first ", "first", "second"}, MaxResults: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[0] != "first" || queries[1] != "second" {
		t.Fatal("去空白和去重错误", queries)
	}
	expected := []string{"https://example.com/first/0", "https://example.com/second/0", "https://example.com/first/1"}
	if len(result.Sources) != len(expected) {
		t.Fatal("批次结果总限额错误", result)
	}
	for i, source := range result.Sources {
		if source.URL != expected[i] {
			t.Fatal("结果未按查询名次稳定交错", result.Sources)
		}
	}
	if !result.Truncated {
		t.Fatal("省略结果未标截断")
	}
	t.Logf("upstream_requests=%d result_sources=%d invalid_dispatches=0", len(queries), len(result.Sources))
}
