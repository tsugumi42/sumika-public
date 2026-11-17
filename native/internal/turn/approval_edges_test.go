package turn

// A6：卡片已经落定后再批，执行、授权、文件和审批记录都不能再变；全部用事件控制交错。
import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

func approvalFileExec(t *testing.T, ran *atomic.Int32) func(*Config) {
	t.Helper()
	return func(c *Config) {
		c.ExecTool = func(_ context.Context, wd, _ string, args jsjson.Value) (tools.Result, error) {
			ran.Add(1)
			err := os.WriteFile(filepath.Join(wd, args.Obj["path"].Str), []byte(args.Obj["content"].Str), 0o644)
			return tools.Result{Text: "写好了"}, err
		}
	}
}

func checkApprovalFile(t *testing.T, h *harness, ran *atomic.Int32, wantRuns int32, wantText string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.wd, "a.txt"))
	if err != nil || string(b) != wantText || ran.Load() != wantRuns {
		t.Fatalf("文件应为 %q、执行 %d 次；实际文件 %q、执行 %d 次，错误 %v", wantText, wantRuns, b, ran.Load(), err)
	}
}

func TestA6ApprovalOutcomesAndLateDecisions(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"拒绝", "超时", "取消", "回答过", "回合停了"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			var ran atomic.Int32
			h := newHarnessOpts(t, harnessOpts{mode: "manual", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: approvalFileExec(t, &ran)})
			if err := os.WriteFile(filepath.Join(h.wd, "a.txt"), []byte("旧内容"), 0o644); err != nil {
				t.Fatal(err)
			}
			if action == "超时" {
				h.hub.Cfg.Gate.ApprovalMinutes = 0 // 登记后立即到期，不靠睡眠等边界
			}
			requests := make(chan approval.Request, 1)
			resolved := make(chan string, 2)
			if action != "超时" {
				h.approvals.OnRequest = append(h.approvals.OnRequest, func(r approval.Request) { requests <- r })
			}
			h.approvals.OnResolved = append(h.approvals.OnResolved, func(r approval.Request, d approval.Decision) {
				if action == "超时" {
					requests <- r // 立即到期的等待可被合并，迟到回答用最终卡片的 ID。
				}
				resolved <- d.Kind
			})
			out := h.hub.Send(h.c.CharacterID, "写个文件")
			req := recv(t, requests, "审批卡片："+h.diagnose(action))
			wantDecision, wantText, wantRuns := "denied", "旧内容", int32(0)
			switch action {
			case "拒绝", "回答过":
				choice := "deny"
				if action == "回答过" {
					choice, wantDecision, wantText, wantRuns = "once", "approved", "x", 1
				}
				if ok, err := h.approvals.Decide(req.ID, choice, "先别改"); !ok || err != nil {
					t.Fatalf("第一次回答没收下：%v %v", ok, err)
				}
			case "超时":
				wantDecision = "expired"
			case "取消":
				wantDecision = "cancelled"
				if !h.approvals.Cancel(req.ID) {
					t.Fatal("待审批的卡片应该能取消")
				}
			case "回合停了":
				wantDecision = "cancelled"
				if !h.stopWithin() {
					t.Fatal("等审批时应当能停止")
				}
			}
			if d := recv(t, resolved, "审批结局"); d != wantDecision {
				t.Fatalf("审批应为 %s，实际 %s", wantDecision, d)
			}
			o := recv(t, out, "审批后的回合结束："+h.diagnose(action))
			if action == "回合停了" {
				if o.Result == nil || !o.Result.Stopped {
					t.Fatalf("回合应当已经停止：%+v", o)
				}
			} else if o.Err != nil {
				t.Fatal(o.Err)
			}
			checkApprovalFile(t, h, &ran, wantRuns, wantText)
			before := h.stepState("c1")
			if !strings.Contains(before.approval, `"decision":"`+wantDecision+`"`) {
				t.Fatalf("审批记录应当记 %s：%+v", wantDecision, before)
			}
			for _, choice := range []string{"conversation", "always"} {
				if ok, err := h.approvals.Decide(req.ID, choice, ""); ok || err != nil {
					t.Fatalf("%s 后迟到的 %s 应返回假：%v %v", action, choice, ok, err)
				}
			}
			args, err := jsjson.Parse(writeCall)
			if err != nil {
				t.Fatal(err)
			}
			key := policy.KindOf("write", args, h.wd, nil).Key
			_, conv := h.approvals.Conv.Match("console:"+h.c.CharacterID, h.wd, key, "")
			if conv || h.approvals.Grants.Has(key) {
				t.Fatal("迟到的批准记下了授权")
			}
			if _, err := os.Stat(filepath.Join(h.data.Root, "config", "tool-grants.yml")); !os.IsNotExist(err) {
				t.Fatalf("迟到的批准不能创建授权文件：%v", err)
			}
			if after := h.stepState("c1"); after != before || len(resolved) != 0 || len(h.approvals.Pending()) != 0 {
				t.Fatalf("迟到的批准又改了结局：前 %+v，后 %+v，额外通知 %d", before, after, len(resolved))
			}
			checkApprovalFile(t, h, &ran, wantRuns, wantText)
		})
	}
}

