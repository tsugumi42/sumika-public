package fake

import (
	"sort"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// Clock 是假钟（types.Clock）：时间只在测试调 Advance 的时候往前走，计时器到点才触发。
// 出站的停顿、重试、超时、回声认领的窗口全靠它测，不用真等，也不用 sleep 去碰运气
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	seq    int
}

var _ types.Clock = (*Clock)(nil)

// NewClock 造一个停在 start 的假钟
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

type fakeTimer struct {
	c     *Clock
	at    time.Time
	n     int // 造的先后，同一刻到点的按这个排
	ch    chan time.Time
	fired bool
	gone  bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.fired && !t.gone
	t.gone = true
	return was
}

// Now 是假钟现在的时间
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer 实现 types.Clock：d 不大于 0 立刻触发
func (c *Clock) NewTimer(d time.Duration) types.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &fakeTimer{c: c, at: c.now.Add(d), n: c.seq, ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.fired = true
		t.ch <- c.now
		return t
	}
	c.timers = append(c.timers, t)
	return t
}

// Advance 把假钟拨快 d，到点的计时器按先后触发。触发只是往通道里放一个值：等着它的 goroutine 什么时候醒、
// 醒来以后又造了什么新计时器，由调度决定，所以测试拨一次以后要用 WaitPending 等到它们安顿好再看结果或再拨
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	live := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.gone || t.fired:
		case !t.at.After(c.now):
			due = append(due, t)
		default:
			live = append(live, t)
		}
	}
	c.timers = live
	sort.Slice(due, func(i, j int) bool {
		if !due[i].at.Equal(due[j].at) {
			return due[i].at.Before(due[j].at)
		}
		return due[i].n < due[j].n
	})
	for _, t := range due {
		t.fired = true
		t.ch <- t.at
	}
}

// Pending 是现在还在等的计时器个数（没触发、没取消的）
func (c *Clock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.gone && !t.fired {
			n++
		}
	}
	return n
}

// waitLimit 是真等计时器造好的上限，只防卡死：另一个协程停几秒在机器忙时很常见（2026-11-09 合并队列 5 秒红过）。
const waitLimit = time.Minute

// WaitPending 等到 Pending() 正好是 n，最多真等 waitLimit；等不到就让测试失败。
// 被测的代码在另一个 goroutine 里造计时器，测试要先等它造好才能拨钟
func (c *Clock) WaitPending(t testing.TB, n int) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for c.Pending() != n {
		if time.Now().After(deadline) {
			t.Fatalf("等计时器：想要 %d 个在等，实际 %d 个", n, c.Pending())
		}
		time.Sleep(time.Millisecond)
	}
}

// WaitTimer 等到有一个计时器离到点正好还有 d，最多真等 waitLimit。同时有好几个计时器（发送超时、停顿、重试间隔）时，
// 数个数分不清哪个是哪个——有的刚被取消、下一个还没造出来——靠"还剩多久"认出要等的那一个
func (c *Clock) WaitTimer(t testing.TB, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		c.mu.Lock()
		found := false
		for _, tm := range c.timers {
			if !tm.gone && !tm.fired && tm.at.Sub(c.now) == d {
				found = true
			}
		}
		c.mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等计时器：没有一个还剩 %v 的在等", d)
		}
		time.Sleep(time.Millisecond)
	}
}
