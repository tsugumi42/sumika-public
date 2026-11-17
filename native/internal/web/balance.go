package web

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// 余额缓存：服务商能查到余额的家（Tavily、Firecrawl）按间隔重查，Brave 每次搜索从响应头更新，
// Exa 没有余额接口，只在收到 402 时记“用完”。本地额度只是兜底，这里的数字来自服务商。
const (
	// BalanceRefresh 是余额缓存的有效期；期间每次成功出网再在本地估计里扣掉用掉的单位。
	BalanceRefresh = 10 * time.Minute
	// ExhaustedHold 是“用完”标记（402 或头里的 0）在没有余额接口时保留多久，之后放一次试探请求。
	ExhaustedHold = time.Hour
)

// Balance 是某一家当前已知的剩余额度（单位按服务商：Tavily 和 Firecrawl 是 credits，Brave 是本月请求数）。
type Balance struct {
	Remaining int64
	Unlimited bool
	Known     bool      // false 表示查询失败过；只记 At，避免短时间内反复重查
	At        time.Time // 取得这条记录的时间
}

// Balances 保存各家的余额；方法对 nil 接收者安全，提供方不接就不记。
type Balances struct {
	mu  sync.Mutex
	m   map[string]Balance
	now func() time.Time
	// OnUpdate 在余额变化时回调（装配处用来写日志，只给服务商名和剩余数）。
	OnUpdate func(id string, b Balance)
}

func NewBalances() *Balances { return &Balances{m: map[string]Balance{}, now: time.Now} }

func (b *Balances) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *Balances) store(id string, v Balance) {
	b.mu.Lock()
	b.m[id] = v
	hook := b.OnUpdate
	b.mu.Unlock()
	if hook != nil && v.Known {
		hook(id, v)
	}
}

// Set 记下服务商给出的真实余额。
func (b *Balances) Set(id string, remaining int64, unlimited bool) {
	if b == nil {
		return
	}
	b.store(id, Balance{Remaining: max(remaining, 0), Unlimited: unlimited, Known: true, At: b.clock()})
}

// Fail 记一次查询失败：不改已有余额的数字，只把时间推到现在，别每次搜索都重试。
func (b *Balances) Fail(id string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	v := b.m[id]
	v.At = b.clock()
	b.m[id] = v
	b.mu.Unlock()
}

// Spend 在本地估计里扣掉刚用掉的单位（缓存期内的估计，下次重查时以服务商为准）。
func (b *Balances) Spend(id string, n int64) {
	if b == nil || n <= 0 {
		return
	}
	b.mu.Lock()
	v, ok := b.m[id]
	if !ok || !v.Known || v.Unlimited {
		b.mu.Unlock()
		return
	}
	v.Remaining = max(v.Remaining-n, 0)
	b.m[id] = v
	hook := b.OnUpdate
	b.mu.Unlock()
	if hook != nil {
		hook(id, v)
	}
}

// MarkExhausted 记服务商已拒绝（402 等）：余额视为 0。
func (b *Balances) MarkExhausted(id string) {
	b.Set(id, 0, false)
}

func (b *Balances) Get(id string) (Balance, bool) {
	if b == nil {
		return Balance{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[id]
	return v, ok
}

// Snapshot 给状态展示用：只含已知余额的家。
func (b *Balances) Snapshot() map[string]Balance {
	out := map[string]Balance{}
	if b == nil {
		return out
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, v := range b.m {
		if v.Known {
			out[id] = v
		}
	}
	return out
}

// Exhausted 判断这家现在是否应当跳过。refreshable 表示这家能主动查余额（Tavily、Firecrawl）：
// 这类记录过期后要重查，不在这里当作用完；其余的用完标记保留 ExhaustedHold。
func (b *Balances) Exhausted(id string, refreshable bool) bool {
	v, ok := b.Get(id)
	if !ok || !v.Known || v.Unlimited || v.Remaining > 0 {
		return false
	}
	age := b.clock().Sub(v.At)
	if refreshable {
		return age < BalanceRefresh
	}
	return age < ExhaustedHold
}

// Stale 说明可查余额的这家记录是否需要重查（没有记录，或超过 BalanceRefresh）。
func (b *Balances) Stale(id string) bool {
	v, ok := b.Get(id)
	return !ok || b.clock().Sub(v.At) >= BalanceRefresh
}

// BalanceSource 由能主动查余额的提供方实现。查询不走 DoHTTP（不进本地额度账，也不消耗服务商额度）。
type BalanceSource interface {
	FetchBalance(ctx context.Context) (remaining int64, unlimited bool, err error)
}

// ExhaustedError 表示服务商明确拒绝：额度用完了。
type ExhaustedError struct{ Provider string }

func (e *ExhaustedError) Error() string { return e.Provider + " 额度用完了" }

// IsExhausted 判断错误链里有没有“额度用完”。
func IsExhausted(err error) bool {
	var e *ExhaustedError
	return errors.As(err, &e)
}

// BalanceLine 把余额渲染成一行文字，给日志和 sumika-ctl 用。
func BalanceLine(id string, v Balance) string {
	if v.Unlimited {
		return fmt.Sprintf("%s 剩余 不限", id)
	}
	return fmt.Sprintf("%s 剩余 %d", id, v.Remaining)
}