func TestA6StopWhileCheckWaits(t *testing.T) {
	t.Parallel()
	var ran atomic.Int32
	h := newHarnessOpts(t, harnessOpts{mode: "manual", replies: `[` + callReply("write", writeCall) + `]`, mutate: approvalFileExec(t, &ran)})
	if err := os.WriteFile(filepath.Join(h.wd, "a.txt"), []byte("旧内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	// 改坏后也能收尾，不能把测试挂在 Hub.Idle 上。
	t.Cleanup(func() { close(release) })
	h.hub.Cfg.Gate.Ask = func(ctx context.Context, _ approval.Request, _ *approval.Pending) approval.Decision {
		close(entered)
		select {
		case <-ctx.Done():
		case <-release:
		}
		return approval.Decision{Kind: "cancelled"}
	}
	out := h.hub.Send(h.c.CharacterID, "写个文件")
	recv(t, entered, "Check 进入阻塞的审批门")
	if !h.stopWithin() {
		t.Fatal("Check 等着时也应该能停止")
	}
	o := recv(t, out, "Check 被停止后的回合结局")
	if o.Result == nil || !o.Result.Stopped {
		t.Fatalf("应当停止：%+v", o)
	}
	checkApprovalFile(t, h, &ran, 0, "旧内容")
	if s := h.stepState("c1"); s.status != "error" || !strings.Contains(s.preview, "Operation aborted") {
		t.Fatalf("停在 Check 上应当有唯一的中止结果：%+v", s)
	}
}

func TestStopWhileApprovalCallbackWaits(t *testing.T) {
	t.Parallel()
	for _, callback := range []string{"request", "resolved"} {
		t.Run(callback, func(t *testing.T) {
			var ran atomic.Int32
			h := newHarnessOpts(t, harnessOpts{mode: "manual", replies: `[` + callReply("write", writeCall) + `]`, mutate: approvalFileExec(t, &ran)})
			entered, release, exited, recorded := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)
			block := func() { close(entered); <-release; close(exited) }
			requests := make(chan struct{}, 1)
			h.approvals.OnRequest = append(h.approvals.OnRequest, func(approval.Request) { requests <- struct{}{} })
			if callback == "request" {
				h.approvals.OnRequest = append([]func(approval.Request){func(approval.Request) { block() }}, h.approvals.OnRequest...)
			} else {
				h.approvals.OnResolved = append([]func(approval.Request, approval.Decision){func(approval.Request, approval.Decision) { block() }}, h.approvals.OnResolved...)
			}
			h.approvals.OnResolved = append(h.approvals.OnResolved, func(approval.Request, approval.Decision) { close(recorded) })
			out := h.hub.Send(h.c.CharacterID, "写个文件")
			if callback == "request" {
				recv(t, entered, "请求通知已经卡住")
			} else {
				recv(t, requests, "审批卡片已经发出")
			}
			started := time.Now()
			if !h.stopWithin() {
				t.Fatal("审批通知卡住时也应当能停止")
			}
			o := recv(t, out, "通知卡住后的回合结束")
			if o.Result == nil || !o.Result.Stopped || ran.Load() != 0 {
				t.Fatalf("回合应已停止且不能执行工具：%+v，执行 %d 次", o, ran.Load())
			}
			// 通知一直卡到测试结束：等通知的改法上面的 recv 就过不去
			if elapsed := time.Since(started); elapsed > barrierLimit {
				t.Fatalf("停止等通知用了 %s", elapsed)
			}
			recv(t, recorded, "迟到的审批步骤记录")
			if s := h.stepState("c1"); s.status != "cancelled" || !strings.Contains(s.approval, `"decision":"cancelled"`) {
				t.Fatalf("迟到通知不能盖掉取消结局：%+v", s)
			}
			if _, err := os.Stat(filepath.Join(h.wd, "a.txt")); !os.IsNotExist(err) {
				t.Fatalf("停止后的工具不能写文件：%v", err)
			}
			t.Cleanup(func() { recv(t, exited, "卡住的通知退出") })
		})
	}
}

func TestApprovalStepsLateNotifications(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[]`})
	a := &approval.Approvals{}
	WireApprovalSteps(a, h.st)
	for _, scenario := range []string{"结局后等待", "结束后等待", "结束后批准"} {
		t.Run(scenario, func(t *testing.T) {
			req := approval.Request{ID: scenario, ToolCallID: scenario, CharacterID: h.c.CharacterID, Title: "写文件"}
			if scenario == "结局后等待" {
				if err := a.Persist(req, &approval.Decision{Kind: "cancelled"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := h.st.StepApproval(req.ToolCallID, req.CharacterID, "running", `{"title":"写文件"}`); err != nil {
					t.Fatal(err)
				}
				if err := h.st.StepEnd(req.ToolCallID, "写好了", false, 1, nil); err != nil {
					t.Fatal(err)
				}
			}
			before := h.stepState(req.ToolCallID)
			if scenario == "结束后批准" {
				if err := a.Persist(req, &approval.Decision{Kind: "approved"}); err != nil {
					t.Fatal(err)
				}
				if after := h.stepState(req.ToolCallID); after.status != "done" || !strings.Contains(after.approval, `"decision":"approved"`) {
					t.Fatalf("迟到的批准应补记结局并保留工具完成状态：%+v", after)
				}
			} else {
				if err := a.Persist(req, nil); err != nil {
					t.Fatal(err)
				}
				if after := h.stepState(req.ToolCallID); after != before {
					t.Fatalf("迟到的等待盖掉了终态：之前 %+v，之后 %+v", before, after)
				}
			}
		})
	}
}

func TestApprovalStepsDatabaseWaitIsBounded(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"request", "resolved", "approved", "approved-cancel"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarnessOpts(t, harnessOpts{replies: `[]`})
			a := approval.NewApprovals(&approval.Gate{Now: time.Now})
			WireApprovalSteps(a, h.st)
			h.st.Trace.SetMaxOpenConns(1)
			requested, writing, recorded := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			a.Subscribe(func(approval.Request) { requested <- struct{}{} }, func(approval.Request, approval.Decision) { close(recorded) })
			persist := a.Persist
			written := make(chan error, 1)
			a.Persist = func(req approval.Request, d *approval.Decision) error {
				target := (phase == "request" && d == nil) || (phase != "request" && d != nil)
				if target {
					close(writing)
				}
				err := persist(req, d)
				if target {
					written <- err
				}
				return err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := approval.Request{ID: phase, ToolCallID: phase, CharacterID: h.c.CharacterID, Title: "写文件", ExpiresAt: 600_000}
			asked := make(chan approval.Decision, 1)
			// 等待阶段在发卡前占连接；结局阶段在发卡后占连接。
			if phase != "request" {
				go func() { asked <- a.Ask(ctx, req, nil) }()
				recv(t, requested, "等待状态已写成")
			}
			conn, err := h.st.Trace.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				conn.Close()
				recv(t, recorded, "持久化流程最终退出")
			}()
			if phase == "request" {
				go func() { asked <- a.Ask(ctx, req, nil) }()
				recv(t, writing, "等待状态正在等连接")
			}
			started := time.Now()
			if strings.HasPrefix(phase, "approved") {
				if ok, err := a.Decide(req.ID, "once", ""); !ok || err != nil {
					t.Fatalf("认领批准失败：%v %v", ok, err)
				}
			} else {
				cancel()
			}
			if phase != "request" {
				recv(t, writing, "结局正在等连接")
			}
			if phase == "approved-cancel" {
				cancel()
			}
			if phase == "approved" {
				select {
				case d := <-asked:
					t.Fatalf("批准尚未写成，不能放行工具：%+v", d)
				case <-time.After(300 * time.Millisecond):
				}
			} else if d := recv(t, asked, "连接占用时取消仍返回"); d.Kind != "cancelled" {
				t.Fatalf("回合应先取消，实际 %+v", d)
			}
			// 连接要到下面才放开：等数据库的改法上面的 recv 就过不去
			if elapsed := time.Since(started); phase != "approved" && elapsed > barrierLimit {
				t.Fatalf("取消等待数据库用了 %s", elapsed)
			}
			conn.Close()
			if err := recv(t, written, "放开连接后原写入最终成功"); err != nil {
				t.Fatalf("取消等待不能取消步骤写入：%v", err)
			}
			wantStatus, wantDecision := "cancelled", "cancelled"
			if strings.HasPrefix(phase, "approved") {
				wantStatus, wantDecision = "running", "approved"
			}
			if phase == "approved" {
				if d := recv(t, asked, "批准写成才返回"); d.Kind != "approved" {
					t.Fatalf("写成后应该批准，实际 %+v", d)
				}
			}
			recv(t, recorded, "最终结局已经写成")
			if s := h.stepState(phase); s.status != wantStatus || !strings.Contains(s.approval, `"decision":"`+wantDecision+`"`) {
				t.Fatalf("放开连接后必须有最终审批记录：%+v", s)
			}
		})
	}
}

func TestApprovalDurableCrashClassification(t *testing.T) {
	t.Parallel()
	for _, decision := range []string{"waiting", "approved"} {
		t.Run(decision, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[]`})
			a := approval.NewApprovals(&approval.Gate{Now: time.Now})
			WireApprovalSteps(a, h.st)
			if err := h.st.TurnStart("crash", h.c.CharacterID, "console:"+h.c.CharacterID, "console", 1); err != nil {
				t.Fatal(err)
			}
			if err := h.st.StepStart(store.Step{ID: "crash-tool", CharacterID: h.c.CharacterID, TurnID: "crash", Kind: "tool", Name: "write", Status: "running", StartedAt: 1}); err != nil {
				t.Fatal(err)
			}
			requests := make(chan approval.Request, 1)
			a.OnRequest = []func(approval.Request){func(r approval.Request) { requests <- r }}
			asked := make(chan approval.Decision, 1)
			go func() {
				asked <- a.Ask(context.Background(), approval.Request{ID: "crash", ToolCallID: "crash-tool", CharacterID: h.c.CharacterID, Title: "写文件", ExpiresAt: 600_000}, nil)
			}()
			recv(t, requests, "持久化后发出的卡片")
			if decision == "approved" {
				if ok, err := a.Decide("crash", "once", ""); !ok || err != nil {
					t.Fatalf("批准失败：%v %v", ok, err)
				}
				recv(t, asked, "批准写成才放行")
			}
			swept, err := h.st.SweepInterrupted(100)
			if err != nil {
				t.Fatal(err)
			}
			s := h.stepState("crash-tool")
			if decision == "waiting" {
				if swept.Waiting != 1 || swept.Unknown != 0 || s.status != "cancelled" || !strings.Contains(s.preview, "没有执行") {
					t.Fatalf("等待中被杀只能取消等待：%+v %+v", swept, s)
				}
				a.Cancel("crash")
				recv(t, asked, "清理模拟进程的等待")
			} else if swept.Unknown != 1 || swept.Waiting != 0 || s.status != "error" || !strings.Contains(s.preview, "不知道结果") {
				t.Fatalf("批准后执行前被杀必须保守归入结果未知：%+v %+v", swept, s)
			}
		})
	}
}
