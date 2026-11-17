package turn

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/store"
)

// 放行 held 的话：时限从放行这一刻算，不从几小时前的受理算。
func TestReleaseHeldOldInputStillRuns(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	sub, old := heldAfterRestart(t, h, "隔了一小时才放行的话", "held-old")
	hourAgo := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := h.st.Trace.Exec(`UPDATE kernel_inbox SET accepted_at=? WHERE input_id=?`, hourAgo, old.InputID); err != nil {
		t.Fatal(err)
	}
	before := h.bodyCount()
	rec, err := h.hub.ReleaseHeld(context.Background(), sub.Input.Identity.Scope, old.InputID)
	if err != nil {
		t.Fatal(err)
	}
	snap := lifecycleWait(t, h.hub, sub.Input.Identity.Scope, rec.RunID)
	if snap.Outcome != kernel.OutcomeDone {
		t.Fatalf("受理在一小时前的 held 放行后应当正常处理，不是额度到顶：%+v", snap)
	}
	if got := h.bodyCount(); got != before+1 {
		t.Fatalf("应当恰好请求一次模型：%d→%d", before, got)
	}
}

// 授权号、授权版本变了（旧行没有，或授权已撤销）的 held 行照样能放行、能放弃；别人的身份仍不行。
func TestHeldIgnoresAuthorityDrift(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	sub, a := heldAfterRestart(t, h, "旧授权的一句", "held-drift-a")
	cur := sub.Input.Identity.Scope
	cur.AuthorityID, cur.AuthorityRevision, cur.AccountID = "auth-now", 7, "acct-now"
	rec, err := h.hub.ReleaseHeld(context.Background(), cur, a.InputID)
	if err != nil {
		t.Fatalf("授权信息不同不该放不了：%v", err)
	}
	if snap := lifecycleWait(t, h.hub, cur, rec.RunID); snap.Outcome != kernel.OutcomeDone {
		t.Fatalf("%+v", snap)
	}
	row, err := h.st.KernelInputByID(context.Background(), a.InputID)
	if err != nil || row.Identity.Scope.AuthorityID != "auth-now" || row.Identity.Scope.AuthorityRevision != 7 {
		t.Fatalf("放行后应当带当前绑定的身份，不沿用旧授权：%+v %v", row.Identity.Scope, err)
	}
	_, b := heldAfterRestart(t, h, "要放弃的旧授权一句", "held-drift-b")
	if err := h.hub.DiscardHeld(context.Background(), cur, b.InputID); err != nil {
		t.Fatalf("授权信息不同不该丢不掉：%v", err)
	}
	other := cur
	other.ActorID = "someone-else"
	_, c := heldAfterRestart(t, h, "别人碰不得的一句", "held-drift-c")
	if err := h.hub.DiscardHeld(context.Background(), other, c.InputID); !errors.Is(err, store.ErrInputConflict) {
		t.Fatalf("别的身份仍然不能放弃：%v", err)
	}
}

