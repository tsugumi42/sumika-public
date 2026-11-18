package modelcall

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turnbudget"
)

func reserveFixture(t *testing.T, limits kernel.Limits) (*turnbudget.Ledger, *store.Store, kernel.Invocation) {
	t.Helper()
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ledger := &turnbudget.Ledger{Store: st}
	in := kernel.Invocation{RootID: "r", RunID: "run", Kind: "foreground", StepID: "s"}
	limits.Deadline = time.Now().Add(time.Minute)
	if err := ledger.OpenRoot(context.Background(), "r", in.Scope, limits); err != nil {
		t.Fatal(err)
	}
	return ledger, st, in
}

// 单次输出跟模型：调用端按模型算好的值原样发出，不再被固定值（原来的 4096）压住；调用端没给时用兜底 32000
func TestOutputFollowsModelWhenNoCap(t *testing.T) {
	ledger, st, in := reserveFixture(t, kernel.Limits{MaxRequests: kernel.NoLimit, MaxTools: kernel.NoLimit, MaxTokens: kernel.NoLimit, MaxOutputTokens: kernel.NoLimit})
	var got []int64
	tr := transportFunc(func(_ context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		got = append(got, r.MaxOutputTokens)
		return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 5, Output: 1}}, nil
	})
	caller := New(tr, ledger, st, 4, 0)
	for i, want := range []struct{ ask, send int64 }{{128000, 128000}, {0, llm.FallbackMaxOutput}} {
		x := in
		x.AttemptID = string(rune('a' + i))
		if _, err := caller.StreamChat(context.Background(), x, testChatRequest("m", want.ask), nil); err != nil {
			t.Fatal(err)
		}
		if got[i] != want.send {
			t.Errorf("请求 %d：模型给 %d，发出去 %d，想要 %d", i, want.ask, got[i], want.send)
		}
	}
}

// 回归：放开单次输出后，一次请求能把根的输出余额整个发出去；以前预留等于发送值，同根并发的下一个请求算出 0，
// 走 ExhaustRoot 停掉整个根（和所有子代理）。现在预留只是均分的占位，第二个请求照常发出，根不被停
func TestConcurrentRequestDoesNotExhaustRootOnlyBecauseOfReservation(t *testing.T) {
	ledger, st, in := reserveFixture(t, kernel.Limits{MaxRequests: 10, MaxTools: 10, MaxTokens: 1 << 20, MaxOutputTokens: 32768})
	release := make(chan struct{})
	entered := make(chan int64, 4)
	tr := transportFunc(func(ctx context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		entered <- r.MaxOutputTokens
		<-release
		return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 5, Output: 10}}, nil
	})
	caller := New(tr, ledger, st, 2, 0)
	errs := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		x := in
		x.AttemptID = id
		go func() {
			_, err := caller.StreamChat(context.Background(), x, testChatRequest("m", 128000), nil)
			errs <- err
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s 没有发出去：被别人的预留挡住了", id)
		}
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("并发请求失败：%v", err)
		}
	}
	// 根没被停：第三个请求仍然能发
	third := in
	third.AttemptID = "third"
	if _, err := caller.StreamChat(context.Background(), third, testChatRequest("m", 128000), nil); err != nil {
		t.Fatalf("根不该被停：%v", err)
	}
}

// 只是被别的在途请求占住（实际一个字没花）：等它们结算后重算，不停根；真用完了才停根
func TestWaitsForSettleThenExhaustsOnlyWhenReallySpent(t *testing.T) {
	ledger, st, in := reserveFixture(t, kernel.Limits{MaxRequests: 10, MaxTools: 10, MaxTokens: 1 << 20, MaxOutputTokens: 2})
	release := make(chan struct{})
	entered := make(chan string, 3)
	tr := transportFunc(func(ctx context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		entered <- r.Model
		if r.Model == "hold" {
			<-release
		}
		return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 1, Output: 0}}, nil
	})
	caller := New(tr, ledger, st, 3, 0)
	run := func(id, model string, errs chan<- error) {
		x := in
		x.AttemptID = id
		_, err := caller.StreamChat(context.Background(), x, testChatRequest(model, 100), nil)
		errs <- err
	}
	errs := make(chan error, 3)
	go run("a", "hold", errs)
	<-entered
	go run("b", "hold", errs)
	<-entered
	// 两个占位已经把 2 个输出额度占光，第三个只能等
	go run("c", "fast", errs)
	select {
	case m := <-entered:
		t.Fatalf("第三个请求不该在占位释放前发出：%s", m)
	case err := <-errs:
		t.Fatalf("第三个请求不该直接失败（那会停根）：%v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	for range 3 {
		select {
		case err := <-errs:
			if err != nil && !errors.Is(err, turnbudget.ErrExhausted) {
				t.Fatalf("意外的错误：%v", err)
			}
			if err != nil {
				t.Fatalf("占位释放后第三个请求该成功，输出一个字没花：%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("占位释放后请求没有继续")
		}
	}
}

// 真的用完了（已经花掉的输出到了上限）才是耗尽，要停根
func TestExhaustsRootWhenOutputReallySpent(t *testing.T) {
	ledger, st, in := reserveFixture(t, kernel.Limits{MaxRequests: 10, MaxTools: 10, MaxTokens: 1 << 20, MaxOutputTokens: 20})
	tr := transportFunc(func(_ context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 1, Output: 20}}, nil
	})
	caller := New(tr, ledger, st, 2, 0)
	first := in
	first.AttemptID = "a"
	if _, err := caller.StreamChat(context.Background(), first, testChatRequest("m", 100), nil); err != nil {
		t.Fatal(err)
	}
	second := in
	second.AttemptID = "b"
	_, err := caller.StreamChat(context.Background(), second, testChatRequest("m", 100), nil)
	if !errors.Is(err, turnbudget.ErrExhausted) {
		t.Fatalf("实际输出已用完应该报耗尽：%v", err)
	}
}

// 以前崩溃留下的占位（派发了没结算）没有人会结算：本进程没有别的在途请求时按真耗尽处理，不能一直等到根时限
func TestLeakedReservationIsNotWaitedForever(t *testing.T) {
	ledger, st, in := reserveFixture(t, kernel.Limits{MaxRequests: 10, MaxTools: 10, MaxTokens: 1 << 20, MaxOutputTokens: 100})
	leak := in
	leak.AttemptID = "leaked"
	if err := ledger.Reserve(context.Background(), kernel.Reservation{ID: "leaked", Invocation: leak, Requests: 1, Tokens: 100, OutputTokens: 100}); err != nil {
		t.Fatal(err)
	}
	tr := transportFunc(func(_ context.Context, r chat.Request, _ func(llm.Event)) (llm.Result, error) {
		t.Fatal("没有额度，不该发请求")
		return llm.Result{}, nil
	})
	caller := New(tr, ledger, st, 2, 0)
	done := make(chan error, 1)
	go func() {
		x := in
		x.AttemptID = "a"
		_, err := caller.StreamChat(context.Background(), x, testChatRequest("m", 50), nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, turnbudget.ErrExhausted) {
			t.Fatalf("应该报耗尽：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("一直在等一个没人结算的占位")
	}
}
