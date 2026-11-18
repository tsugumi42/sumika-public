package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 自动接收失败后按退避重试：成功就停、不想要了就停、程序退出就停、试满就放弃（2026-11-08 重启后 Magpie 晚起，接收一直停着）
func TestRetryAutoReceive(t *testing.T) {
	t.Parallel()
	logf := func(string, ...any) {}
	delays := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	run := func(failUntil int, wanted func(n int) bool, ctx context.Context) int {
		n := 0
		retryAutoReceive(ctx, delays, func() receiveResult {
			n++
			if n <= failUntil {
				return receiveResult{Outcome: receiveOpenFailed, Err: errors.New("连不上 Magpie")}
			}
			return receiveResult{Outcome: receiveStarted}
		}, func() bool { return wanted(n) }, logf)
		return n
	}
	always := func(int) bool { return true }
	if n := run(0, always, context.Background()); n != 1 {
		t.Fatalf("第一次就成：试了 %d 次", n)
	}
	if n := run(2, always, context.Background()); n != 3 {
		t.Fatalf("前两次失败第三次成：试了 %d 次", n)
	}
	if n := run(99, always, context.Background()); n != 1+len(delays) {
		t.Fatalf("一直失败要试满：试了 %d 次", n)
	}
	if n := run(99, func(n int) bool { return n < 2 }, context.Background()); n != 2 {
		t.Fatalf("用户切换过就停：试了 %d 次", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n := run(99, always, ctx); n != 1 {
		t.Fatalf("退出了不再试：试了 %d 次", n)
	}
}

// 失败种类决定要不要重试：只有"接收后台开不了"值得再试；没小号、后台受阻、已在接收、退出中都不重试，各写一行日志
func TestRetryAutoReceiveByOutcome(t *testing.T) {
	t.Parallel()
	delays := []time.Duration{time.Millisecond, time.Millisecond}
	for _, c := range []struct {
		o    receiveOutcome
		want int
	}{
		{receiveStarted, 1}, {receiveAlready, 1}, {receiveCancelled, 1},
		{receiveNoAccount, 1}, {receiveBlocked, 1}, {receiveOpenFailed, 1 + len(delays)},
		{receiveLocalBusy, 1 + len(delays)}, {receiveLocalNotReady, 1 + len(delays)},
	} {
		n := 0
		var logs []string
		retryAutoReceive(context.Background(), delays, func() receiveResult {
			n++
			return receiveResult{Outcome: c.o, Err: errors.New("原因")}
		}, func() bool { return true }, func(f string, a ...any) { logs = append(logs, f) })
		if n != c.want {
			t.Errorf("结果种类 %d：试了 %d 次，应为 %d", c.o, n, c.want)
		}
		if len(logs) == 0 {
			t.Errorf("结果种类 %d：一行日志都没写", c.o)
		}
	}
}
