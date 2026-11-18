package pool

import "time"

// Clock 是池用的时钟：当前时间和定时器。生产用真的，测试用假的（clock_test.go），空闲卸载、等槽位、等加载都不用真睡。
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer 是一次性的定时器。
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type realClock struct{}

func (realClock) Now() time.Time                 { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }
