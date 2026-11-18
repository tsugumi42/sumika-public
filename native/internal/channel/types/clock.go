package types

import "time"

// Clock 是出口（Outbox 的实现）用的钟：现在几点、等一会儿，都从这里取。
// 出站里全是时间：段间停顿、重试间隔、发送超时、回声认领的 5 分钟、关闭时等回执的 5 秒——
// 直接用 time.Sleep 的话，测试要么真等（慢、还不稳），要么测不到"停顿期间切档"这类只在某一刻才有的情况。
// 生产用 channel.SystemClock，测试用 fake.Clock（自己拨时间）
type Clock interface {
	Now() time.Time
	// NewTimer 过 d 以后在 C() 上给一个时刻；d 不大于 0 就立刻给。Stop 取消，返回取消时它还没触发
	NewTimer(d time.Duration) Timer
}

// Timer 是 Clock.NewTimer 造出来的计时器，形状和 time.Timer 一样但 C 是方法，好让假钟也能实现
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}
