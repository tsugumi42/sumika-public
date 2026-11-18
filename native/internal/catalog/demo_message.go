package catalog

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 消息和审批（规格-过渡动画 第二批：C5～C8、铁律 3、4）。一屏三样：
//   - 审批卡：会倒计时，点“允许”“拒绝”“让它超时”，这一张卡自己收成一行结果（高度用利落弹簧、按钮和凹块先淡出、
//     结果的小图标从倒计时的时钟流成勾或叉、字淡入）；“重新来”再要一张；
//   - 过程气泡：点摘要行展开收起，再点里面的思考行、命令卡。这里模拟“这一行的顶部可见、列表没有贴底”，所以高度会动；
//     真实聊天里贴底跟随时高度直接切换、内容淡入（宿主给 FollowingEnd 之前也是这样）；
//   - 一条消息：按钮往里追加表格或代码块，整块出现时只淡入一次，周围不位移。
//
// 顶栏的“动画速度”慢放、“重放”（R）都管。

const askDemoWindow = 272 * time.Second // 审批的等待时间，到 0 就是超时

// askDemo 是审批卡那一栏的进度。
type askDemo struct {
	Step     *chatui.Step
	Deadline time.Time
	Rounds   int // 重新来了几次：步骤 ID 跟着变，新一张卡是新的一步
}

func newAskStep(round int, now time.Time) *chatui.Step {
	return &chatui.Step{ID: fmt.Sprintf("ask-%d", round), Kind: chatui.StepCommand, State: chatui.StepRunning, Target: "rm -rf build",
		Approval: &chatui.Approval{State: chatui.ApprovalPending, Reason: "清掉旧的构建，下一步重新编译。", Remaining: askDemoWindow, Shown: now,
			Scopes: []chatui.ApprovalScope{chatui.AllowConversation, chatui.AllowAlways}, KindLabel: "这类命令"}}
}

// blocksDemo 是第三栏那条消息的进度：已经追加了几块。
type blocksDemo struct{ Added, Clears int }

const blocksIntro = "我把三份笔记的字数数了一遍，结果和那段脚本都在下面。"

var blockAdds = []string{
	"\n\n| 文件 | 字数 | 状态 |\n|---|--:|---|\n| 纸灯.md | 1280 | 已补完 |\n| 窗边.md | 460 | 进行中 |\n| 待办.md | 3920 | 排过一遍 |",
	"\n\n数的时候用的是这段：\n\n```go\nfor _, f := range files {\n\tn := len([]rune(read(f)))\n\tfmt.Println(f, n)\n}\n```",
	"\n\n| 日期 | 新增 | 删掉 |\n|---|--:|--:|\n| 11-13 | 420 | 35 |\n| 11-14 | 880 | 120 |\n| 11-15 | 310 | 18 |",
	"\n\n如果想再省一步，可以把它存成脚本：\n\n```sh\ngo run ./cmd/count notes/*.md\n```",
}

// processDemoMsg 是第二栏的过程消息：想、读、运行、改，做完了，收着。
func processDemoMsg(id string) *chatui.Msg {
	return process(id, 0,
		&chatui.Step{ID: id + "-think", Kind: chatui.StepThink, State: chatui.StepDone, Seconds: 4,
			Thought: "先看昨天的笔记停在哪一段。\n窗边那段只写了一半，灯罩换纸的事还没记。\n补完以后跑一遍测试，确认没把别的格式弄坏。\n最后把改动和结果一起告诉主人。"},
		&chatui.Step{ID: id + "-read", Kind: chatui.StepRead, State: chatui.StepDone, Target: "notes/纸灯.md"},
		&chatui.Step{ID: id + "-cmd", Kind: chatui.StepCommand, State: chatui.StepDone, Target: "go test ./internal/chatui", Seconds: 3,
			Output: "ok  \tsumika-native/internal/chatui\t11.7s\n=== RUN   TestAskCardLayers\n--- PASS: TestAskCardLayers (0.02s)\n=== RUN   TestAskCardKeyboard\n--- PASS: TestAskCardKeyboard (0.01s)\nPASS"},
		&chatui.Step{ID: id + "-edit", Kind: chatui.StepEdit, State: chatui.StepDone, Target: "notes/纸灯.md", Added: 2, Removed: 1, Diff: approvalDiff},
	)
}

func messageDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	w := e.chatCardWidth(3)
	rid := fmt.Sprintf("-%d", e.Replay) // 重放要新的消息：元素和 markdown 里的“已播过”都是按消息 ID 记的

	ask := Local(e, "ask", func() askDemo { return askDemo{Step: newAskStep(0, c.Now()), Deadline: c.Now().Add(askDemoWindow)} })
	a := *ask
	blocks := Local(e, "blocks", func() blocksDemo { return blocksDemo{} })
	b := *blocks

	// 过程气泡高度要动的条件：这里不是滚动的列表，所以“没贴底、行顶可见”恒成立
	host := func(col float32) chatui.Host {
		h := e.ChatHost(col, false)
		h.FollowingEnd = func() bool { return false }
		h.ViewRect = func() ui.Rect { return ui.Rect{X: -1e5, Y: -1e5, W: 2e5, H: 2e5} }
		return h
	}

	// 三栏各自定宽并排（Section 是铺满一行的，放不进来）
	column := func(name, title string, body func()) {
		ui.Column(c.Key(e.Key(name))).Width(w).Shrink(0).Gap(g.SpaceSmall).AlignItems(ui.Start).Children(func() {
			k.SectionHeading(c, title)
			body()
		})
	}
	ui.Row(c.Key(e.Key("columns"))).FillWidth().Gap(g.SpaceMedium).AlignItems(ui.Start).Wrap().Children(func() {
		// ---- 审批卡 ----
		column("ask", "审批决定后：这一张卡自己收成一行", func() {
			st := a.Step
			pending := st.Approval.State == chatui.ApprovalPending || st.Approval.State == chatui.ApprovalSubmitting
			if pending { // 倒计时：每到整秒醒一次，到 0 就是超时
				left := a.Deadline.Sub(c.Now())
				if left <= 0 {
					st.Approval.State, st.State = chatui.ApprovalExpired, chatui.StepStopped
					pending = false
				} else {
					st.Approval.Remaining = left
					wake := left % time.Second
					if wake == 0 {
						wake = time.Second
					}
					c.After(wake + time.Millisecond)
				}
			}
			decide := func(state chatui.ApprovalState, step chatui.StepState) func() {
				return func() {
					if st.Approval.State == chatui.ApprovalPending {
						st.Approval.State, st.State = state, step
					}
				}
			}
			e.ChatCard(c, "ask-card", w, func(col float32) {
				h := host(col)
				h.OnApprove = func(string, chatui.ApprovalScope) { decide(chatui.ApprovalAllowed, chatui.StepDone)() }
				h.OnDeny = func(string, string) { decide(chatui.ApprovalDenied, chatui.StepDenied)() }
				messages(c, h, []*chatui.Msg{process("ask-msg"+rid, 0, st)})
			})
			ui.Row(c.Key(e.Key("ask-btns"))).Gap(g.SpaceSmall).Wrap().Children(func() {
				k.Button(c, e.Key("ask-allow"), "允许", true, !pending, decide(chatui.ApprovalAllowed, chatui.StepDone))
				k.Button(c, e.Key("ask-deny"), "拒绝", false, !pending, decide(chatui.ApprovalDenied, chatui.StepDenied))
				k.Button(c, e.Key("ask-expire"), "让它超时", false, !pending, decide(chatui.ApprovalExpired, chatui.StepStopped))
				k.Button(c, e.Key("ask-again"), "重新来", false, pending, func() {
					ask.Rounds++
					ask.Step, ask.Deadline = newAskStep(ask.Rounds, c.Now()), c.Now().Add(askDemoWindow)
				})
			})
			out := "还在等你决定。"
			switch st.Approval.State {
			case chatui.ApprovalAllowed:
				out = "允许了：卡收成一行，时钟变成勾。"
			case chatui.ApprovalDenied:
				out = "拒绝了：卡收成一行，时钟变成叉。"
			case chatui.ApprovalExpired:
				out = "超时了：卡收成一行，时钟还是时钟。"
			}
			e.Line(c, "ask-out", out)
		})

		// ---- 过程气泡 ----
		column("process", "过程气泡：点一行展开，再点里面的行", func() {
			e.ChatCard(c, "process-card", w, func(col float32) {
				messages(c, host(col), []*chatui.Msg{processDemoMsg("proc" + rid)})
			})
			e.Line(c, "process-hint", "展开收起时高度走利落弹簧、内容淡入淡出，下面的行只被推着走。")
		})

		// ---- 整块出现 ----
		column("blocks", "整块出现：表格、代码块只淡入", func() {
			text := blocksIntro
			for i := 0; i < b.Added; i++ {
				text += blockAdds[i%len(blockAdds)]
			}
			e.ChatCard(c, "blocks-card", w, func(col float32) {
				messages(c, host(col), []*chatui.Msg{her(fmt.Sprintf("blocks%s-%d", rid, b.Clears), 0, text)})
			})
			ui.Row(c.Key(e.Key("blocks-btns"))).Gap(g.SpaceSmall).Wrap().Children(func() {
				label := "追加表格"
				if b.Added%2 == 1 {
					label = "追加代码块"
				}
				k.Button(c, e.Key("blocks-add"), label, true, b.Added >= len(blockAdds), func() { blocks.Added++ })
				k.Button(c, e.Key("blocks-clear"), "清空", false, b.Added == 0, func() { blocks.Added, blocks.Clears = 0, blocks.Clears+1 })
			})
			e.Line(c, "blocks-hint", "淡入一次，周围的字和框一动不动。")
		})
	})
}
