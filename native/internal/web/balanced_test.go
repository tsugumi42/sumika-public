package web

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type fakeSearch struct {
	id        string
	err       error
	searches  int
	balance   int64
	unlimited bool
	fetchErr  error
	fetches   int
	source    bool
}

func (f *fakeSearch) ID() string       { return f.id }
func (f *fakeSearch) Available() error { return nil }
func (f *fakeSearch) Search(context.Context, ProviderSearchRequest) (ProviderSearchResult, error) {
	f.searches++
	return ProviderSearchResult{Provider: f.id}, f.err
}

// fakeSource 在 fakeSearch 外再加余额查询。
type fakeSource struct{ *fakeSearch }

func (f fakeSource) FetchBalance(context.Context) (int64, bool, error) {
	f.fetches++
	return f.balance, f.unlimited, f.fetchErr
}

func TestAutoSkipsExhaustedAndCachesBalance(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	bal := NewBalances()
	bal.now = func() time.Time { return now }
	empty := &fakeSearch{id: "tavily", balance: 0}
	next := &fakeSearch{id: "duckduckgo"}
	s := NewBalancedSearch([]SearchProvider{fakeSource{empty}, next}, bal, true)
	for range 3 {
		res, err := s.Search(context.Background(), ProviderSearchRequest{Query: "q"})
		if err != nil || res.Provider != "duckduckgo" {
			t.Fatalf("应换到下一家: %+v %v", res, err)
		}
	}
	if empty.searches != 0 || next.searches != 3 {
		t.Fatalf("用完的家不该被搜: %d %d", empty.searches, next.searches)
	}
	if empty.fetches != 1 {
		t.Fatalf("缓存期内只该查一次余额，实际 %d 次", empty.fetches)
	}
	b, ok := bal.Get("tavily")
	if !ok || b.Remaining != 0 || !b.At.Equal(now) {
		t.Fatalf("余额记录: %+v", b)
	}
	// 缓存期内不重查；过期后重查，余额恢复就用回这一家。
	now = now.Add(BalanceRefresh + time.Second)
	empty.balance = 5
	if s.ID() != "tavily" {
		t.Fatal("记录过期后不再当作用完（ID 不联网，重查在下一次 Search）")
	}
	res, err := s.Search(context.Background(), ProviderSearchRequest{Query: "q"})
	if err != nil || res.Provider != "tavily" {
		t.Fatalf("充值后应回到第一家: %+v %v", res, err)
	}
	if b, _ := bal.Get("tavily"); b.Remaining != 4 {
		t.Fatalf("成功后本地估计应扣 1：%+v", b)
	}
}

func TestExplicitExhaustedReportsClearError(t *testing.T) {
	bal := NewBalances()
	only := &fakeSearch{id: "firecrawl", balance: 0}
	s := NewBalancedSearch([]SearchProvider{fakeSource{only}}, bal, false)
	_, err := s.Search(context.Background(), ProviderSearchRequest{Query: "q"})
	if !IsExhausted(err) || err.Error() != "Firecrawl 额度用完了" {
		t.Fatalf("应报额度用完: %v", err)
	}
	if only.searches != 0 {
		t.Fatal("用完后不该出网")
	}
}

func TestBalanceQueryFailureIsUnknownNotExhausted(t *testing.T) {
	bal := NewBalances()
	flaky := &fakeSearch{id: "tavily", fetchErr: errors.New("网络不通")}
	s := NewBalancedSearch([]SearchProvider{fakeSource{flaky}}, bal, true)
	res, err := s.Search(context.Background(), ProviderSearchRequest{Query: "q"})
	if err != nil || res.Provider != "tavily" || flaky.searches != 1 {
		t.Fatalf("查余额失败应照常搜索: %+v %v", res, err)
	}
	if len(bal.Snapshot()) != 0 {
		t.Fatalf("失败不该留下已知余额: %v", bal.Snapshot())
	}
}

func TestMarkedExhaustedExpiresForNonRefreshable(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	bal := NewBalances()
	bal.now = func() time.Time { return now }
	exa := &fakeSearch{id: "exa"}
	next := &fakeSearch{id: "duckduckgo"}
	s := NewBalancedSearch([]SearchProvider{exa, next}, bal, true)
	bal.MarkExhausted("exa")
	if res, _ := s.Search(context.Background(), ProviderSearchRequest{Query: "q"}); res.Provider != "duckduckgo" {
		t.Fatalf("402 后应跳过: %+v", res)
	}
	now = now.Add(ExhaustedHold + time.Second)
	if res, _ := s.Search(context.Background(), ProviderSearchRequest{Query: "q"}); res.Provider != "exa" {
		t.Fatalf("一小时后应放一次试探: %+v", res)
	}
}

func TestBalanceLineAndHook(t *testing.T) {
	bal := NewBalances()
	var lines []string
	bal.OnUpdate = func(id string, b Balance) { lines = append(lines, BalanceLine(id, b)) }
	bal.Set("brave", 1990, false)
	bal.Set("tavily", 0, true)
	bal.Spend("brave", 2)
	want := fmt.Sprint([]string{"brave 剩余 1990", "tavily 剩余 不限", "brave 剩余 1988"})
	if fmt.Sprint(lines) != want {
		t.Fatalf("日志行：%v", lines)
	}
}