// 排队等待的时间不算进这句话自己的时限。
func TestQueuedInputDeadlineStartsWhenItRuns(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var first atomic.Bool
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, onBody: func(ctx context.Context, body string) {
		if strings.Contains(body, "占着的前一句") && first.CompareAndSwap(false, true) {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}})
	conv := consoleConv(h.c.CharacterID)
	a := lifecycleSubmission(t, h, conv, "占着的前一句", "qd-a", kernel.InputStart)
	if _, err := h.hub.Submit(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	<-entered
	q := lifecycleSubmission(t, h, conv, "排在后面的一句", "qd-b", kernel.InputQueue)
	rq, err := h.hub.Submit(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	row, err := h.st.KernelInputByID(context.Background(), rq.InputID)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟前一轮占了很久：这句话受理时开的根，时限已经过去了
	past := time.Now().Add(-time.Minute)
	err = h.st.UpdateBudget(context.Background(), row.Identity.RootID, "", func(data []byte) ([]byte, error) {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		m["Snapshot"].(map[string]any)["Limits"].(map[string]any)["Deadline"] = past
		return json.Marshal(m)
	})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	snap := lifecycleWait(t, h.hub, a.Input.Identity.Scope, rq.RunID)
	if snap.Outcome != kernel.OutcomeDone {
		t.Fatalf("排队等久了的那句不该一开始就判额度到顶：%+v", snap)
	}
	if n := h.bodyCount(); n != 2 {
		t.Fatalf("两句都该请求模型：%d 次", n)
	}
}

// 模型列表里没有她用的模型：结局认成 model_unknown 并带上模型名。
func TestRunReasonUnknownModelNamesIt(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`, mutate: func(c *Config) {
		for id := range c.Models {
			c.Models[id] = "编造的不存在模型"
		}
	}})
	sub := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "你好", "reason-um", kernel.InputStart)
	rec, err := h.hub.Submit(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	snap := lifecycleWait(t, h.hub, sub.Input.Identity.Scope, rec.RunID)
	if snap.Outcome != kernel.OutcomeError || snap.Reason.Class != llm.ClassModelUnknown || snap.Reason.Model != "编造的不存在模型" {
		t.Fatalf("模型不在列表里应当是 model_unknown 并带模型名：%+v", snap)
	}
}

// 读卡、读提示词这类本地文件错误不能被说成网络断了（Windows 上 PathError 解开是 syscall.Errno，满足 net.Error）。
func TestReasonOfLocalFileErrorIsNotNetwork(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	_, openErr := os.Open(filepath.Join(t.TempDir(), "不存在", "card.md"))
	var pe *fs.PathError
	if !errors.As(openErr, &pe) {
		t.Fatalf("测试前提：应当是 PathError：%v", openErr)
	}
	for _, err := range []error{openErr, errors.New("读卡：" + openErr.Error()), errors.Join(errors.New("读卡"), openErr)} {
		got := h.hub.reasonOf(kernel.OutcomeError, err, nil)
		if got.Class == llm.ClassNetwork {
			t.Fatalf("本地文件错误被认成了网络：%+v", got)
		}
	}
	if got := h.hub.reasonOf(kernel.OutcomeError, openErr, nil).Class; got != ReasonLocal {
		t.Fatalf("本地文件错误应当归到 %s：%q", ReasonLocal, got)
	}
}

// 收尾时才到的插话被改排成新一轮：终态快照要带上，界面才跟得上。
func TestRequeuedSteerReportedOnSnapshot(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`})
	sub := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "先说一句", "rq-a", kernel.InputStart)
	rec, err := h.hub.Submit(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, h.hub, sub.Input.Identity.Scope, rec.RunID)
	in := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "收尾时才到的插话", "rq-b", kernel.InputQueue)
	late := in.Input
	late.Identity.RunID, late.Identity.RootID = "old-run", "old-run"
	payload, err := json.Marshal(late)
	if err != nil {
		t.Fatal(err)
	}
	row, _, err := h.st.AcceptKernelInput(context.Background(), store.KernelInput{Identity: late.Identity, Domain: convKey(late.CharacterID, late.Conversation), ClientKey: "rq-b", Digest: "rq-b-digest", Payload: string(payload), Mode: kernel.InputSteer, AcceptedAt: time.Now().UnixMilli()}, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	r := &inputRun{input: late}
	h.hub.lifecycle.mu.Lock()
	h.hub.lifecycle.steers[late.Identity.RunID] = []store.KernelInput{row}
	h.hub.requeueSteersLocked(r)
	h.hub.lifecycle.mu.Unlock()
	rq := r.snapshot.Requeued
	if rq == nil || len(rq.Inputs) != 1 || rq.Inputs[0].InputID != row.Identity.InputID || rq.Inputs[0].RunID == "" || rq.Inputs[0].RunID == "old-run" || rq.Inputs[0].Text != "收尾时才到的插话" {
		t.Fatalf("快照该带上改排出的新一轮：%+v", rq)
	}
	lifecycleWait(t, h.hub, in.Input.Identity.Scope, rq.Inputs[0].RunID)
}
