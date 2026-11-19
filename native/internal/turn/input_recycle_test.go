package turn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

func TestCompletedInputRunsBounded(t *testing.T) {
	t.Parallel()
	const limit, rounds = 128, 257
	h := newHarnessOpts(t, harnessOpts{replies: `[` + strings.TrimSuffix(strings.Repeat(textReply+",", rounds), ",") + `]`})
	var first Submission
	var firstReceipt kernel.Receipt
	var watch *RunSubscription
	var firstSnapshot RunSnapshot
	for i := range rounds {
		text := fmt.Sprintf("编造回收输入 %d", i)
		in, err := h.hub.trustedInput(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{h.storeGroup(text)}, Text: text, Audience: audience.Audience{Channel: "qq", Scene: "group", Conversation: groupConv}, Scope: "public", Addressed: true})
		if err != nil {
			t.Fatal(err)
		}
		s := Submission{Input: in, Mode: kernel.InputQueue, ClientKey: h.hub.inputClientKey(in)}
		rec, err := h.hub.Submit(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		r := inputRunOf(t, h.hub, rec)
		if i == 0 {
			first, firstReceipt = s, rec
			watch, err = h.hub.WatchRun(in.Identity.Scope, rec.RunID)
			if err != nil {
				t.Fatal(err)
			}
			defer watch.Close()
		}
		out := recv(t, r.outcome, "回收轮次完成")
		if out.Err != nil {
			t.Fatal(out.Err)
		}
		if i == 0 {
			firstSnapshot = watch.Snapshot()
		}
		duplicate, err := h.hub.Submit(t.Context(), s)
		if err != nil || duplicate.RunID != rec.RunID || duplicate.InputID != rec.InputID || duplicate.Sequence != rec.Sequence {
			t.Fatalf("完成后幂等回执改变：%+v %v", duplicate, err)
		}
		if got := h.hub.submitOutcome(t.Context(), in, kernel.InputQueue); got != r.outcome {
			t.Fatal("完成后重复输入没有返回原 outcome 通道")
		}
		stopped, err := h.hub.StopRun(t.Context(), kernel.StopRequest{Scope: in.Identity.Scope, RunID: rec.RunID, ThroughSequence: rec.Sequence})
		if err != nil || !stopped.Drained || stopped.RunID != rec.RunID || stopped.Revision != r.snapshot.Revision {
			t.Fatalf("完成后停止回执改变：%+v %v", stopped, err)
		}
		h.hub.lifecycle.mu.Lock()
		count := len(h.hub.lifecycle.runs)
		h.hub.lifecycle.mu.Unlock()
		if count > limit {
			t.Fatalf("完成 %d 轮后 lc.runs=%d，超过保留上限 %d", i+1, count, limit)
		}
	}
	h.hub.lifecycle.mu.Lock()
	retained := len(h.hub.lifecycle.runs)
	h.hub.lifecycle.mu.Unlock()
	if retained > limit {
		t.Fatalf("完成 %d 轮后 lc.runs=%d，超过保留上限 %d", rounds, retained, limit)
	}
	if got := watch.Snapshot(); got != firstSnapshot {
		t.Fatalf("淘汰破坏已持有的终态订阅：%+v want %+v", got, firstSnapshot)
	}
	duplicate, err := h.hub.Submit(t.Context(), first)
	if err != nil || duplicate.RunID != firstReceipt.RunID || duplicate.InputID != firstReceipt.InputID {
		t.Fatalf("淘汰后重复受理丢失持久幂等：%+v %v", duplicate, err)
	}
	if _, err := h.hub.WatchRun(first.Input.Identity.Scope, firstReceipt.RunID); !errors.Is(err, store.ErrInputConflict) {
		t.Fatalf("过期运行仍可新建内存订阅：%v", err)
	}
	if _, err := h.hub.StopRun(t.Context(), kernel.StopRequest{Scope: first.Input.Identity.Scope, RunID: firstReceipt.RunID, ThroughSequence: firstReceipt.Sequence}); !errors.Is(err, store.ErrInputConflict) {
		t.Fatalf("过期运行没有按内存未命中拒绝停止：%v", err)
	}
	oldOutcome := recv(t, h.hub.submitOutcome(t.Context(), first.Input, kernel.InputQueue), "过期结果通道")
	if oldOutcome.TurnID != firstReceipt.RunID || oldOutcome.Err == nil || !strings.Contains(oldOutcome.Err.Error(), "输入已受理") {
		t.Fatalf("过期重复输入没有返回原运行定位：%+v", oldOutcome)
	}
	h.mu.Lock()
	requests := len(h.bodies)
	h.mu.Unlock()
	if requests != rounds {
		t.Fatalf("幂等重复输入重新执行：requests=%d want %d", requests, rounds)
	}
	t.Logf("完成=%d 保留=%d 假HTTP请求=%d；窗口内幂等/StopRun、淘汰后持久幂等/原订阅通过", rounds, retained, requests)
}

