package web

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

type Options struct {
	Enabled                                                                                     bool
	Timeout                                                                                     time.Duration
	Policy                                                                                      URLPolicy
	Journal                                                                                     Journal
	DailySearch, DailyFetch, MonthlyMicroCredits                                                *int64
	Lifetime                                                                                    kernel.Lifetime
	Timezone                                                                                    string
	MaxInflight, MaxInflightPerTask, MaxQueriesPerTask, MaxFetchesPerTask, MaxDispatchesPerTask int
	SearchCacheSeconds, FetchCacheSeconds, CacheBytes, CacheEntries                             int
	ResponseMaxBytes                                                                            int
}
type cacheValue struct {
	key   string
	value any
	size  int
	until time.Time
}
type rootState struct{ slot chan struct{} }
type Service struct {
	opts       Options
	search     SearchProvider
	fetch      FetchProvider
	wiki       WikiProvider
	zone       *time.Location
	mu         sync.Mutex
	roots      map[string]*rootState
	slot       chan struct{}
	cache      map[string]*list.Element
	lru        *list.List
	cacheBytes int
	flights    map[string]*flight
	closed     bool
}

func New(o Options, search SearchProvider, fetch FetchProvider) (*Service, error) {
	for _, limit := range []struct {
		value             *int
		fallback, maximum int
	}{{&o.ResponseMaxBytes, 5 << 20, 5 << 20}, {&o.MaxInflight, 4, 8}, {&o.MaxInflightPerTask, 2, 2}, {&o.MaxQueriesPerTask, 4, 4}, {&o.MaxFetchesPerTask, 8, 8}, {&o.MaxDispatchesPerTask, 12, 12}, {&o.SearchCacheSeconds, 300, 300}, {&o.FetchCacheSeconds, 600, 600}, {&o.CacheBytes, 16 << 20, 16 << 20}, {&o.CacheEntries, 256, 256}} {
		if *limit.value == 0 {
			*limit.value = limit.fallback
		}
		if *limit.value < 1 || *limit.value > limit.maximum {
			return nil, fmt.Errorf("web 资源限额越界")
		}
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.Timeout <= 0 || o.Timeout > 60*time.Second {
		return nil, fmt.Errorf("web 时限越界")
	}
	if o.Timezone == "" {
		o.Timezone = "Asia/Shanghai"
	}
	zone, err := time.LoadLocation(o.Timezone)
	if err != nil {
		return nil, err
	}
	if o.Enabled {
		if o.Journal == nil {
			return nil, fmt.Errorf("web 缺记账端口")
		}
		// 三项本地额度都是可选的：nil 表示不限；写了照办（0 仍表示禁止）。
		for _, limit := range []*int64{o.DailySearch, o.DailyFetch, o.MonthlyMicroCredits} {
			if limit != nil && *limit < 0 {
				return nil, fmt.Errorf("web 额度不能为负")
			}
		}
		if err := search.Available(); err != nil {
			return nil, err
		}
		if err := fetch.Available(); err != nil {
			return nil, err
		}
	}
	return &Service{opts: o, search: search, fetch: fetch, zone: zone, roots: map[string]*rootState{}, slot: make(chan struct{}, o.MaxInflight), cache: map[string]*list.Element{}, lru: list.New()}, nil
}
func (s *Service) begin(ctx context.Context, a Access, kind string, n int) (context.Context, func(), error) {
	if !s.opts.Enabled {
		return nil, nil, ecosystem.ErrDenied
	}
	if err := a.grant.Check(ctx, "web:"+kind); err != nil {
		return nil, nil, err
	}
	logicalLimit := int64(s.opts.MaxFetchesPerTask)
	if kind == "search" {
		logicalLimit = int64(s.opts.MaxQueriesPerTask)
	}
	if err := s.opts.Journal.WebConsume(ctx, []store.WebQuota{{Dimension: "logical:" + kind + ":" + a.inv.RootID, Window: "root", Limit: logicalLimit, Amount: int64(n)}}); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	return ctx, cancel, nil
}
func (s *Service) get(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.cache[key]
	if e == nil {
		return nil, false
	}
	v := e.Value.(cacheValue)
	if time.Now().After(v.until) {
		s.remove(e)
		return nil, false
	}
	s.lru.MoveToFront(e)
	return v.value, true
}
func (s *Service) remove(e *list.Element) {
	v := e.Value.(cacheValue)
	delete(s.cache, v.key)
	s.cacheBytes -= v.size
	s.lru.Remove(e)
}
func (s *Service) put(key string, value any, ttl time.Duration) {
	b, _ := json.Marshal(value)
	if len(b) > s.opts.CacheBytes {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.cache[key]; old != nil {
		s.remove(old)
	}
	for len(s.cache) >= s.opts.CacheEntries || s.cacheBytes+len(b) > s.opts.CacheBytes {
		s.remove(s.lru.Back())
	}
	s.cache[key] = s.lru.PushFront(cacheValue{key, value, len(b), time.Now().Add(ttl)})
	s.cacheBytes += len(b)
}
func (s *Service) Search(ctx context.Context, a Access, q SearchRequest) (SearchResult, error) {
	if len(q.Queries) < 1 || len(q.Queries) > 4 {
		return SearchResult{}, fmt.Errorf("queries 需要 1～4 项")
	}
	if q.MaxResults == 0 {
		q.MaxResults = 5
	}
	if q.MaxResults < 1 || q.MaxResults > 10 {
		return SearchResult{}, fmt.Errorf("max_results 越界")
	}
	if q.TimeRange != "" && q.TimeRange != "day" && q.TimeRange != "week" && q.TimeRange != "month" && q.TimeRange != "year" {
		return SearchResult{}, fmt.Errorf("time_range 非法")
	}
	seen := map[string]bool{}
	queries := make([]string, 0, len(q.Queries))
	for _, text := range q.Queries {
		text = strings.TrimSpace(text)
		if text == "" || utf8.RuneCountInString(text) > 512 {
			return SearchResult{}, fmt.Errorf("查询为空或过长")
		}
		if !seen[text] {
			queries = append(queries, text)
			seen[text] = true
		}
	}
	q.Queries = queries
	ctx, done, err := s.begin(ctx, a, "search", len(queries))
	if err != nil {
		return SearchResult{}, err
	}
	defer done()
	key := ecosystem.Digest([]any{a.grant.Key(), s.search.ID(), q})
	if v, ok := s.get(key); ok {
		out := v.(SearchResult)
		out.Cached = true
		out.Usage = Usage{}
		return out, nil
	}
	value, err := s.shared(ctx, a, key, func(sharedCtx context.Context) (any, error) { return s.searchOnce(sharedCtx, a, q, key) })
	if err != nil {
		return SearchResult{}, err
	}
	return value.(SearchResult), nil
}
func (s *Service) searchOnce(ctx context.Context, a Access, q SearchRequest, key string) (SearchResult, error) {
	queries := q.Queries
	d := &dispatchState{s: s, a: a, kind: "search"}
	ctx = context.WithValue(ctx, dispatchKey{}, d)
	out := SearchResult{Provider: s.search.ID(), FetchedAt: time.Now()}
	cacheable := true
	usageComplete := true
	urls := map[string]bool{}
	batches := make([][]Source, 0, len(queries))
	for _, text := range queries {
		res, callErr := s.search.Search(ctx, ProviderSearchRequest{text, q.MaxResults, q.TimeRange})
		if callErr != nil {
			settleErr := d.settle(ctx, Usage{})
			if settleErr != nil {
				return out, settleErr
			}
			return out, callErr
		}
		out.Usage.UpstreamRequests += len(d.ids)
		if res.Provider != "" {
			out.Provider = res.Provider
		}
		if err := d.settle(ctx, res.Usage); err != nil {
			return out, err
		}
		if res.Usage.MicroCredits != nil {
			if out.Usage.MicroCredits == nil {
				v := int64(0)
				out.Usage.MicroCredits = &v
			}
			*out.Usage.MicroCredits += *res.Usage.MicroCredits
		}
		if res.Usage.MicroCredits == nil {
			usageComplete = false
		}
		cacheable = cacheable && res.Cacheable
		out.Truncated = out.Truncated || res.Truncated
		batch := make([]Source, 0, min(len(res.Sources), q.MaxResults))
		for index, source := range res.Sources {
			if index >= q.MaxResults {
				out.Truncated = true
				break
			}
			if _, err := s.opts.Policy.Check(ctx, source.URL); err != nil {
				continue
			}
			if utf8.RuneCountInString(source.Snippet) > 1000 {
				source.Snippet = string([]rune(source.Snippet)[:1000])
				out.Truncated = true
			}
			batch = append(batch, source)
		}
		batches = append(batches, batch)
	}
	for rank := 0; rank < q.MaxResults; rank++ {
		for _, batch := range batches {
			if rank >= len(batch) {
				continue
			}
			source := batch[rank]
			if urls[source.URL] {
				continue
			}
			urls[source.URL] = true
			if len(out.Sources) == q.MaxResults {
				out.Truncated = true
				continue
			}
			out.Sources = append(out.Sources, source)
		}
	}
	if !usageComplete {
		out.Usage.MicroCredits = nil
	}
	if err := a.grant.Check(ctx, "web:search"); err != nil {
		return SearchResult{}, err
	}
	if cacheable {
		s.put(key, out, time.Duration(s.opts.SearchCacheSeconds)*time.Second)
	}
	return out, nil
}
func (s *Service) Fetch(ctx context.Context, a Access, q FetchRequest) (FetchResult, error) {
	if q.Format == "" {
		q.Format = "markdown"
	}
	if q.Format != "text" && q.Format != "markdown" {
		return FetchResult{}, fmt.Errorf("format 非法")
	}
	ctx, done, err := s.begin(ctx, a, "fetch", 1)
	if err != nil {
		return FetchResult{}, err
	}
	defer done()
	if _, err := s.opts.Policy.Check(ctx, q.URL); err != nil {
		return FetchResult{}, err
	}
	key := ecosystem.Digest([]any{a.grant.Key(), s.fetch.ID(), q})
	if v, ok := s.get(key); ok {
		out := v.(FetchResult)
		out.Cached = true
		out.Usage = Usage{}
		return out, nil
	}
	value, err := s.shared(ctx, a, key, func(sharedCtx context.Context) (any, error) { return s.fetchOnce(sharedCtx, a, q, key) })
	if err != nil {
		return FetchResult{}, err
	}
	return value.(FetchResult), nil
}
func (s *Service) fetchOnce(ctx context.Context, a Access, q FetchRequest, key string) (FetchResult, error) {
	d := &dispatchState{s: s, a: a, kind: "fetch"}
	ctx = context.WithValue(ctx, dispatchKey{}, d)
	res, err := s.fetch.Fetch(ctx, q)
	if settleErr := d.settle(ctx, res.Result.Usage); settleErr != nil {
		return res.Result, settleErr
	}
	if err != nil {
		return res.Result, err
	}
	if err := a.grant.Check(ctx, "web:fetch"); err != nil {
		return FetchResult{}, err
	}
	res.Result.FetchedAt = time.Now()
	if res.Cacheable {
		s.put(key, res.Result, time.Duration(s.opts.FetchCacheSeconds)*time.Second)
	}
	return res.Result, nil
}
func (s *Service) ForgetRoot(id string) { s.mu.Lock(); delete(s.roots, id); s.mu.Unlock() }
