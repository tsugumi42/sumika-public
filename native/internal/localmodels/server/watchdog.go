package server

import (
	"context"
	"fmt"
	"time"
)

// Watchdog 盯着前向：单次前向超过 Limit 就让进程自己退出（退出码 4），由 Local 重启。
// 理由（规格 2.5"看门狗"）：Local 的 ping 只看 /health 回 200，进程活着但 ORT 卡死时会一直是"就绪"，
// 守卫每次等满 2 秒再退到"只比原文"；前向中途打断不了（Run 没有取消），只能把整个进程换掉。
type Watchdog struct {
	Mon      *Monitor
	Limit    time.Duration
	Interval time.Duration // 多久看一次；0 = 取 Limit 的四分之一，夹在 100 毫秒到 5 秒之间
	Fail     func(code int, msg string)
}

func (w *Watchdog) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return min(5*time.Second, max(100*time.Millisecond, w.Limit/4))
}

// Run 一直看到 ctx 结束，或者触发了就返回。Limit ≤ 0 什么也不做。
func (w *Watchdog) Run(ctx context.Context) {
	if w.Limit <= 0 {
		return
	}
	t := time.NewTicker(w.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if d := w.Mon.Longest(); d > w.Limit {
				w.Fail(ExitWatchdog, fmt.Sprintf("前向超时：一次前向已经跑了 %.1f 秒，超过 --run-watchdog-seconds %.0f，进程退出由 Local 重启", d.Seconds(), w.Limit.Seconds()))
				return
			}
		}
	}
}
