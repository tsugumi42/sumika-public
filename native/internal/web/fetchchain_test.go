package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeFetch struct {
	id      string
	content string
	err     error
	noKey   bool
	calls   int
}

func (f *fakeFetch) ID() string { return f.id }
func (f *fakeFetch) Available() error {
	if f.noKey {
		return fmt.Errorf("没有 key")
	}
	return nil
}
func (f *fakeFetch) Fetch(_ context.Context, q FetchRequest) (ProviderFetchResult, error) {
	f.calls++
	return ProviderFetchResult{Result: FetchResult{Provider: f.id, RequestedURL: q.URL, Content: f.content, Usage: Usage{UpstreamRequests: 1}}}, f.err
}

func chainOf(order []string, ps ...*fakeFetch) (*FetchChain, *Order) {
	all := map[string]FetchProvider{}
	for _, p := range ps {
		all[p.id] = p
	}
	o := NewOrder(nil, order)
	return NewFetchChain(all, o, NewBalances()), o
}

func TestFetchChainFallsThroughFailures(t *testing.T) {
	long := strings.Repeat("正文", 800)
	for _, tc := range []struct {
		name  string
		first *fakeFetch
	}{
		{"报错", &fakeFetch{id: "http", err: errors.New("WEB_HTTP_STATUS_403")}},
		{"内容为空", &fakeFetch{id: "http", content: "  \n"}},
		{"人机校验页", &fakeFetch{id: "http", content: "Just a moment... 请完成安全验证"}},
	} {
		second := &fakeFetch{id: "deepseek", content: long}
		c, _ := chainOf([]string{"http", "deepseek"}, tc.first, second)
		res, err := c.Fetch(context.Background(), FetchRequest{URL: "https://x.example"})
		if err != nil || res.Result.Provider != "deepseek" || tc.first.calls != 1 || second.calls != 1 {
			t.Errorf("%s：应换到第二家：%+v %v", tc.name, res.Result, err)
		}
		if res.Result.Usage.UpstreamRequests != 2 {
			t.Errorf("%s：出网次数应累加：%d", tc.name, res.Result.Usage.UpstreamRequests)
		}
	}
}

func TestFetchChainStopsAtFirstSuccessAndSkipsNoKey(t *testing.T) {
	long := strings.Repeat("正文", 800)
	free := &fakeFetch{id: "http", content: long}
	paid := &fakeFetch{id: "deepseek", content: long}
	c, _ := chainOf([]string{"http", "deepseek"}, free, paid)
	if res, err := c.Fetch(context.Background(), FetchRequest{URL: "https://x.example"}); err != nil || res.Result.Provider != "http" || paid.calls != 0 {
		t.Fatalf("成功就不该再试后面的：%+v %v", res.Result, err)
	}
	nokey := &fakeFetch{id: "deepseek", noKey: true, content: long}
	ok := &fakeFetch{id: "tavily", content: long}
	c, _ = chainOf([]string{"deepseek", "tavily"}, nokey, ok)
	if res, err := c.Fetch(context.Background(), FetchRequest{URL: "https://x.example"}); err != nil || res.Result.Provider != "tavily" || nokey.calls != 0 {
		t.Fatalf("没 key 的家应跳过：%+v %v", res.Result, err)
	}
}

func TestFetchChainAllFailReturnsLastError(t *testing.T) {
	a := &fakeFetch{id: "http", err: errors.New("WEB_HTTP_STATUS_403")}
	b := &fakeFetch{id: "deepseek", err: errors.New("DeepSeek HTTP 404")}
	c, _ := chainOf([]string{"http", "deepseek"}, a, b)
	_, err := c.Fetch(context.Background(), FetchRequest{URL: "https://x.example"})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("应返回最后一个错误：%v", err)
	}
	empty, _ := chainOf(nil)
	if empty.Available() == nil {
		t.Fatal("没有任何可用的家，Available 该报错")
	}
}

func TestFetchChainOrderChangesLive(t *testing.T) {
	long := strings.Repeat("正文", 800)
	a := &fakeFetch{id: "http", content: long}
	b := &fakeFetch{id: "deepseek", content: long}
	c, order := chainOf([]string{"http", "deepseek"}, a, b)
	if c.ID() != "http" {
		t.Fatalf("起始第一家：%s", c.ID())
	}
	order.SetFetch([]string{"deepseek", "http"})
	res, err := c.Fetch(context.Background(), FetchRequest{URL: "https://x.example"})
	if err != nil || res.Result.Provider != "deepseek" || c.ID() != "deepseek" {
		t.Fatalf("改顺序应立即生效：%+v %v", res.Result, err)
	}
}

func TestOrderedSearchFollowsLiveOrder(t *testing.T) {
	a := &fakeSearch{id: "tavily"}
	b := &fakeSearch{id: "duckduckgo"}
	order := NewOrder([]string{"tavily", "duckduckgo"}, nil)
	s := NewOrderedSearch(map[string]SearchProvider{"tavily": a, "duckduckgo": b}, order, NewBalances())
	if res, err := s.Search(context.Background(), ProviderSearchRequest{Query: "q"}); err != nil || res.Provider != "tavily" {
		t.Fatalf("起始第一家：%+v %v", res, err)
	}
	order.SetSearch([]string{"duckduckgo", "tavily"})
	if res, err := s.Search(context.Background(), ProviderSearchRequest{Query: "q"}); err != nil || res.Provider != "duckduckgo" {
		t.Fatalf("改顺序应立即生效：%+v %v", res, err)
	}
}

func TestLooksBlocked(t *testing.T) {
	if !LooksBlocked("Checking your browser before accessing example.com") || !LooksBlocked("请完成安全验证后继续") {
		t.Error("校验页没认出来")
	}
	if LooksBlocked("") || LooksBlocked("一篇正常的短文，讲猫。") {
		t.Error("正常内容被误判")
	}
	if LooksBlocked(strings.Repeat("长文章里提到 captcha 这个词也不算。", 200)) {
		t.Error("长文不该当校验页")
	}
}
