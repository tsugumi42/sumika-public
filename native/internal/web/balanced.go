package web

import (
	"context"
	"fmt"
	"time"
)

// BalancedSearch 把一组搜索提供方包成一个：每次搜索前按已知余额挑一家。
// auto 为真时按候选顺序跳过已用完的家；显式指定一家时（只有一个候选）用完直接报“额度用完了”。
// 余额查询带缓存（BalanceRefresh），不会每次搜索前多发一次请求；查询失败当作余额未知，照常搜索。
type BalancedSearch struct {
	candidates []SearchProvider
	// all、order 非空时候选每次按当前顺序现算（设置页改了顺序立刻生效）；只有 auto 用。
	all      map[string]SearchProvider
	order    *Order
	balances *Balances
	auto     bool
	// QueryTimeout 是一次余额查询的时限。
	QueryTimeout time.Duration
}

func NewBalancedSearch(candidates []SearchProvider, balances *Balances, auto bool) *BalancedSearch {
	return &BalancedSearch{candidates: candidates, balances: balances, auto: auto, QueryTimeout: 5 * time.Second}
}

// NewOrderedSearch 是 auto 模式：all 是所有构造好的服务，order 给先后（可随时改），key 没填的家自动跳过。
func NewOrderedSearch(all map[string]SearchProvider, order *Order, balances *Balances) *BalancedSearch {
	return &BalancedSearch{all: all, order: order, balances: balances, auto: true, QueryTimeout: 5 * time.Second}
}

// cands 返回此刻的候选，按顺序。
func (s *BalancedSearch) cands() []SearchProvider {
	if s.order == nil {
		return s.candidates
	}
	var out []SearchProvider
	for _, id := range s.order.Search() {
		if p := s.all[id]; p != nil {
			out = append(out, p)
		}
	}
	return out
}

func (s *BalancedSearch) Available() error {
	var first error
	for _, c := range s.cands() {
		err := c.Available()
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	if first == nil {
		first = fmt.Errorf("没有可用的 web 搜索提供方")
	}
	return first
}

// ID 不联网：用缓存里的余额挑出当前会用的那家，供缓存键和日志使用。
func (s *BalancedSearch) ID() string {
	for _, c := range s.cands() {
		if c.Available() != nil {
			continue
		}
		_, refreshable := c.(BalanceSource)
		if s.balances.Exhausted(c.ID(), refreshable) {
			continue
		}
		return c.ID()
	}
	if cs := s.cands(); len(cs) > 0 {
		return cs[len(cs)-1].ID()
	}
	return ""
}

func (s *BalancedSearch) refresh(ctx context.Context, c SearchProvider) {
	refreshBalance(ctx, s.balances, c.ID(), c, s.QueryTimeout)
}

// refreshBalance 在能查余额的服务记录过期时重查一次；p 不是 BalanceSource 就什么也不做。
func refreshBalance(ctx context.Context, b *Balances, id string, p any, timeout time.Duration) {
	src, ok := p.(BalanceSource)
	if !ok || !b.Stale(id) {
		return
	}
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	remaining, unlimited, err := src.FetchBalance(qctx)
	if err != nil {
		b.Fail(id)
		return
	}
	b.Set(id, remaining, unlimited)
}

func (s *BalancedSearch) pick(ctx context.Context) (SearchProvider, error) {
	var skipped SearchProvider
	for _, c := range s.cands() {
		if c.Available() != nil {
			continue
		}
		s.refresh(ctx, c)
		_, refreshable := c.(BalanceSource)
		if s.balances.Exhausted(c.ID(), refreshable) {
			skipped = c
			continue
		}
		return c, nil
	}
	if skipped != nil {
		return nil, &ExhaustedError{Provider: displayName(skipped.ID())}
	}
	return nil, s.Available()
}

func (s *BalancedSearch) Search(ctx context.Context, q ProviderSearchRequest) (ProviderSearchResult, error) {
	c, err := s.pick(ctx)
	if err != nil {
		return ProviderSearchResult{}, err
	}
	SetDispatchProvider(ctx, c.ID())
	res, err := c.Search(ctx, q)
	if err != nil {
		return res, err
	}
	if _, ok := c.(BalanceSource); ok {
		units := int64(1)
		if res.Usage.MicroCredits != nil && *res.Usage.MicroCredits >= 1_000_000 {
			units = *res.Usage.MicroCredits / 1_000_000
		}
		s.balances.Spend(c.ID(), units)
	}
	return res, nil
}

func displayName(id string) string {
	switch id {
	case "tavily":
		return "Tavily"
	case "exa":
		return "Exa"
	case "brave":
		return "Brave"
	case "firecrawl":
		return "Firecrawl"
	case "deepseek":
		return "DeepSeek"
	}
	return id
}
