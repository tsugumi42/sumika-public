package catalog

import (
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

const approvalDiff = "@@ -3,2 +3,3 @@\n 窗边的纸灯亮起来了。\n-昨天说到一半。\n+昨天说到窗边那段。\n+今天接着说。\n"

// 点“允许一次”后多久后台才确认：模拟后台往返，让“提交中”的转圈能被看见。
const approvalSettle = 900 * time.Millisecond // 行为计时：模拟后台确认

// process 造一条她的过程消息（一轮里的若干步）。
func process(id string, sec int, steps ...*chatui.Step) *chatui.Msg {
	m := her(id, sec, "")
	m.Steps = steps
	return m
}

func commandStep(id, target string, a *chatui.Approval) *chatui.Step {
	return &chatui.Step{ID: id, Kind: chatui.StepCommand, State: chatui.StepRunning, Target: target, Approval: a}
}

// approvalLive 是试一试里那张审批卡的进度。
type approvalLive struct {
	Step      *chatui.Step
	SubmitAt  time.Time
	Decisions int
}

func newApprovalStep(now time.Time) *chatui.Step {
	return commandStep("live-cmd", "rm -rf build", &chatui.Approval{
		State: chatui.ApprovalPending, Reason: "清掉旧的构建，下一步重新编译。", Remaining: 272 * time.Second, Shown: now,
		Scopes: []chatui.ApprovalScope{chatui.AllowConversation, chatui.AllowAlways}, KindLabel: "这类命令",
	})
}

// approvalDemo：等批准的审批卡（单步一张卡、多步各占一行）、提交中、决定后的结果；试一试里能点“拒绝”“允许一次”和“⋯”里的选项，看提交中、结果一行，再点“重来”。
func approvalDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	w := e.chatCardWidth(4)
	// 展开着看：结果行在过程展开后的头部行尾；每条消息第一次出现时展开一次，之后用户可以自己收
	expand := func(id string) {
		Local(e, "opened-"+id, func() *bool { *e.Open("step-" + id) = true; return new(bool) })
	}
	card := func(name, label string, group, open bool, width float32, m *chatui.Msg) {
		if open {
			expand(m.ID)
		}
		e.Cell(c, name, label, func() {
			e.ChatCard(c, name+"-card", width, func(col float32) {
				h := e.ChatHost(col, false)
				h.Group = group
				messages(c, h, []*chatui.Msg{m})
			})
		})
	}
	result := func(id string, kind chatui.StepKind, state chatui.StepState, target string, ap chatui.ApprovalState) *chatui.Msg {
		return process("st-"+id, 0, &chatui.Step{ID: id, Kind: kind, State: state, Target: target, Seconds: 1, Approval: &chatui.Approval{State: ap}})
	}
	e.Section(c, "states", "等批准：只有一步时压成一张卡", func() {
		e.Cells(c, "row", func() {
			card("cmd", "命令", false, false, w, process("st-cmd", 0, commandStep("c1", "rm -rf build", &chatui.Approval{State: chatui.ApprovalPending, Reason: "清掉旧的构建，下一步重新编译。", Remaining: 272 * time.Second})))
			card("edit", "改文件", false, false, w, process("st-edit", 0, &chatui.Step{ID: "e1", Kind: chatui.StepEdit, State: chatui.StepRunning, Target: "notes/纸灯.md", Diff: approvalDiff, Added: 2, Removed: 1,
				Approval: &chatui.Approval{State: chatui.ApprovalPending, Reason: "把昨天的笔记补完整。", Remaining: 59 * time.Second}}))
			card("group", "群里：谁的话引起的", true, false, w, process("st-group", 0, commandStep("c2", "git push origin main", &chatui.Approval{State: chatui.ApprovalPending, Cause: "雾栞", Reason: "把今天的改动推上去。", Remaining: 120 * time.Second})))
			card("busy", "提交中", false, false, w, process("st-busy", 0, commandStep("c3", "rm -rf build", &chatui.Approval{State: chatui.ApprovalSubmitting, Reason: "清掉旧的构建。", Remaining: 50 * time.Second})))
		})
	})
	e.Section(c, "results", "决定之后：头部行尾一个小图标加两三个字", func() {
		e.Cells(c, "row", func() {
			card("allowed", "已允许", false, true, w, result("r1", chatui.StepCommand, chatui.StepDone, "rm -rf build", chatui.ApprovalAllowed))
			card("denied", "已拒绝", false, true, w, result("r2", chatui.StepCommand, chatui.StepDenied, "rm -rf /", chatui.ApprovalDenied))
			card("expired", "超时", false, true, w, result("r3", chatui.StepCommand, chatui.StepStopped, "make deploy", chatui.ApprovalExpired))
			card("revoked", "已撤回", false, true, w, result("r4", chatui.StepEdit, chatui.StepStopped, "notes/纸灯.md", chatui.ApprovalRevoked))
		})
	})
	// 多步的样子这次没变，为了一屏放得下没摆在这里（主人要“3 秒内点完”）

	live := Local(e, "live", func() *approvalLive { return &approvalLive{Step: newApprovalStep(c.Now())} })
	a := *live
	e.Section(c, "play", "试一试", func() {
		st := a.Step
		if st.Approval.State == chatui.ApprovalSubmitting { // 到点后台“确认”
			if left := a.SubmitAt.Add(approvalSettle).Sub(c.Now()); left > 0 {
				c.After(left)
			} else {
				st.Approval.State, st.State = chatui.ApprovalAllowed, chatui.StepDone
			}
		}
		out := "还在等你决定。"
		switch st.Approval.State {
		case chatui.ApprovalSubmitting:
			out = "已发出，等后台确认。"
		case chatui.ApprovalAllowed:
			out = "允许了，命令执行完了。"
		case chatui.ApprovalDenied:
			out = "拒绝了，命令没有执行。"
		}
		ui.Row(c.Key(e.Key("play-row"))).FillWidth().Gap(g.SpaceMedium).AlignItems(ui.Start).Children(func() {
			e.ChatCard(c, "play-card", min(e.Room, g.ControlWide*2), func(col float32) {
				h := e.ChatHost(col, false)
				h.OnApprove = func(string, chatui.ApprovalScope) {
					if st.Approval.State == chatui.ApprovalPending {
						st.Approval.State, a.SubmitAt = chatui.ApprovalSubmitting, c.Now()
						a.Decisions++
					}
				}
				h.OnDeny = func(string, string) {
					if st.Approval.State == chatui.ApprovalPending {
						st.Approval.State, st.State = chatui.ApprovalDenied, chatui.StepDenied
						a.Decisions++
					}
				}
				messages(c, h, []*chatui.Msg{process("live-msg", 0, st)})
			})
			ui.Column(c.Key(e.Key("play-side"))).Gap(g.SpaceSmall).AlignItems(ui.Start).Shrink(0).Children(func() {
				k.Button(c, e.Key("again"), "重来", false, st.Approval.State == chatui.ApprovalPending, func() { a.Step = newApprovalStep(c.Now()) })
				e.Line(c, "play-out", out)
				e.Line(c, "play-hint", "焦点在卡片里时，回车是允许一次，Esc 是拒绝。")
			})
		})
	})
}
