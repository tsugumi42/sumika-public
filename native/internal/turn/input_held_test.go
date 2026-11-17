package turn

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/store"
)

// heldAfterRestart 造一句“重启后还是 held 的话”：先正常跑完一句，再把输入行改回 held（事件、载荷都是真的）。
func heldAfterRestart(t *testing.T, h *harness, text, key string) (Submission, kernel.Receipt) {
	t.Helper()
	sub := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), text, key, kernel.InputStart)
	rec, err := h.hub.Submit(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, h.hub, sub.Input.Identity.Scope, rec.RunID)
	if _, err := h.st.Trace.Exec(`UPDATE kernel_inbox SET state='held' WHERE input_id=?`, rec.InputID); err != nil {
		t.Fatal(err)
	}
	return sub, rec
}

func inputState(t *testing.T, h *harness, id string) kernel.InputState {
	t.Helper()
	row, err := h.st.KernelInputByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.State
}

func TestReleaseHeldRunsOnceAndKeepsText(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
	sub, old := heldAfterRestart(t, h, "重启前没处理的话", "held-1")
	held, err := h.hub.HeldInputs(context.Background())
	if err != nil || len(held) != 1 || held[0].Text != "重启前没处理的话" || held[0].InputID != old.InputID || held[0].Scope != sub.Input.Identity.Scope {
		t.Fatalf("列出的 held 不对：%+v %v", held, err)
	}
	// 别人的身份放不了行
	other := sub.Input.Identity.Scope
	other.ActorID = "someone-else"
	if _, err := h.hub.ReleaseHeld(context.Background(), other, old.InputID); !errors.Is(err, store.ErrInputConflict) {
		t.Fatalf("别的身份不该放行：%v", err)
	}
	before := h.bodyCount()
	rec, err := h.hub.ReleaseHeld(context.Background(), sub.Input.Identity.Scope, old.InputID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.RunID == "" || rec.RunID == old.RunID || rec.InputID != old.InputID {
		t.Fatalf("放行回执不对：%+v 旧=%+v", rec, old)
	}
	snap := lifecycleWait(t, h.hub, sub.Input.Identity.Scope, rec.RunID)
	if snap.Outcome != kernel.OutcomeDone {
		t.Fatalf("放行的一轮应当正常结束：%+v", snap)
	}
	if got := inputState(t, h, old.InputID); got != kernel.InputAdmitted {
		t.Fatalf("放行后应当是已处理，现在是 %s", got)
	}
	if after := h.bodyCount(); after != before+1 {
		t.Fatalf("放行应当让她处理这句话恰好一次：%d→%d", before, after)
	}
	if _, err := h.hub.ReleaseHeld(context.Background(), sub.Input.Identity.Scope, old.InputID); !errors.Is(err, store.ErrInputConflict) {
		t.Fatalf("同一句不能放行两次：%v", err)
	}
	if held, _ := h.hub.HeldInputs(context.Background()); len(held) != 0 {
		t.Fatalf("放行后不该再列出：%+v", held)
	}
}

func TestDiscardHeldKeepsRecord(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	sub, old := heldAfterRestart(t, h, "算了的那句", "held-2")
	before := h.bodyCount()
	if err := h.hub.DiscardHeld(context.Background(), sub.Input.Identity.Scope, old.InputID); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, h, old.InputID); got != kernel.InputCancelled {
		t.Fatalf("放弃后应当是已取消：%s", got)
	}
	var n int
	if err := h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE text=?`, "算了的那句").Scan(&n); err != nil || n != 1 {
		t.Fatalf("原文记录必须保留：%d %v", n, err)
	}
	row, _ := h.st.KernelInputByID(context.Background(), old.InputID)
	if !strings.Contains(row.Payload, "算了的那句") {
		t.Fatal("输入行的载荷也要保留")
	}
	if _, err := h.hub.ReleaseHeld(context.Background(), sub.Input.Identity.Scope, old.InputID); !errors.Is(err, store.ErrInputConflict) {
		t.Fatalf("放弃后不能再放行：%v", err)
	}
	if err := h.hub.DiscardHeld(context.Background(), sub.Input.Identity.Scope, old.InputID); !errors.Is(err, store.ErrInputConflict) {
		t.Fatalf("重复放弃要报错：%v", err)
	}
	if h.bodyCount() != before {
		t.Fatal("放弃不该触发模型请求")
	}
}

func TestWithdrawQueuedBeforeItsTurn(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var first atomic.Bool
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`, onBody: func(ctx context.Context, body string) {
		if strings.Contains(body, "前一句") && first.CompareAndSwap(false, true) {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}})
	conv := consoleConv(h.c.CharacterID)
	a := lifecycleSubmission(t, h, conv, "前一句", "wq-a", kernel.InputStart)
	ra, err := h.hub.Submit(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	q := lifecycleSubmission(t, h, conv, "排队后撤回的一句", "wq-b", kernel.InputQueue)
	rq, err := h.hub.Submit(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	scope := a.Input.Identity.Scope
	if err := h.hub.WithdrawQueued(context.Background(), scope, rq.InputID); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, h, rq.InputID); got != kernel.InputCancelled {
		t.Fatalf("撤回后应当是已取消：%s", got)
	}
	close(release)
	lifecycleWait(t, h.hub, scope, ra.RunID)
	snap := lifecycleWait(t, h.hub, scope, rq.RunID)
	if snap.Outcome != kernel.OutcomeStopped {
		t.Fatalf("撤回的那一轮不该回复：%+v", snap)
	}
	if n := h.bodyCount(); n != 1 {
		t.Fatalf("撤回的话不该触发请求：%d 次", n)
	}
	// 已经开始处理的撤不回
	if err := h.hub.WithdrawQueued(context.Background(), scope, ra.InputID); !errors.Is(err, ErrInputNotPending) {
		t.Fatalf("已开始的话撤不回：%v", err)
	}
}

