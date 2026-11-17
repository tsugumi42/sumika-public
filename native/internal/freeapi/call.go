package freeapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// 一次逻辑调用最多试几个提供方（规格-免费接口 第四节）
const maxAttempts = 3

// 调用整体的结局
var (
	// ErrNoProvider 是一个能用的提供方都没有：全被关了、需处理、本地上限到了或退避中（强试也没放行）
	ErrNoProvider = errors.New("暂时没有可用的接口")
	// ErrAllFailed 是试了，都没成
	ErrAllFailed = errors.New("接口都没成功")
)

// Env 是交给提供方函数的东西
type Env struct {
	Client   *http.Client
	Provider Provider
	URL      string // 注册表里的地址（测试可换）
}

// Fetcher 是某个提供方对某次请求的做法：发请求、解析、校验内容。查无结果返回 ErrNotFound，内容不对返回 Schema(...)
type Fetcher func(ctx context.Context, env Env) (any, error)

// Request 是一次逻辑调用
type Request struct {
	Category string
	Region   string
	Fetch    map[string]Fetcher // 提供方 id → 做法；没写的提供方不参与
	// CacheKey 非空才缓存和合并相同请求；键里可以有地点，只放内存
	CacheKey string
	CacheTTL time.Duration
}

// Result 是成功的结果
type Result struct {
	Value    any
	Provider Provider
	Cached   bool
	Attempts int
}

// Expiring 让结果自己说什么时候过期（比如汇率下次更新的时间），早于 CacheTTL 就用它
type Expiring interface{ ExpiresAt() time.Time }

// Attempt 是一次真的调提供方（插件日志用，Options.Trace）：URL 是真发出去的最后一个请求地址（没发出请求时是注册表里的地址）
type Attempt struct {
	Provider, Category string
	URL                string
	Took               time.Duration
	Kind               Kind
	Err                error
}

// CallError 带着失败的细节
type CallError struct {
	Base     error
	Attempts int
	Last     Kind
}

func (e *CallError) Error() string { return e.Base.Error() }
func (e *CallError) Unwrap() error { return e.Base }

const cacheMax = 200

