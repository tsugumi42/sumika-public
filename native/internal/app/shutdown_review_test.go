package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/turn"
)

// 第二批已经在 StopIn 等；路由先耗掉大半预算，Outcome 仍未回来，不能重新起一份 10 秒等待。
func TestShutdownStopInSharesDeadline(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	// 等回复的上限缩到 3 秒、路由占 2.4 秒（生产是 10 秒和 8 秒，同一个 8:10 比例）：照样量得出“共用一个截止时间”和“各等各的”的差别
	const idle, routeHold = 3 * time.Second, 2400 * time.Millisecond
	a.responderWait = idle
	defer a.set.Close() // 改坏关库时也要收好测试自己的连接。
	k := &shutdownKernel{Kernel: a.kernel.Kernel, stopped: make(chan struct{}), route: make(chan struct{}), outcome: make(chan turn.Outcome, 1)}
	a.kernel.Kernel = k
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var routeOnce, outcomeOnce sync.Once
	unblockRoute := func() { routeOnce.Do(func() { close(k.route) }) }
	unblockKernel := func() { outcomeOnce.Do(func() { k.outcome <- turn.Outcome{} }) }
	finished := false
	defer func() {
		cancel()
		unblockRoute()
		unblockKernel()
		if !finished {
			<-done
		}
		a.kernel.idle(context.Background())
	}()
	c := e.peer.accept(t)
	e.peer.send(t, c, groupAtFrame(e.now, testAccount, "721", testOwner, "编的第一批"))
	waitFor(t, "第一批已调用内核", func() bool { return a.kernel.calls.Load() == 1 })
	e.peer.send(t, c, groupAtFrame(e.now.Add(time.Minute), testAccount, "722", testOwner, "编的第二批"))
	select {
	case <-k.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("第二批没有进入 StopIn")
	}
	started := time.Now()
	// 路由占 routeHold；内核不返回，关机只能用剩下的 idle-routeHold。
	timer := time.AfterFunc(routeHold, unblockRoute)
	defer timer.Stop()
	cancel()
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(idle + time.Second):
		// 放回 Outcome，旧实现也能收好测试环境。
		unblockKernel()
		<-done
		finished = true
		t.Fatal("路由和内核等待没有共用关机截止时间")
	}
	elapsed := time.Since(started)
	if err := a.set.TraceStore().Trace.Ping(); err == nil {
		t.Fatal("关机返回时 trace 库仍然开着")
	}
	b := e.open()
	b.cleanup()
	t.Logf("第二批 StopIn 期间取消：耗时 %v，库已关闭且可重开", elapsed)
}

type shutdownKernel struct {
	channel.Kernel
	stopped chan struct{}
	route   chan struct{}
	outcome chan turn.Outcome
}

func (k *shutdownKernel) Respond(turn.Input) <-chan turn.Outcome { return k.outcome }
func (k *shutdownKernel) StopIn(string, string) bool             { close(k.stopped); <-k.route; return true }

func (k *shutdownKernel) Drain(ctx context.Context) error {
	return k.Kernel.(interface{ Drain(context.Context) error }).Drain(ctx)
}