func TestKernelRunStateFailureReachesOutcome(t *testing.T) {
	t.Parallel()
	for _, states := range [][]string{{"draining"}, {"settled"}, {"draining", "settled"}} {
		t.Run(strings.Join(states, "+"), func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
			settled := h.watchSettled()
			for _, state := range states {
				_, err := h.st.Trace.Exec(`CREATE TRIGGER fail_` + state + ` BEFORE UPDATE ON kernel_runs WHEN NEW.state='` + state + `' BEGIN SELECT RAISE(ABORT,'made-` + state + `-failure'); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			out := recv(t, h.hub.Send(h.c.CharacterID, "编造收尾失败"), "写库失败的本轮结果")
			event := settledFor(t, settled, out.TurnID)
			for _, state := range states {
				marker := "made-" + state + "-failure"
				if out.Err == nil || !strings.Contains(out.Err.Error(), marker) {
					t.Errorf("Outcome.Err=%v，缺少 %s", out.Err, marker)
				}
				if event.Err == nil || !strings.Contains(event.Err.Error(), marker) {
					t.Errorf("Settled.Err=%v，缺少 %s", event.Err, marker)
				}
			}
			// 收尾失败仍封住后续受理；生成结局保持原来的 done。
			next := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "不能受理", "after-failure", kernel.InputStart)
			_, err := h.hub.Submit(t.Context(), next)
			if err == nil || !strings.Contains(err.Error(), "made-") || event.Outcome != OutcomeDone {
				t.Fatalf("后续准入或生成结局改变：err=%v outcome=%s", err, event.Outcome)
			}
		})
	}
}

func TestCompletedInternalRunsBounded(t *testing.T) {
	t.Parallel()
	const limit, rounds = 128, 129
	entered, release := make(chan struct{}, 1), make(chan struct{}, 1)
	defer close(release)
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + strings.TrimSuffix(strings.Repeat(textReply+",", rounds+2), ",") + `]`,
		mutate: func(cfg *Config) {
			cfg.InternalAuthorize = func(context.Context, kernel.Invocation, string) error { return nil }
		},
		onBody: func(ctx context.Context, body string) {
			if strings.Contains(body, "编造阻塞子任务") {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		},
	})
	parent := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "编造父运行", "parent", kernel.InputStart)
	rec, err := h.hub.Submit(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	parentRun := inputRunOf(t, h.hub, rec)
	if out := recv(t, parentRun.outcome, "父运行完成"); out.Err != nil {
		t.Fatal(out.Err)
	}
	var first InternalRun
	var holding RunHandle
	for i := range rounds {
		inv := parentRun.input.Identity
		inv.Scope.Conversation = fmt.Sprintf("internal:recycle-%d", i)
		inv.Scope.AuthorityID, inv.Scope.AuthorityRevision = "made-authority", 1
		inv.Kind, inv.RunID, inv.ParentRunID = kernel.KindChild, fmt.Sprintf("made-child-%d", i), rec.RunID
		inv.RootID = inv.RunID
		limits := h.hub.Cfg.BudgetConfig.Limits(time.Now())
		if err := AdmitBudgetRoot(t.Context(), h.hub.Cfg.Budget, inv, limits); err != nil {
			t.Fatal(err)
		}
		in := InternalRun{Identity: inv, Goal: "编造子任务", ModelID: h.c.Characters[h.c.CharacterID].Model, Limits: limits}
		if i == 0 {
			first = in
			held := in
			held.Identity.RunID, held.Identity.RootID = "made-held-child", "made-held-child"
			held.Identity.Scope.Conversation = "internal:held-child"
			held.Goal = "编造阻塞子任务"
			if err := AdmitBudgetRoot(t.Context(), h.hub.Cfg.Budget, held.Identity, limits); err != nil {
				t.Fatal(err)
			}
			holding, err = h.hub.StartInternal(t.Context(), held)
			if err != nil {
				t.Fatal(err)
			}
			recv(t, entered, "阻塞子任务开始")
		}
		handle, err := h.hub.StartInternal(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		if out := recv(t, handle.Done, "子运行完成"); out.Err != nil {
			t.Fatal(out.Err)
		}
		if _, err := h.hub.StartInternal(t.Context(), in); !errors.Is(err, store.ErrInputConflict) {
			t.Fatalf("完成后重复子运行未拒绝：%v", err)
		}
	}
	h.hub.lifecycle.mu.Lock()
	retained := len(h.hub.lifecycle.runs)
	h.hub.lifecycle.mu.Unlock()
	if retained > limit+1 {
		t.Fatalf("完成 %d 个子运行后 lc.runs=%d，超过完成上限 %d 加一个活动运行", rounds, retained, limit)
	}
	if releaseMaintenance, err := h.hub.AcquireLocalMaintenance(t.Context(), parent.Input.Identity.Scope); !errors.Is(err, ErrInputBusy) {
		if releaseMaintenance != nil {
			releaseMaintenance()
		}
		t.Fatalf("父运行淘汰后，活动子任务丢失维护屏障：%v", err)
	}
	release <- struct{}{}
	if out := recv(t, holding.Done, "活动子任务完成"); out.Err != nil {
		t.Fatal(out.Err)
	}
	h.hub.lifecycle.mu.Lock()
	retained = len(h.hub.lifecycle.runs)
	h.hub.lifecycle.mu.Unlock()
	if retained > limit {
		t.Fatalf("所有子任务完成后 lc.runs=%d，超过保留上限 %d", retained, limit)
	}
	if _, err := h.hub.StartInternal(t.Context(), first); !errors.Is(err, store.ErrInputConflict) {
		t.Fatalf("淘汰后重复子运行未拒绝：%v", err)
	}
	t.Logf("子运行完成=%d 保留=%d；重复 RunID 拒绝，父运行淘汰后活动子任务仍阻止维护", rounds+1, retained)
}
