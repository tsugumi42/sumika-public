package app

import (
	"context"
	"testing"
	"time"
)

// 工人因错误退出后，下一次 connected 要能重新起来，不能一直占着旧的 cancel。
func TestBackfillWorkerRestartsAfterError(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	e.p.mu.Lock()
	e.p.ready = false
	e.p.mu.Unlock()
	e.b.connected(context.Background())
	e.clk.WaitTimer(t, time.Second)
	e.clk.Advance(time.Minute) // 等就绪超时，run 返回错误
	e.b.mu.Lock()
	first := e.b.done
	e.b.mu.Unlock()
	<-first
	e.p.mu.Lock()
	e.p.ready = true
	e.p.mu.Unlock()
	e.b.connected(context.Background())
	e.b.mu.Lock()
	restarted := e.b.done != first
	e.b.mu.Unlock()
	if !restarted {
		t.Fatal("工人出错退出后 connected 没有重新启动")
	}
	e.b.mu.Lock()
	alive := e.b.aliveLocked()
	e.b.mu.Unlock()
	if !alive {
		t.Fatal("重启后工人应在运行")
	}
}
