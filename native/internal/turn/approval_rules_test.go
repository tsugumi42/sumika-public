package turn

// ADR-330：放行留痕、给模型的状态、放行之后执行之前规则被撤销。全用假网关和假工具，临时目录。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/approval"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
	"sumika-native/internal/tools"
)

// web_search 默认放行：不弹卡、工具照跑，步骤记录写明是“默认放行”。
func TestWebSearchDefaultAllowedAndTraced(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"manual", "auto", "yolo"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{mode: mode, replies: `[` + callReply("web_search", `{"queries":["编的查询"]}`) + `,` + textReply + `]`})
			var ran atomic.Int32
			h.use(toolPlugin{"web", []Tool{{Decl: tools.Declaration{Name: "web_search", Description: "查", Parameters: `{"type":"object","properties":{"queries":{"type":"array"}}}`},
				Exec: func(context.Context, ToolCall) (tools.Result, error) {
					ran.Add(1)
					return tools.Result{Text: "查到了"}, nil
				}}}})
			WireGateSteps(h.hub.Cfg.Gate, h.st, nil) // 真后台装配时就是这样接的
			var asked atomic.Int32
			h.approvals.OnRequest = append(h.approvals.OnRequest, func(approval.Request) { asked.Add(1) })
			h.send("查一下")
			if ran.Load() != 1 || asked.Load() != 0 || len(h.approvals.Pending()) != 0 {
				t.Fatalf("默认放行不该问：执行 %d 次，问 %d 次", ran.Load(), asked.Load())
			}
			st := h.stepState("c1")
			if !strings.Contains(st.approval, `"by":"default"`) || StepRule(st.approval) != "默认放行，不用问" {
				t.Fatalf("步骤记录要写明默认放行：%q", st.approval)
			}
		})
	}
}

// 主人批准了：步骤记录写“你批准了这一次”，模型下一轮看到状态说明，且说明不进步骤输出。
func TestApprovedOnceNoteForModelAndStep(t *testing.T) {
	t.Parallel()
	var ran atomic.Int32
	h := newHarnessOpts(t, harnessOpts{mode: "manual", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: approvalFileExec(t, &ran)})
	h.approvals.OnRequest = append(h.approvals.OnRequest, func(r approval.Request) { go h.approvals.Decide(r.ID, "once", "") })
	h.send("写个文件")
	if ran.Load() != 1 {
		t.Fatalf("批准后应当执行一次：%d", ran.Load())
	}
	st := h.stepState("c1")
	if !strings.Contains(st.approval, `"by":"once"`) || StepRule(st.approval) != "你批准了这一次" {
		t.Fatalf("步骤记录：%q", st.approval)
	}
	if !strings.Contains(h.body(1), "所有者批准了这一步") {
		t.Fatal("模型下一轮要看到批准的状态说明")
	}
	if strings.Contains(st.preview, "所有者批准了这一步") {
		t.Fatal("状态说明只给模型看，不进步骤")
	}
}

// 被拒绝：模型看到的话要说明不要换个方式重试。
func TestDeniedWordsReachModel(t *testing.T) {
	t.Parallel()
	var ran atomic.Int32
	h := newHarnessOpts(t, harnessOpts{mode: "manual", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: approvalFileExec(t, &ran)})
	h.approvals.OnRequest = append(h.approvals.OnRequest, func(r approval.Request) { go h.approvals.Decide(r.ID, "deny", "先别改") })
	h.send("写个文件")
	body := h.body(1)
	if ran.Load() != 0 || !strings.Contains(body, "所有者拒绝了这个操作") || !strings.Contains(body, "不要换个方式重试") {
		t.Fatalf("拒绝的话没到模型：执行 %d 次", ran.Load())
	}
}

// 放行之后、真正执行之前规则被撤销：这一步不执行，模型看到原因。
// 批准和执行之间隔着等全局槽：占满槽让这一步停在那里，等它过了门，在设置页撤销，再放槽。
func TestRevokedBetweenApprovalAndExecution(t *testing.T) {
	t.Parallel()
	var ran atomic.Int32
	h := newHarnessOpts(t, harnessOpts{mode: "manual", replies: `[` + callReply("write", writeCall) + `,` + textReply + `]`, mutate: approvalFileExec(t, &ran)})
	slots := NewToolScheduler(1, 1)
	slots.global <- struct{}{} // 槽被别的回合占着
	h.hub.Cfg.Scheduler = slots
	args, _ := jsjson.Parse(writeCall)
	key := policy.KindOf("write", args, h.wd, nil).Key
	if err := h.approvals.Grants.Put(approval.Rule{Key: key, Label: "x", Tool: "write", At: 1}); err != nil {
		t.Fatal(err)
	}
	cleared := make(chan struct{})
	h.hub.Cfg.Gate.OnAuto = func(approval.Request, approval.Clearance) { close(cleared) }
	out := h.hub.Send(h.c.CharacterID, "写个文件")
	recv(t, cleared, "这一步过了门（还在等槽）")
	if ran.Load() != 0 {
		t.Fatal("槽被占着，不该已经执行")
	}
	if ok, err := h.approvals.Grants.Remove(key); !ok || err != nil { // 主人在设置页撤销
		t.Fatalf("撤销：%v %v", ok, err)
	}
	<-slots.global // 槽空出来
	wait(t, out)
	if ran.Load() != 0 {
		t.Fatalf("撤销之后这一步不该执行：执行了 %d 次", ran.Load())
	}
	if _, err := os.Stat(filepath.Join(h.wd, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("文件不该写出来：%v", err)
	}
	if !strings.Contains(h.body(1), "刚被所有者撤销") {
		t.Fatal("模型要看到规则被撤销的原因")
	}
}