func TestRunReasonFromRealRun(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
		http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
		return true
	}})
	sub := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "你好", "reason-1", kernel.InputStart)
	rec, err := h.hub.Submit(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	snap := lifecycleWait(t, h.hub, sub.Input.Identity.Scope, rec.RunID)
	if snap.Outcome != kernel.OutcomeError || snap.Reason.Class != llm.ClassModelUnknown || snap.Reason.Detail == "" {
		t.Fatalf("模型不存在应当是 model_unknown 并带原文：%+v", snap)
	}
	// 正常结束没有原因
	h2 := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	s2 := lifecycleSubmission(t, h2, consoleConv(h2.c.CharacterID), "你好", "reason-2", kernel.InputStart)
	r2, _ := h2.hub.Submit(context.Background(), s2)
	if snap := lifecycleWait(t, h2.hub, s2.Input.Identity.Scope, r2.RunID); snap.Reason != (RunReason{}) {
		t.Fatalf("正常结束不该有原因：%+v", snap.Reason)
	}
}

func TestReasonOfBranches(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	cases := []struct {
		state kernel.Outcome
		err   error
		want  string
	}{
		{kernel.OutcomeBudgetExhausted, nil, "budget_exhausted"},
		{kernel.OutcomeError, context.DeadlineExceeded, llm.ClassTimeout},
		{kernel.OutcomeError, errors.New("429: rate limit exceeded"), llm.ClassRateLimited},
		{kernel.OutcomeError, errors.New("dial tcp: connection refused"), llm.ClassNetwork},
		{kernel.OutcomeError, errors.New("谁也没见过的错"), "unknown"},
		{kernel.OutcomeStopped, nil, ""},
		{kernel.OutcomeDone, nil, ""},
	}
	for _, c := range cases {
		if got := h.hub.reasonOf(c.state, c.err, nil).Class; got != c.want {
			t.Errorf("%s %v：%q，应是 %q", c.state, c.err, got, c.want)
		}
	}
	h.hub.lifecycle.sealed.Store(true)
	if got := h.hub.reasonOf(kernel.OutcomeStopped, nil, nil).Class; got != ReasonExit {
		t.Errorf("退出时被切断应当是 exit：%q", got)
	}
	long := strings.Repeat("长", detailRunes+50)
	if d := h.hub.reasonOf(kernel.OutcomeError, errors.New(long), nil).Detail; len([]rune(d)) > detailRunes+1 {
		t.Errorf("详情要截短：%d", len([]rune(d)))
	}
}
