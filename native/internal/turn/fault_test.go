package turn

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
)

// groupSubmission 造一条 QQ 群里的输入：和本地私聊是同一个角色下的另一个会话。
func groupSubmission(t *testing.T, h *harness, text, key string) Submission {
	t.Helper()
	in, err := h.hub.trustedInput(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{h.storeGroup(text)}, Text: text,
		Audience: audience.Audience{Channel: "qq", Scene: "group", Conversation: groupConv}, Scope: "public", Addressed: true})
	if err != nil {
		t.Fatal(err)
	}
	return Submission{Input: in, Mode: kernel.InputQueue, ClientKey: key}
}

func (h *harness) exec(query string) {
	h.t.Helper()
	if _, err := h.st.Trace.Exec(query); err != nil {
		h.t.Fatal(err)
	}
}

// 两种收尾写库失败：一轮的状态写不进去、活动退出写不进去。
// stillBroken：库还坏着时重试是否该失败。收尾中间态（draining）写不进、落定（settled）已经写成的那种，库里这一轮已经落定，
// 重试读回确认后直接解除，不算“没恢复”；其余的重写仍会撞上同一个失败。
var faultInjections = []struct {
	name, create, drop, marker string
	kind                       FaultKind
	stillBroken                bool
}{
	{"收尾中间态", `CREATE TRIGGER made_fault BEFORE UPDATE ON kernel_runs WHEN NEW.state='draining' BEGIN SELECT RAISE(ABORT,'made-draining-failure'); END`, `DROP TRIGGER made_fault`, "made-draining-failure", FaultRunState, false},
	{"一轮的落定", `CREATE TRIGGER made_fault BEFORE UPDATE ON kernel_runs WHEN NEW.state='settled' BEGIN SELECT RAISE(ABORT,'made-settled-failure'); END`, `DROP TRIGGER made_fault`, "made-settled-failure", FaultRunState, true},
	{"活动退出", `CREATE TRIGGER made_fault BEFORE UPDATE ON kernel_activities WHEN NEW.state='exited' BEGIN SELECT RAISE(ABORT,'made-activity-failure'); END`, `DROP TRIGGER made_fault`, "made-activity-failure", FaultActivityEnd, true},
}

