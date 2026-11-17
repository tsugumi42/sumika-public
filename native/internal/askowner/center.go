// Package askowner 是她在一轮中间向主人提问并等回答（规格-向主人提问）。
//
// 这里只有“问”和“答”：答案变成一段文字交还给她，不是批准。所以这个包不引 internal/approval、internal/policy，
// 结果里也没有任何授权字段（测试断言这条 import 边界）。
package askowner

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Option 是一个选项
type Option struct{ Label, Detail string }

// Question 是一个在等回答的问题；身份全由宿主绑定，模型只填 Text、Options
type Question struct {
	ID, CharacterID, Conversation, TurnID string
	Text                                  string
	Options                               []Option
	CreatedAt, ExpiresAt                  int64
}

// Kind 是等待的结局
type Kind string

const (
	Chosen    Kind = "chosen"    // 主人点了选项
	Said      Kind = "said"      // 主人没点选项，直接说了一句（走插话）
	Skipped   Kind = "skipped"   // 主人点了“先不答”
	Expired   Kind = "expired"   // 到期没回答，不算同意
	Cancelled Kind = "cancelled" // 这一轮被停下
)

// Result 是等待的结果；Option 只在 Chosen 时有意义
type Result struct {
	Kind   Kind
	Option int
}

// MaxWaiting 是全程序同时等的问题上限：每个等待占一个全局工具槽（共 8 个），留够别的工具用
const MaxWaiting = 4

type waiter struct {
	q    Question
	done chan Result // 容量 1：谁把它从 waiting 里拿走，谁放结局
}

// Center 是在等回答的问题。零值不能用，用 NewCenter
type Center struct {
	mu       sync.Mutex
	waiting  map[string]*waiter
	missed   map[string]string // 会话 → 这个回合里有问题到期没等到（同一轮再问就不等了）
	watchers []func()
}

// NewCenter 是空的
func NewCenter() *Center {
	return &Center{waiting: map[string]*waiter{}, missed: map[string]string{}}
}

// OnChange 注册问题变动的通知：不在锁里调，也不能阻塞。启动时注册一次
func (c *Center) OnChange(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watchers = append(c.watchers, fn)
}

func (c *Center) changed() {
	c.mu.Lock()
	fns := append([]func(){}, c.watchers...)
	c.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Ask 把问题挂出来等回答。ctx 取消、到时限都收回问题；同时到的回答优先（真答了就算答了）
func (c *Center) Ask(ctx context.Context, q Question, wait time.Duration) Result {
	w := &waiter{q: q, done: make(chan Result, 1)}
	c.mu.Lock()
	c.waiting[q.ID] = w
	c.mu.Unlock()
	c.changed()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var r Result
	select {
	case r = <-w.done:
	case <-ctx.Done():
		r = c.withdraw(w, Cancelled)
	case <-timer.C:
		r = c.withdraw(w, Expired)
	}
	if r.Kind == Expired {
		c.mu.Lock()
		c.missed[q.Conversation] = q.TurnID
		c.mu.Unlock()
	}
	return r
}

// withdraw 收回还没结局的问题；已经被答了的（从 waiting 拿走了）等那个结局
func (c *Center) withdraw(w *waiter, kind Kind) Result {
	c.mu.Lock()
	if c.waiting[w.q.ID] != w {
		c.mu.Unlock()
		return <-w.done
	}
	delete(c.waiting, w.q.ID)
	c.mu.Unlock()
	c.changed()
	return Result{Kind: kind}
}

// finish 在锁里把问题拿走（同一个问题只有一个结局），解锁后放结局、发通知
func (c *Center) finish(w *waiter, r Result) {
	delete(c.waiting, w.q.ID)
	c.mu.Unlock()
	w.done <- r
	c.changed()
}

// Answer 是主人点了第 option 个选项；负数 = 先不答。问题不在了、下标越界返回 false，问题状态不变
func (c *Center) Answer(id string, option int) bool {
	c.mu.Lock()
	w := c.waiting[id]
	if w == nil || option >= len(w.q.Options) {
		c.mu.Unlock()
		return false
	}
	r := Result{Kind: Chosen, Option: option}
	if option < 0 {
		r = Result{Kind: Skipped}
	}
	c.finish(w, r)
	return true
}

// Interrupt 是这个会话收到了插话：结束等待，这句话就是回答。没有在等的返回 false。
// 不结束的话工具等回答、插话等工具做完，两边互相等到超时（规格第四节）
func (c *Center) Interrupt(conversation string) bool {
	c.mu.Lock()
	for _, w := range c.waiting {
		if w.q.Conversation == conversation {
			c.finish(w, Result{Kind: Said})
			return true
		}
	}
	c.mu.Unlock()
	return false
}

// Waiting 是这个会话有没有问题在等
func (c *Center) Waiting(conversation string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.waiting {
		if w.q.Conversation == conversation {
			return true
		}
	}
	return false
}

// Full 是同时等的问题到上限了
func (c *Center) Full() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiting) >= MaxWaiting
}

// Missed 是这个回合里已经有问题到期没等到
func (c *Center) Missed(conversation, turnID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.missed[conversation]
	return ok && t == turnID
}

// Snapshot 是在等的问题，按问出来的先后
func (c *Center) Snapshot() []Question {
	c.mu.Lock()
	out := make([]Question, 0, len(c.waiting))
	for _, w := range c.waiting {
		q := w.q
		q.Options = append([]Option(nil), q.Options...)
		out = append(out, q)
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}
