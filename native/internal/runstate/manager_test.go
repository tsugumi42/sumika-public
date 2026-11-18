package runstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/kernel"
)

func TestDrainWaitsRealExit(t *testing.T) {
	m := New(nil)
	in := kernel.Invocation{RunID: "run-a", ToolID: "tool-host-a", ProviderCallID: "repeatable-provider-id", Kind: kernel.KindForeground}
	ctx, a, err := m.Begin(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	release, exited := make(chan struct{}), make(chan struct{})
	go func() { <-release; a.End(context.DeadlineExceeded); close(exited) }()
	m.Seal()
	m.CancelRun(in.RunID)
	if ctx.Err() == nil {
		t.Fatal("未取消")
	}
	timeout, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	before := time.Now()
	err = m.Drain(timeout)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("工具可见超时不应当排空 %v", err)
	}
	t.Logf("执行屏障未退：Drain=%v elapsed=%v", err, time.Since(before))
	close(release)
	<-exited
	if err = m.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.Begin(context.Background(), in); !errors.Is(err, ErrSealed) {
		t.Fatalf("Seal 后仍准入 %v", err)
	}
}

type blockedJournal struct{ entered, release chan struct{} }

func (j blockedJournal) ActivityStart(ctx context.Context, _ string, _ kernel.Invocation) error {
	close(j.entered)
	<-j.release
	return ctx.Err()
}
func (j blockedJournal) ActivityEnd(context.Context, string, error) error {
	panic("未准入的执行不应退出")
}

func TestShutdownAdmissionBarrier(t *testing.T) {
	j := blockedJournal{make(chan struct{}), make(chan struct{})}
	m := New(j)
	begin := make(chan error, 1)
	go func() {
		_, _, err := m.Begin(context.Background(), kernel.Invocation{RunID: "intent", Kind: kernel.KindForeground})
		begin <- err
	}()
	<-j.entered
	sealed := make(chan struct{})
	go func() { m.Seal(); m.CancelAll(); close(sealed) }()
	select {
	case <-sealed:
	case <-time.After(time.Second):
		close(j.release)
		<-begin
		t.Fatal("持久化拿着准入锁，Seal 无法取消在途写入")
	}
	if _, _, err := m.Begin(context.Background(), kernel.Invocation{Kind: kernel.KindForeground}); !errors.Is(err, ErrSealed) {
		t.Fatalf("Seal 后准入：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("意图写入未退就排空：%v", err)
	}
	close(j.release)
	if err := <-begin; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后仍派发：%v", err)
	}
	if err := m.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}