// 质检 X-07 的基线翻过来：一个会话收尾写库失败，只有它自己停止受理，别的会话照常；重试成功才恢复。
// 旧代码（lc.err 只增不减）下，下面“别的会话”那一步会被旧错误拒收。
func TestFaultOnlyBlocksTheFaultedConversation(t *testing.T) {
	t.Parallel()
	for _, inj := range faultInjections {
		t.Run(inj.name, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`})
			conv := consoleConv(h.c.CharacterID)
			h.exec(inj.create)
			first := lifecycleSubmission(t, h, conv, "编造收尾失败", "fault-first", kernel.InputStart)
			rec, err := h.hub.Submit(t.Context(), first)
			if err != nil {
				t.Fatal(err)
			}
			out := recv(t, inputRunOf(t, h.hub, rec).outcome, "写库失败的本轮结果")
			if out.Err == nil || !strings.Contains(out.Err.Error(), inj.marker) {
				t.Fatalf("本轮没有如实报收尾写库失败：%v", out.Err)
			}
			// 库恢复了（比如那一瞬间的 SQLITE_BUSY 过去了）；故障不会自己消失
			h.exec(inj.drop)
			faults := h.hub.Faults()
			if len(faults) != 1 || faults[0].Kind != inj.kind || faults[0].Conversation != conv || faults[0].CharacterID != h.c.CharacterID || faults[0].Kernel() {
				t.Fatalf("故障记录不对：%+v", faults)
			}
			if f, ok := h.hub.FaultOf(h.c.CharacterID, conv); !ok || f.Reason == "" || f.At.IsZero() {
				t.Fatalf("会话自己的故障没有原因或时间：%+v %v", f, ok)
			}

			// 别的会话（QQ 群）照常受理，这一轮本身没有带着旧错误
			if _, ok := h.hub.FaultOf(h.c.CharacterID, groupConv); ok {
				t.Fatal("别的会话不该受影响")
			}
			grec, err := h.hub.Submit(t.Context(), groupSubmission(t, h, "另一个会话的话", "fault-group"))
			if err != nil {
				t.Fatalf("别的会话被拒了：%v", err)
			}
			if gout := recv(t, inputRunOf(t, h.hub, grec).outcome, "别的会话这一轮"); gout.Err != nil {
				t.Fatalf("别的会话这一轮带着旧错误：%v", gout.Err)
			}

			// 出事的会话在恢复之前，每一种入口都被拒，拒的话如实说是这个会话的故障
			for _, c := range []struct {
				name string
				s    Submission
			}{
				{"开始", lifecycleSubmission(t, h, conv, "新的一句", "fault-start", kernel.InputStart)},
				{"排队", lifecycleSubmission(t, h, conv, "排队的一句", "fault-queue", kernel.InputQueue)},
				{"插话", func() Submission {
					s := lifecycleSubmission(t, h, conv, "插的一句", "fault-steer", kernel.InputSteer)
					s.ExpectedRunID = rec.RunID
					return s
				}()},
				{"重复提交已受理的那句", first},
			} {
				_, err := h.hub.Submit(t.Context(), c.s)
				if !errors.Is(err, ErrConversationFaulted) || errors.Is(err, ErrKernelFaulted) || !strings.Contains(err.Error(), inj.marker) {
					t.Fatalf("%s：出事的会话该被如实拒收，实际 %v", c.name, err)
				}
			}

			// 库还是坏的时候重试：没恢复就明说，故障保持，原因更新
			h.exec(inj.create)
			if inj.stillBroken {
				err = h.hub.RetryFault(t.Context(), h.c.CharacterID, conv)
				if err == nil || !errors.Is(err, ErrConversationFaulted) || !strings.Contains(err.Error(), "没有恢复") || !strings.Contains(err.Error(), inj.marker) {
					t.Fatalf("重试没成功却没有说没恢复：%v", err)
				}
				if f, ok := h.hub.FaultOf(h.c.CharacterID, conv); !ok || f.Tries != 1 {
					t.Fatalf("重试失败后故障该保持、记一次重试：%+v %v", f, ok)
				}
				if _, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "重试失败后的一句", "fault-after-failed", kernel.InputStart)); !errors.Is(err, ErrConversationFaulted) {
					t.Fatalf("重试失败后仍该拒收：%v", err)
				}
			}

			// 库好了再重试：读回确认后恢复受理
			h.exec(inj.drop)
			if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, conv); err != nil {
				t.Fatalf("库恢复后重试该成功：%v", err)
			}
			if len(h.hub.Faults()) != 0 {
				t.Fatalf("重试成功后故障该清掉：%+v", h.hub.Faults())
			}
			state, outcome, drained, err := h.st.KernelRunRow(t.Context(), out.TurnID)
			if err != nil || state != kernel.RunSettled || outcome != kernel.OutcomeDone || !drained {
				t.Fatalf("重试后库里这一轮该是 settled/done/drained：%s %s %v %v", state, outcome, drained, err)
			}
			var running int
			if err := h.st.Trace.QueryRow(`SELECT count(*) FROM kernel_activities WHERE state='running'`).Scan(&running); err != nil || running != 0 {
				t.Fatalf("重试后不该还有记成运行中的活动：%d %v", running, err)
			}
			again, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "恢复后的一句", "fault-recovered", kernel.InputStart))
			if err != nil {
				t.Fatalf("恢复后该受理：%v", err)
			}
			if aout := recv(t, inputRunOf(t, h.hub, again).outcome, "恢复后的这一轮"); aout.Err != nil {
				t.Fatal(aout.Err)
			}
			if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, conv); !errors.Is(err, ErrNoFault) {
				t.Fatalf("没有故障时重试该说没有：%v", err)
			}
		})
	}
}

// 排在出事会话里的下一轮不开始：如实报这个会话的故障，话在库里改成 held（和重启恢复同一个结果），
// 重试成功后主人可以放行；故障期间放行也被拒。
func TestFaultQueuedNextRunIsHeldNotRun(t *testing.T) {
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
	second := lifecycleSubmission(t, h, conv, "编造第二句", "q2", kernel.InputQueue)
	secondRec, err := h.hub.Submit(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	h.exec(faultInjections[1].create)
	close(release)
	out1 := recv(t, inputRunOf(t, h.hub, firstRec).outcome, "第一轮结果")
	out2 := recv(t, inputRunOf(t, h.hub, secondRec).outcome, "排队那一轮的结果")
	if out1.Err == nil || !strings.Contains(out1.Err.Error(), "made-settled-failure") {
		t.Fatalf("第一轮没有如实报收尾写库失败：%v", out1.Err)
	}
	if !errors.Is(out2.Err, ErrConversationFaulted) {
		t.Fatalf("排队那一轮该如实报这个对话的故障，不是别的：%v", out2.Err)
	}
	h.mu.Lock()
	requests := len(h.bodies)
	h.mu.Unlock()
	if requests != 1 {
		t.Fatalf("出事会话排着的下一轮不该调模型：请求 %d 次", requests)
	}
	// 故障期间不放行 held（这时那句话还是 accepted，没有 held，放行也被拒）
	if _, err := h.hub.ReleaseHeld(t.Context(), second.Input.Identity.Scope, secondRec.InputID); !errors.Is(err, ErrConversationFaulted) {
		t.Fatalf("故障期间放行该被拒：%v", err)
	}
	h.exec(faultInjections[1].drop)
	if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, conv); err != nil {
		t.Fatalf("重试该成功：%v", err)
	}
	row, err := h.st.KernelInputByID(t.Context(), secondRec.InputID)
	if err != nil || row.State != kernel.InputHeld {
		t.Fatalf("排队那句话该记成 held：%v %v", row.State, err)
	}
	held, err := h.hub.HeldInputs(t.Context())
	if err != nil || len(held) != 1 || held[0].Text != "编造第二句" {
		t.Fatalf("主人该在没处理的话里看到它：%+v %v", held, err)
	}
	rel, err := h.hub.ReleaseHeld(t.Context(), second.Input.Identity.Scope, secondRec.InputID)
	if err != nil {
		t.Fatalf("恢复后该能放行：%v", err)
	}
	if out := recv(t, inputRunOf(t, h.hub, rel).outcome, "放行后的这一轮"); out.Err != nil {
		t.Fatal(out.Err)
	}
}

// 内核故障（启动时整库的恢复没做成）：所有会话都被拒，重试成功才恢复；还有运行在跑时不能重试（再恢复一次会把在跑的当成没收尾的）。
func TestKernelFaultBlocksEveryConversationUntilRetried(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	conv := consoleConv(h.c.CharacterID)
	lc := h.hub.lifecycle
	lc.mu.Lock()
	lc.raiseLocked(h.hub.Cfg.Now(), FaultScopeKernel, "", "", FaultStartupRecover, errors.New("made-recover-failure"), repair{kind: repairRecover})
	lc.mu.Unlock()
	for _, c := range []struct {
		name string
		s    Submission
	}{
		{"本地私聊", lifecycleSubmission(t, h, conv, "编造本地一句", "k-local", kernel.InputStart)},
		{"QQ 群", groupSubmission(t, h, "编造群里一句", "k-group")},
	} {
		if _, err := h.hub.Submit(t.Context(), c.s); !errors.Is(err, ErrKernelFaulted) || errors.Is(err, ErrConversationFaulted) || !strings.Contains(err.Error(), "made-recover-failure") {
			t.Fatalf("%s：内核故障该拒收并如实说：%v", c.name, err)
		}
	}
	if f := h.hub.Faults(); len(f) != 1 || !f[0].Kernel() {
		t.Fatalf("该有一条内核故障：%+v", f)
	}
	if f, ok := h.hub.FaultOf(h.c.CharacterID, conv); !ok || !f.Kernel() {
		t.Fatalf("每个会话都受内核故障影响：%+v %v", f, ok)
	}
	if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, ""); err == nil {
		t.Fatal("只给角色不给会话该报参数不对")
	}
	// 还有活动没退出：不能重做整库的恢复
	_, act, err := lc.manager.Begin(t.Context(), kernel.Invocation{RunID: "made-live", Kind: kernel.KindCompact})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.hub.RetryFault(t.Context(), "", ""); !errors.Is(err, ErrRetryBusy) {
		t.Fatalf("有活动在跑时不该重试整库恢复：%v", err)
	}
	act.End(nil)
	if err := h.hub.RetryFault(t.Context(), "", ""); err != nil {
		t.Fatalf("没有活动在跑时重试该成功：%v", err)
	}
	if len(h.hub.Faults()) != 0 {
		t.Fatalf("重试成功后该清掉：%+v", h.hub.Faults())
	}
	rec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造恢复后一句", "k-after", kernel.InputStart))
	if err != nil {
		t.Fatalf("恢复后该受理：%v", err)
	}
	recv(t, inputRunOf(t, h.hub, rec).outcome, "恢复后这一轮")
}

// 重试正在做的时候再重试：说忙，不并发做两遍。
func TestFaultRetryIsNotReentrant(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	lc := h.hub.lifecycle
	lc.mu.Lock()
	lc.raiseLocked(h.hub.Cfg.Now(), FaultScopeConversation, h.c.CharacterID, "console:made", FaultRunState, errors.New("made"), repair{kind: repairHold, input: "made-input"})
	lc.faults[faultKey(h.c.CharacterID, "console:made")].retrying = true
	lc.mu.Unlock()
	if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, "console:made"); !errors.Is(err, ErrRetryBusy) {
		t.Fatalf("正在重试时再重试该说忙：%v", err)
	}
}

// 只拒这一条：中途插进来的话载荷读不出，只把这一条标成已取消、界面看到它被丢了；不进故障，会话照常。
func TestBadSteerPayloadRejectsOnlyThatInput(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `]`,
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
	rec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造第一句", "s1", kernel.InputStart))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, entered, "第一轮开始")
	steer := lifecycleSubmission(t, h, conv, "插进来的一句", "s2", kernel.InputSteer)
	steer.ExpectedRunID = rec.RunID
	srec, err := h.hub.Submit(t.Context(), steer)
	if err != nil {
		t.Fatal(err)
	}
	// 载荷坏了：内存里排着的那份和库里的都读不出
	lc := h.hub.lifecycle
	lc.mu.Lock()
	for i := range lc.steers[rec.RunID] {
		lc.steers[rec.RunID][i].Payload = "{坏"
	}
	lc.mu.Unlock()
	r := inputRunOf(t, h.hub, rec)
	lc.mu.Lock()
	events := h.hub.requeueSteersLocked(r)
	lc.mu.Unlock()
	if len(events) != 1 || events[0].Phase != "dropped" || events[0].ID != srec.InputID || events[0].Requeue {
		t.Fatalf("界面该看到这一条被丢了：%+v", events)
	}
	if row, err := h.st.KernelInputByID(t.Context(), srec.InputID); err != nil || row.State != kernel.InputCancelled {
		t.Fatalf("坏载荷那一条该标成已取消：%v %v", row.State, err)
	}
	if f := h.hub.Faults(); len(f) != 0 {
		t.Fatalf("单条数据坏了不该进故障：%+v", f)
	}
	close(release)
	if out := recv(t, r.outcome, "第一轮结果"); out.Err != nil {
		t.Fatal(out.Err)
	}
	next, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "后面的一句", "s3", kernel.InputStart))
	if err != nil {
		t.Fatalf("会话该照常受理：%v", err)
	}
	recv(t, inputRunOf(t, h.hub, next).outcome, "后面的一句")
}

// 插话重排写不进库：库里这句话还停在旧运行上，这个会话进故障；重试把它改成 held，主人能看到。
// 已经不是待处理的那种冲突（ErrInputConflict）只拒这一条，不进故障。
func TestSteerRequeueWriteFailureFaultsOnlyThatConversation(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `]`,
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
	rec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造第一句", "r1", kernel.InputStart))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, entered, "第一轮开始")
	steer := lifecycleSubmission(t, h, conv, "插进来的一句", "r2", kernel.InputSteer)
	steer.ExpectedRunID = rec.RunID
	srec, err := h.hub.Submit(t.Context(), steer)
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`CREATE TRIGGER made_requeue BEFORE UPDATE ON kernel_inbox WHEN NEW.mode='queue' AND NEW.state='accepted' BEGIN SELECT RAISE(ABORT,'made-requeue-failure'); END`)
	r := inputRunOf(t, h.hub, rec)
	lc := h.hub.lifecycle
	lc.mu.Lock()
	events := h.hub.requeueSteersLocked(r)
	lc.mu.Unlock()
	if len(events) != 1 || events[0].Phase != "dropped" || events[0].Text != "插进来的一句" {
		t.Fatalf("界面该看到这句没排上：%+v", events)
	}
	f, ok := h.hub.FaultOf(h.c.CharacterID, conv)
	if !ok || f.Kind != FaultRequeue || !strings.Contains(f.Detail, "made-requeue-failure") {
		t.Fatalf("重排写不进该让这个会话进故障：%+v %v", f, ok)
	}
	if _, ok := h.hub.FaultOf(h.c.CharacterID, groupConv); ok {
		t.Fatal("别的会话不该受影响")
	}
	close(release)
	recv(t, r.outcome, "第一轮结果")
	h.exec(`DROP TRIGGER made_requeue`)
	if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, conv); err != nil {
		t.Fatalf("重试该成功：%v", err)
	}
	if row, err := h.st.KernelInputByID(t.Context(), srec.InputID); err != nil || row.State != kernel.InputHeld {
		t.Fatalf("没排上的那句该记成 held：%v %v", row.State, err)
	}
}

// 修复项可以重做：已经落定的运行、已经处理过的话再来一遍，不报错也不改状态；真处于 accepted 的话才改成 held 并读回。
func TestFaultRepairsAreIdempotent(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	rec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "编造一句", "v1", kernel.InputStart))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, inputRunOf(t, h.hub, rec).outcome, "这一轮")
	// 这一行已经 settled/drained：重写不改它，读回是已落定
	err = h.hub.redo(t.Context(), h.hub.trace(), repair{kind: repairRunState, run: rec.RunID, state: kernel.RunRunning})
	if err != nil {
		t.Fatalf("已经 drained 的行视为已落定：%v", err)
	}
	// 一句已经被处理（admitted）的话要求 hold：不是 accepted，不动它，读回不是 accepted，算对得上
	if err := h.hub.redo(t.Context(), h.hub.trace(), repair{kind: repairHold, input: rec.InputID}); err != nil {
		t.Fatalf("已经处理过的话不该再被 hold：%v", err)
	}
	row, err := h.st.KernelInputByID(t.Context(), rec.InputID)
	if err != nil || row.State == kernel.InputHeld {
		t.Fatalf("处理过的话不该变成 held：%v %v", row.State, err)
	}
	// 把它硬改回 accepted 再 hold：该成功读回 held
	h.exec(`UPDATE kernel_inbox SET state='accepted' WHERE input_id='` + rec.InputID + `'`)
	if err := h.hub.redo(t.Context(), h.hub.trace(), repair{kind: repairHold, input: rec.InputID}); err != nil {
		t.Fatal(err)
	}
	if row, _ := h.st.KernelInputByID(t.Context(), rec.InputID); row.State != kernel.InputHeld {
		t.Fatalf("该读回 held：%v", row.State)
	}
}

// 重试是主人和运维的操作：模型拿到的工具清单（本地私聊和 QQ 群各看一次）和登记过的全部工具名里都没有它。
func TestRetryFaultIsNotInAnyModelToolList(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	recv(t, h.hub.Send(h.c.CharacterID, "编造本地一句"), "本地私聊这一轮")
	recv(t, h.inGroup("编造群里一句", nil), "QQ 群这一轮")
	deny := regexp.MustCompile(`(?i)retry|fault|kernel|故障|重试`)
	seen := 0
	for i := 0; i < 2; i++ {
		for _, name := range h.toolNames(i) {
			seen++
			if deny.MatchString(name) {
				t.Errorf("第 %d 次请求的工具清单里有重试相关的工具：%s", i+1, name)
			}
		}
	}
	for name := range policy.Specs {
		seen++
		if deny.MatchString(name) {
			t.Errorf("登记过的工具里有重试相关的：%s", name)
		}
	}
	if seen < 10 {
		t.Fatalf("工具清单是空的，这条测试没有意义：看了 %d 个名字", seen)
	}
}

const privateConv = "qq:private:1001:2002"

// qqPrivateSubmission 造一条 QQ 私聊里的输入（接收账号 1001，对方 2002）。
func qqPrivateSubmission(t *testing.T, h *harness, text, key string) Submission {
	t.Helper()
	seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: privateConv, ActorKind: "human", ActorID: "2002", Origin: "human", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
	if err != nil {
		t.Fatal(err)
	}
	in, err := h.hub.trustedInput(Input{CharacterID: h.c.CharacterID, Conversation: privateConv, Seqs: []int64{seq}, Text: text,
		Audience: audience.Audience{Channel: "qq", Scene: "private", Conversation: privateConv}, Scope: "public", Addressed: true})
	if err != nil {
		t.Fatal(err)
	}
	return Submission{Input: in, Mode: kernel.InputQueue, ClientKey: key}
}

// 出事的会话是 QQ 群或 QQ 私聊时同样只挡它自己：另一个 QQ 会话和本地私聊照常，重试成功才恢复。
func TestFaultInQQConversationsOnlyBlocksThatOne(t *testing.T) {
	t.Parallel()
	shapes := []struct {
		name, conv string
		sub, other func(*testing.T, *harness, string, string) Submission
	}{
		{"QQ 群", groupConv, groupSubmission, qqPrivateSubmission},
		{"QQ 私聊", privateConv, qqPrivateSubmission, groupSubmission},
	}
	inj := faultInjections[1]
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `,` + textReply + `]`})
			h.exec(inj.create)
			rec, err := h.hub.Submit(t.Context(), sh.sub(t, h, "编造收尾失败", "qq-first"))
			if err != nil {
				t.Fatal(err)
			}
			out := recv(t, inputRunOf(t, h.hub, rec).outcome, "写库失败的本轮结果")
			if out.Err == nil || !strings.Contains(out.Err.Error(), inj.marker) {
				t.Fatalf("本轮没有如实报收尾写库失败：%v", out.Err)
			}
			h.exec(inj.drop)
			if f, ok := h.hub.FaultOf(h.c.CharacterID, sh.conv); !ok || f.Conversation != sh.conv || f.Kernel() {
				t.Fatalf("出事的是这个会话：%+v %v", f, ok)
			}
			// 另一个 QQ 会话、本地私聊照常
			orec, err := h.hub.Submit(t.Context(), sh.other(t, h, "另一个 QQ 会话的话", "qq-other"))
			if err != nil {
				t.Fatalf("另一个 QQ 会话被拒了：%v", err)
			}
			if o := recv(t, inputRunOf(t, h.hub, orec).outcome, "另一个 QQ 会话这一轮"); o.Err != nil {
				t.Fatal(o.Err)
			}
			if o := recv(t, h.hub.Send(h.c.CharacterID, "本地私聊的话"), "本地私聊这一轮"); o.Err != nil {
				t.Fatalf("本地私聊被带着拒了：%v", o.Err)
			}
			// 出事的会话恢复之前被拒，重试成功后受理
			if _, err := h.hub.Submit(t.Context(), sh.sub(t, h, "出事会话的下一句", "qq-next")); !errors.Is(err, ErrConversationFaulted) {
				t.Fatalf("出事的会话在恢复前该被拒：%v", err)
			}
			if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, sh.conv); err != nil {
				t.Fatalf("重试该成功：%v", err)
			}
			rec2, err := h.hub.Submit(t.Context(), sh.sub(t, h, "恢复后的话", "qq-after"))
			if err != nil {
				t.Fatalf("恢复后该受理：%v", err)
			}
			recv(t, inputRunOf(t, h.hub, rec2).outcome, "恢复后这一轮")
		})
	}
}

// 重试到一半（或还没重试）程序退出：故障只在内存里，新进程没有故障；启动时的恢复把上个进程没落定的运行核对掉，会话照常受理。
func TestFaultDoesNotSurviveRestartAndStartupRecoversRows(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	conv := consoleConv(h.c.CharacterID)
	inj := faultInjections[1]
	h.exec(inj.create)
	rec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造收尾失败", "restart-1", kernel.InputStart))
	if err != nil {
		t.Fatal(err)
	}
	out := recv(t, inputRunOf(t, h.hub, rec).outcome, "写库失败的本轮结果")
	h.exec(inj.drop)
	if _, ok := h.hub.FaultOf(h.c.CharacterID, conv); !ok {
		t.Fatal("先要有故障")
	}
	if state, _, drained, err := h.st.KernelRunRow(t.Context(), out.TurnID); err != nil || drained || state == kernel.RunSettled {
		t.Fatalf("库里这一轮该还没落定：%s %v %v", state, drained, err)
	}
	r := restart(t, h, harnessOpts{replies: `[` + textReply + `]`})
	if f := r.hub.Faults(); len(f) != 0 {
		t.Fatalf("新进程不该带着故障：%+v", f)
	}
	state, outcome, drained, err := r.st.KernelRunRow(t.Context(), out.TurnID)
	if err != nil || state != kernel.RunSettled || !drained || outcome != kernel.OutcomeDone {
		t.Fatalf("启动恢复该把没落定的这一轮记成落定（结局保留 done）：%s %s %v %v", state, outcome, drained, err)
	}
	next, err := r.hub.Submit(t.Context(), lifecycleSubmission(t, r, conv, "重启后的一句", "restart-2", kernel.InputStart))
	if err != nil {
		t.Fatalf("重启后该受理：%v", err)
	}
	if o := recv(t, inputRunOf(t, r.hub, next).outcome, "重启后这一轮"); o.Err != nil {
		t.Fatal(o.Err)
	}
}

// 开始时写库失败（这一轮的“开始运行”写不进）：只拒这一条、如实报，收尾写得进，会话不进故障，下一句照常。
func TestStartWriteFailureRejectsOnlyThatTurn(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	conv := consoleConv(h.c.CharacterID)
	h.exec(`CREATE TRIGGER made_start BEFORE UPDATE ON kernel_runs WHEN NEW.state='running' BEGIN SELECT RAISE(ABORT,'made-start-failure'); END`)
	rec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造开始失败", "start-1", kernel.InputStart))
	if err != nil {
		t.Fatal(err)
	}
	out := recv(t, inputRunOf(t, h.hub, rec).outcome, "开始就写库失败的本轮结果")
	if out.Err == nil || !strings.Contains(out.Err.Error(), "made-start-failure") {
		t.Fatalf("没有如实报开始写库失败：%v", out.Err)
	}
	if f := h.hub.Faults(); len(f) != 0 {
		t.Fatalf("开始写不进只拒这一轮，收尾写得进就不该进故障：%+v", f)
	}
	h.exec(`DROP TRIGGER made_start`)
	next, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "下一句", "start-2", kernel.InputStart))
	if err != nil {
		t.Fatalf("下一句该受理：%v", err)
	}
	if o := recv(t, inputRunOf(t, h.hub, next).outcome, "下一句这一轮"); o.Err != nil {
		t.Fatal(o.Err)
	}
}

// 库整体写不进（受理这一步就写不进）：只拒这一条、如实报库错，没有故障、没有半受理；库好了照常。
func TestAcceptWriteFailureRejectsOnlyThatInput(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	h.exec(`CREATE TRIGGER made_accept BEFORE INSERT ON kernel_inbox BEGIN SELECT RAISE(ABORT,'made-accept-failure'); END`)
	s := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "编造受理失败", "accept-1", kernel.InputStart)
	if _, err := h.hub.Submit(t.Context(), s); err == nil || !strings.Contains(err.Error(), "made-accept-failure") || errors.Is(err, ErrConversationFaulted) {
		t.Fatalf("受理写库失败该如实报库错、不是故障：%v", err)
	}
	if f := h.hub.Faults(); len(f) != 0 {
		t.Fatalf("受理失败不进故障：%+v", f)
	}
	h.exec(`DROP TRIGGER made_accept`)
	rec, err := h.hub.Submit(t.Context(), s)
	if err != nil {
		t.Fatalf("库好了同一句该受理：%v", err)
	}
	if o := recv(t, inputRunOf(t, h.hub, rec).outcome, "这一轮"); o.Err != nil {
		t.Fatal(o.Err)
	}
}

// 会话已经出故障时，收尾后才到的插话不重排：话在库里改成 held（和重启恢复同一个结果），界面看到它被丢了；重试后主人能放行。
func TestLateSteerOfFaultedConversationIsHeld(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `]`,
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
	rec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造第一句", "l1", kernel.InputStart))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, entered, "第一轮开始")
	steer := lifecycleSubmission(t, h, conv, "晚到的一句", "l2", kernel.InputSteer)
	steer.ExpectedRunID = rec.RunID
	srec, err := h.hub.Submit(t.Context(), steer)
	if err != nil {
		t.Fatal(err)
	}
	r := inputRunOf(t, h.hub, rec)
	lc := h.hub.lifecycle
	lc.mu.Lock()
	lc.raiseLocked(h.hub.Cfg.Now(), FaultScopeConversation, h.c.CharacterID, conv, FaultRunState, errors.New("made-fault"))
	events := h.hub.requeueSteersLocked(r)
	lc.mu.Unlock()
	if len(events) != 1 || events[0].Phase != "dropped" || events[0].Requeue || events[0].Text != "晚到的一句" {
		t.Fatalf("界面该看到这句没排上：%+v", events)
	}
	if row, err := h.st.KernelInputByID(t.Context(), srec.InputID); err != nil || row.State != kernel.InputAccepted {
		t.Fatalf("重试之前这句话还是 accepted：%v %v", row.State, err)
	}
	close(release)
	recv(t, r.outcome, "第一轮结果")
	if err := h.hub.RetryFault(t.Context(), h.c.CharacterID, conv); err != nil {
		t.Fatalf("重试该成功：%v", err)
	}
	if row, err := h.st.KernelInputByID(t.Context(), srec.InputID); err != nil || row.State != kernel.InputHeld {
		t.Fatalf("重试后这句话该记成 held：%v %v", row.State, err)
	}
}

// 写冲突：插话重排时这句话已经不是待处理（被取消了）：只拒这一条，界面看到它被丢了，不进故障。
func TestSteerRequeueConflictRejectsOnlyThatInput(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `]`,
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
	rec, err := h.hub.Submit(t.Context(), lifecycleSubmission(t, h, conv, "编造第一句", "c1", kernel.InputStart))
	if err != nil {
		t.Fatal(err)
	}
	recv(t, entered, "第一轮开始")
	steer := lifecycleSubmission(t, h, conv, "插进来的一句", "c2", kernel.InputSteer)
	steer.ExpectedRunID = rec.RunID
	srec, err := h.hub.Submit(t.Context(), steer)
	if err != nil {
		t.Fatal(err)
	}
	h.exec(`UPDATE kernel_inbox SET state='cancelled' WHERE input_id='` + srec.InputID + `'`)
	r := inputRunOf(t, h.hub, rec)
	h.hub.lifecycle.mu.Lock()
	events := h.hub.requeueSteersLocked(r)
	h.hub.lifecycle.mu.Unlock()
	if len(events) != 1 || events[0].Phase != "dropped" || events[0].Requeue || events[0].Text != "插进来的一句" {
		t.Fatalf("界面该看到这句被丢了：%+v", events)
	}
	if f := h.hub.Faults(); len(f) != 0 {
		t.Fatalf("写冲突只拒这一条，不进故障：%+v", f)
	}
	close(release)
	recv(t, r.outcome, "第一轮结果")
}
