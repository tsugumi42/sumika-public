package selfcheck_test

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/selfcheck"
)

// S5：10 分钟窗口里断开 6 次才 bad；窗口外的不算；用的是"断开事件"，不是重试次数
func TestS5ReconnectStorm(t *testing.T) {
	th := selfcheck.DefaultThresholds()
	th.ReconnectCount, th.ReconnectWindow = 6, 10*time.Minute
	c := findCheck(t, "channel.reconnect_storm", th)
	live := selfcheck.NewCounters()
	for i := 0; i < 5; i++ {
		live.Disconnect(t0.Add(-time.Duration(i) * time.Minute))
	}
	if r := c.Eval(context.Background(), envAt(t0, live)); r.State != selfcheck.Ok || r.Value != 5 {
		t.Fatalf("5 次不到阈值：%+v", r)
	}
	live.Disconnect(t0)
	if r := c.Eval(context.Background(), envAt(t0, live)); r.State != selfcheck.Bad || r.Value != 6 {
		t.Fatalf("6 次在窗口里应 bad：%+v", r)
	}
	// 一个小时以后，这些都在窗口外
	if r := c.Eval(context.Background(), envAt(t0.Add(time.Hour), live)); r.State != selfcheck.Ok || r.Value != 0 {
		t.Fatalf("窗口外的不算：%+v", r)
	}
}

// 配置允许阈值到 100（config/selfcheck.go）：环形窗口要装得下，阈值 100 时第 100 次断开要能触发
func TestS5ThresholdAtConfigMaximumCanFire(t *testing.T) {
	th := selfcheck.DefaultThresholds()
	th.ReconnectCount, th.ReconnectWindow = 100, time.Hour
	c := findCheck(t, "channel.reconnect_storm", th)
	live := selfcheck.NewCounters()
	for i := 0; i < 99; i++ {
		live.Disconnect(t0.Add(-time.Duration(i) * time.Second))
	}
	if r := c.Eval(context.Background(), envAt(t0, live)); r.State != selfcheck.Ok || r.Value != 99 {
		t.Fatalf("99 次不到阈值：%+v", r)
	}
	live.Disconnect(t0)
	if r := c.Eval(context.Background(), envAt(t0, live)); r.State != selfcheck.Bad || r.Value != 100 {
		t.Fatalf("100 次在窗口里应 bad（环形窗口装不下就永远数不到 100）：%+v", r)
	}
}
