package pool

import (
	"sort"
	"sync"
	"time"
)

// fakeClock 是手动拨的时钟：Advance 把时间往前拨，到点的定时器触发。定时器的通道有 1 的缓冲，触发不会卡在没人收的地方。
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clk     *fakeClock
	c       chan time.Time
	when    time.Time
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{clk: f, c: make(chan time.Time, 1), when: f.now.Add(d)}
	if d <= 0 {
		t.fired = true
		t.c <- f.now
	} else {
		f.timers = append(f.timers, t)
	}
	return t
}

// Advance 把时间拨 d，按到点的先后触发定时器。
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	due := make([]*fakeTimer, 0, len(f.timers))
	keep := f.timers[:0]
	for _, t := range f.timers {
		if !t.stopped && !t.fired && !t.when.After(now) {
			due = append(due, t)
		} else if !t.stopped && !t.fired {
			keep = append(keep, t)
		}
	}
	f.timers = keep
	sort.Slice(due, func(i, j int) bool { return due[i].when.Before(due[j].when) })
	for _, t := range due {
		t.fired = true
		t.c <- now
	}
	f.mu.Unlock()
}

// Pending 是还没触发、没停的定时器个数。
func (f *fakeClock) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.timers {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.clk.mu.Lock()
	defer t.clk.mu.Unlock()
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}
