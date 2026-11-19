package turn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/kernel"
)

// 独立复核（2026-11-15）补的回归测试。

// 故障错误的原因链里带 context.Canceled 时，排着的下一轮照样不调模型：控制流看单独的“已拒”标记，不看 err；
// FaultError 也不能被 errors.Is 认成取消。
func TestFaultErrorWithCanceledCauseStillRejectsQueuedRun(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`,
		onBody: func(ctx context.Context, body string) {
			if strings.Contains(body, "编造第一句") {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		},
	})
	conv := consoleConv(h.c.CharacterID)
	firstRec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造第一句", "q1", kernel.InputStart))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, entered, "第一轮开始")
	secondRec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造第二句", "q2", kernel.InputQueue))
	if err != nil {
		t.Fatal(err)
	}
	lc := h.hub.lifecycle
	lc.mu.Lock()
	lc.raiseLocked(h.hub.Cfg.Now(), FaultScopeConversation, h.c.CharacterID, conv, FaultRequeue, context.Canceled)
	lc.mu.Unlock()
	close(release)
	recv(t, inputRunOf(t, h.hub, firstRec).outcome, "第一轮")
	out2 := recv(t, inputRunOf(t, h.hub, secondRec).outcome, "排着的第二轮")
	h.mu.Lock()
	requests := len(h.bodies)
	h.mu.Unlock()
	if requests != 1 {
		t.Fatalf("故障会话里排着的下一轮调了模型：请求 %d 次", requests)
	}
	if !errors.Is(out2.Err, ErrConversationFaulted) || errors.Is(out2.Err, context.Canceled) || errors.Is(out2.Err, context.DeadlineExceeded) {
		t.Fatalf("被故障拒掉的这一轮该是故障错误，不能被认成取消：%v", out2.Err)
	}
}

// 范围靠显式字段，不靠会话号是不是空：一个空会话号的故障（假设它进得来）也只是会话故障；
// 而 validateInput 本来就拒空会话号，这样的输入根本进不了内核。
func TestFaultScopeIsExplicitAndEmptyConversationIsRejected(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	lc := h.hub.lifecycle
	lc.mu.Lock()
	lc.raiseLocked(h.hub.Cfg.Now(), FaultScopeConversation, h.c.CharacterID, "", FaultRunState, errors.New("made"))
	lc.mu.Unlock()
	f, ok := h.hub.FaultOf(h.c.CharacterID, "")
	if !ok || f.Kernel() || f.Scope != FaultScopeConversation {
		t.Fatalf("空会话号的故障也是会话故障：%+v %v", f, ok)
	}
	if _, ok := h.hub.FaultOf(h.c.CharacterID, consoleConv(h.c.CharacterID)); ok {
		t.Fatal("别的会话不该被它拖住")
	}
	if fs := h.hub.Faults(); len(fs) != 1 || fs[0].Kernel() {
		t.Fatalf("不该出现内核故障：%+v", fs)
	}
	in := consoleInput(h.c.CharacterID, 0, "x")
	in.Conversation, in.Audience.Conversation = "", ""
	if _, err := h.hub.trustedInput(in); !errors.Is(err, ErrInputAuthority) {
		t.Fatalf("空会话号的输入该被拒：%v", err)
	}
	s := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "x", "e1", kernel.InputStart)
	s.Input.Conversation, s.Input.Identity.Scope.Conversation, s.Input.Audience.Conversation = "", "", ""
	if _, err := h.hub.Submit(t.Context(), s); !errors.Is(err, ErrInputAuthority) {
		t.Fatalf("Submit 该拒空会话号：%v", err)
	}
}

// 开始时“准入”写不进：输入行还停在 accepted，运行却要落定了。这个会话进故障；同键重发在恢复之前被拒（不回一张死运行的成功回执），
// 重试后这句话是 held，重发拿到的回执明说是 held。
func TestAdmitWriteFailureFaultsConversationAndSameKeyResendIsNotSuccess(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	conv := consoleConv(h.c.CharacterID)
	h.exec(`CREATE TRIGGER made_admit BEFORE UPDATE ON kernel_inbox WHEN NEW.state='admitted' BEGIN SELECT RAISE(ABORT,'made-admit-failure'); END`)
	s := lifecycleSubmission(t, h, conv, "编造准入失败", "adm-1", kernel.InputStart)
	rec, err := h.hub.Submit(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	out := recv(t, inputRunOf(t, h.hub, rec).outcome, "这一轮")
	if out.Err == nil || !strings.Contains(out.Err.Error(), "made-admit-failure") {
		t.Fatalf("这一轮该如实报准入写库失败：%v", out.Err)
	}
	h.exec(`DROP TRIGGER made_admit`)
	if f, ok := h.hub.FaultOf(h.c.CharacterID, conv); !ok || f.Kind != FaultAdmit {
		t.Fatalf("该进会话故障（admit）：%+v %v", f, ok)
	}
	if _, err := h.hub.Submit(t.Context(), s); !errors.Is(err, ErrConversationFaulted) {
		t.Fatalf("同键重发在恢复之前该被拒，不能回成功：%v", err)
	}
	if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, conv); err != nil {
		t.Fatalf("重试该成功：%v", err)
	}
	row, err := h.st.KernelInputByID(t.Context(), rec.InputID)
	if err != nil || row.State != kernel.InputHeld {
		t.Fatalf("重试后这句话该是 held：%v %v", row.State, err)
	}
	again, err := h.hub.Submit(t.Context(), s)
	if err != nil || again.State != kernel.InputHeld {
		t.Fatalf("同键重发的回执该明说是 held：%+v %v", again, err)
	}
}

// 重复提交的认领（claimQueuedInput）自己也查故障：直接测这个检查点，不靠 executeInput 那一层兜底。
func TestClaimQueuedInputChecksFault(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	conv := consoleConv(h.c.CharacterID)
	lc := h.hub.lifecycle
	lc.mu.Lock()
	lc.raiseLocked(h.hub.Cfg.Now(), FaultScopeConversation, h.c.CharacterID, conv, FaultRunState, errors.New("made"))
	lc.mu.Unlock()
	s := lifecycleSubmission(t, h, conv, "认领", "claim-1", kernel.InputQueue)
	_, claimed, err := h.hub.claimQueuedInput(t.Context(), s.Input, "claim-1")
	if !claimed || !errors.Is(err, ErrConversationFaulted) {
		t.Fatalf("认领该被故障拦下：claimed=%v err=%v", claimed, err)
	}
	// 别的会话不受影响：没有这一行，认领说“不是我管的”
	if _, claimed, err := h.hub.claimQueuedInput(t.Context(), groupSubmission(t, h, "群里", "claim-2").Input, "claim-2"); claimed || err != nil {
		t.Fatalf("别的会话没有故障，认领该放过：claimed=%v err=%v", claimed, err)
	}
}

// 没人认领的活动退出失败（不属于任何会话的故障，比如压缩、检查点、记忆这类活动）：不挡受理、不进会话故障，
// 但数得出来（给自检）、RetryAllFaults 会一起重写，写成才清掉，Drain 也才干净。
func TestStrayActivityEndFailureIsCountedAndRetried(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	h.exec(`CREATE TRIGGER made_stray BEFORE UPDATE ON kernel_activities WHEN NEW.state='exited' BEGIN SELECT RAISE(ABORT,'made-stray-failure'); END`)
	_, act, err := h.hub.lifecycle.manager.Begin(t.Context(), kernel.Invocation{RunID: "made-stray-run", Kind: kernel.KindCompact})
	if err != nil {
		t.Fatal(err)
	}
	act.End(nil)
	if n := h.hub.StrayEndFailures(); n != 1 {
		t.Fatalf("没人认领的退出失败该数出 1 条：%d", n)
	}
	if len(h.hub.Faults()) != 0 {
		t.Fatalf("它不挡受理，不进会话故障：%+v", h.hub.Faults())
	}
	if err := h.hub.Drain(t.Context()); err == nil {
		t.Fatal("没写成的退出记录还在，Drain 不该说干净")
	}
	rep, err := h.hub.RetryAllFaults(t.Context())
	if err == nil || !strings.Contains(err.Error(), "made-stray-failure") || rep.StrayEnds != 1 {
		t.Fatalf("库还坏时重试该如实失败并报条数：%+v %v", rep, err)
	}
	h.exec(`DROP TRIGGER made_stray`)
	rep, err = h.hub.RetryAllFaults(t.Context())
	if err != nil || rep.StrayEnds != 0 || h.hub.StrayEndFailures() != 0 {
		t.Fatalf("库好了重试该清掉：%+v %v", rep, err)
	}
	if err := h.hub.Drain(t.Context()); err != nil {
		t.Fatalf("清掉以后 Drain 该干净：%v", err)
	}
}