// Do 做一次逻辑调用：缓存 → 合并 → 排序 → 依次试 → 记录
//
// 相同请求同时进来只发一次，领头的发，其余的等它的结果。领头的调用被取消或超时是它自己的事，
// 不是这次请求失败：这时等着的调用不拿它的错，回头重新来过（先看缓存，再看有没有别人接了领头，都没有就自己当领头），
// 只有真失败才算失败。
func (e *Engine) Do(ctx context.Context, req Request) (Result, error) {
	if req.CacheKey == "" {
		return e.doOnce(ctx, req)
	}
	key := req.Category + "\x00" + req.CacheKey
	for {
		e.mu.Lock()
		if c, ok := e.cache[key]; ok {
			if e.now().Before(c.exp) {
				e.mu.Unlock()
				return Result{Value: c.v, Provider: c.p, Cached: true}, nil
			}
			delete(e.cache, key)
		}
		f, ok := e.flights[key]
		if !ok {
			break
		}
		f.waiters++
		e.mu.Unlock()
		select {
		case <-f.done:
			if f.abandoned {
				if ctx.Err() != nil {
					return Result{}, ctx.Err()
				}
				continue
			}
			r := f.res
			r.Cached = true
			return r, f.err
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	e.flights[key] = f
	e.mu.Unlock()

	res, err := e.doOnce(ctx, req)

	e.mu.Lock()
	delete(e.flights, key)
	f.abandoned = err != nil && ctx.Err() != nil
	if err == nil && req.CacheTTL > 0 {
		exp := e.now().Add(req.CacheTTL)
		if x, ok := res.Value.(Expiring); ok && x.ExpiresAt().Before(exp) {
			exp = x.ExpiresAt()
		}
		if len(e.cache) >= cacheMax {
			for k, c := range e.cache { // 满了先清过期的，还满就清一个
				if !e.now().Before(c.exp) {
					delete(e.cache, k)
				}
			}
			for k := range e.cache {
				if len(e.cache) < cacheMax {
					break
				}
				delete(e.cache, k)
			}
		}
		e.cache[key] = cacheEntry{v: res.Value, p: res.Provider, exp: exp}
	}
	f.res, f.err = res, err
	close(f.done)
	e.mu.Unlock()
	return res, err
}

func (e *Engine) doOnce(ctx context.Context, req Request) (Result, error) {
	s := e.o.Settings()
	has := func(id string) bool { _, ok := req.Fetch[id]; return ok }
	e.mu.Lock()
	now := e.now()
	solo := e.eligible(req.Category, req.Region, has, s) <= 1
	list, forced := e.rank(req.Category, req.Region, has, s, now)
	if forced != nil && e.strongTryAllowed(req.Category, now) {
		list = []candidate{*forced}
	}
	e.mu.Unlock()
	if len(list) == 0 {
		return Result{}, &CallError{Base: ErrNoProvider}
	}
	attempts, limit, nfCount, failCount := 0, maxAttempts, 0, 0
	var last Kind
	for _, c := range list {
		if attempts >= limit || ctx.Err() != nil {
			break
		}
		if !e.takeProvider(c.p, s) {
			continue
		}
		attempts++
		v, k, took, url, err := e.attempt(ctx, c, req.Fetch[c.p.ID])
		e.record(c.p, c.route, k, took, false, solo, false, retryOf(err))
		if e.o.Trace != nil {
			e.o.Trace(Attempt{Provider: c.p.ID, Category: req.Category, URL: url, Took: took, Kind: k, Err: err})
		}
		last = k
		switch k {
		case KOK:
			return Result{Value: v, Provider: c.p, Attempts: attempts}, nil
		case KNotFound:
			nfCount++
			if nfCount >= 2 {
				limit = attempts // 两家都说查无就停；中间夹着真失败的，还有名额就继续问下一家
			}
		default:
			failCount++
		}
	}
	switch {
	case nfCount > 0 && failCount == 0: // 只有被问到的都说查无才算查无，夹着失败的不能回成“查无此地”
		return Result{}, &CallError{Base: ErrNotFound, Attempts: attempts, Last: KNotFound}
	case attempts == 0:
		return Result{}, &CallError{Base: ErrNoProvider}
	}
	return Result{}, &CallError{Base: ErrAllFailed, Attempts: attempts, Last: last}
}

func retryOf(err error) time.Duration { _, d := Classify(err); return d }

// attempt 发一次：单次最多 AttemptMax，也不超过整体剩余时间。调用方自己取消或整体时限到了，不记成提供方的错。
// url 是真发出去的最后一个请求地址（要记插件日志时才包一层记下来）
func (e *Engine) attempt(ctx context.Context, c candidate, f Fetcher) (any, Kind, time.Duration, string, error) {
	url := e.urlOf(c.p)
	cl, err := e.o.HTTP(c.p.Network == NetDirect)
	if err != nil {
		return nil, KNet, 0, url, err
	}
	var rec *urlRecorder
	if e.o.Trace != nil && cl != nil {
		rec = &urlRecorder{base: cl.Transport}
		wrapped := *cl
		wrapped.Transport = rec
		cl = &wrapped
	}
	actx, cancel := context.WithTimeout(ctx, e.o.AttemptMax)
	defer cancel()
	start := time.Now()
	v, err := f(actx, Env{Client: cl, Provider: c.p, URL: e.urlOf(c.p)})
	took := time.Since(start)
	k, _ := Classify(err)
	if k != KOK && k != KNotFound && ctx.Err() != nil {
		k = KCanceled
	}
	if rec != nil {
		if u := rec.last(); u != "" {
			url = u
		}
	}
	return v, k, took, url, err
}

// urlRecorder 记下经过的最后一个请求地址，其余照原样交给底下的传输
type urlRecorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	url  string
}

func (r *urlRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.url = req.URL.String()
	r.mu.Unlock()
	base := r.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func (r *urlRecorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.url
}

func (e *Engine) urlOf(p Provider) string {
	if u, ok := e.o.URLs[p.ID]; ok {
		return u
	}
	return p.URL
}
