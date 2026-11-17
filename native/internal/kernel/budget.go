package kernel

import (
	"context"
	"math"
	"time"
)

// NoLimit 是"这一项不设上限"在账本里的写法：账本只认正数，配置里的 0 在装配时换成它。取四分之一是为了加减不溢出。
const NoLimit int64 = math.MaxInt64 / 4

// NoDeadlineAfter 是"不设时限"：账本要一个具体的截止时刻，给一个一百年后的（context 的截止时间也装得下）
const NoDeadlineAfter = 100 * 365 * 24 * time.Hour

// Unlimited 说这一项是不是"不设上限"（账本里的数达到 NoLimit 的一半以上都算，防止减去已用量后略小）
func Unlimited(v int64) bool { return v >= NoLimit/2 }

type Limits struct {
	MaxRequests, MaxTools      int64
	MaxTokens, MaxOutputTokens int64
	Deadline                   time.Time
	MaxCostMicros              *int64
	// RequestOutputTokens 是这个根里每次请求的输出上限；0 用模型调用端自己的默认。受理时按场合定下，之后不变
	RequestOutputTokens int64
}

type Reservation struct {
	ID                                    string
	Invocation                            Invocation
	Requests, Tools, Tokens, OutputTokens int64
	CostMicros                            *int64
}

type Charge struct {
	Input, Output, CacheRead, CacheWrite, Reasoning int64
	UsageSeen                                       bool
	CostMicros                                      *int64
	Dispatched                                      bool
}

type BudgetSnapshot struct {
	RootID                                string
	Limits                                Limits
	Requests, Tools, Tokens, OutputTokens int64
	ReservedTokens, ReservedOutputTokens  int64
	CostMicros                            *int64
	Incomplete                            bool
}

// Budget 由 K3 实现；未知用量保留预留，父子运行共用 RootID。
type Budget interface {
	BindChild(context.Context, Invocation, Invocation) error
	Reserve(context.Context, Reservation) error
	Dispatch(context.Context, string) error
	Settle(context.Context, string, Charge) error
	Snapshot(context.Context, string) (BudgetSnapshot, error)
}
