package modelcall

import (
	"context"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turnbudget"
)

type observedBudget struct {
	kernel.Budget
	once   sync.Once
	queued chan struct{}
}

func (b *observedBudget) Snapshot(ctx context.Context, root string) (kernel.BudgetSnapshot, error) {
	s, err := b.Budget.Snapshot(ctx, root)
	in := kernel.InvocationFromContext(ctx)
	if in.AttemptID == "second" {
		b.once.Do(func() { close(b.queued) })
	}
	return s, err
}

func TestQueuedModelRefreshesAllowance(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ledger := &turnbudget.Ledger{Store: st}
	in := kernel.Invocation{RootID: "queued", RunID: "run", StepID: "step", Kind: "foreground"}
	if err := ledger.OpenRoot(ctx, in.RootID, in.Scope, kernel.Limits{MaxRequests: 2, MaxTools: 1, MaxTokens: 1000, MaxOutputTokens: 10, Deadline: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	observed := &observedBudget{Budget: ledger, queued: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	secondOutput := make(chan int64, 1)
	transport := transportFunc(func(ctx context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		if r.Model == "first" {
			close(entered)
			<-release
			return llm.Result{UsageSeen: true, Usage: llm.Usage{Output: 4}}, nil
		}
		secondOutput <- r.MaxOutputTokens
		return llm.Result{UsageSeen: true, Usage: llm.Usage{Output: r.MaxOutputTokens}}, nil
	})
	caller := New(transport, observed, st, 1, 10)
	done := make(chan error, 2)
	launch := func(name string) {
		x := in
		x.AttemptID = name
		go func() {
			_, err := caller.StreamChat(kernel.WithInvocation(ctx, x), x, testChatRequest(name, 0), nil)
			done <- err
		}()
	}
	launch("first")
	<-entered
	launch("second")
	<-observed.queued
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if out := <-secondOutput; out != 6 {
		t.Fatalf("queued request reserved %d output tokens, want remaining 6", out)
	}
	snapshot, err := ledger.Snapshot(ctx, in.RootID)
	if err != nil || snapshot.Requests != 2 || snapshot.OutputTokens != 10 {
		t.Fatalf("snapshot %+v %v", snapshot, err)
	}
}
