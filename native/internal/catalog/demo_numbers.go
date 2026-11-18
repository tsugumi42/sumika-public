package catalog

import (
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 审批倒计时从多少秒开始，到 0 重新开始（样例里让它一直有得看；真的审批到 0 就是超时）。
const countdownFrom = 272 * time.Second

// numbersDemo：数字和图标（规格-过渡动画 第五节铁律 7 的新规则，和图标变形）。
// 一屏放下：步进器（点加减，变了的那几位滚）、会倒数的审批倒计时（每秒一次，从上面落下来）、四对点一下就换的图标钮（复制 ↔ 勾、发送 ↔ 停止、菜单 ↔ 关闭、播放 ↔ 暂停，轮廓流过去）。
// 顶栏的“动画速度”慢放、“重放”（R）都管：慢放下点一下、不等界面空闲，能看到数字滚到一半、图标变到一半。
func numbersDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G

	e.Section(c, "steppers", "步进器：点加减，只有变了的那几位滚；变大从下面升上来，变小从上面落下来", func() {
		e.Cells(c, "steppers-row", func() {
			topk := Local(e, "topk", func() float64 { return 8 })
			wait := Local(e, "wait", func() float64 { return 30 })
			chars := Local(e, "chars", func() float64 { return 1200 })
			e.Cell(c, "step-topk", "每次回话最多想起几条", func() {
				k.Stepper(c, e.Key("topk"), "每次回话最多想起几条", *topk, winshell.StepperSpec{Min: 1, Max: 50, Unit: "条"}, func(v float64) { *topk = v })
			})
			e.Cell(c, "step-wait", "聊天安静多久整理一次", func() {
				k.Stepper(c, e.Key("wait"), "聊天安静多久整理一次", *wait, winshell.StepperSpec{Min: 1, Max: 720, Unit: "分钟"}, func(v float64) { *wait = v })
			})
			e.Cell(c, "step-chars", "想起的记忆最多多少字", func() {
				k.Stepper(c, e.Key("chars"), "想起的记忆最多多少字", *chars, winshell.StepperSpec{Min: 100, Max: 8000, Step: 100, Unit: "字"}, func(v float64) { *chars = v })
			})
		})
	})

	e.Section(c, "countdown", "审批倒计时：每秒变一次，只有变了的位落下来；到 0 重新开始", func() {
		deadline := Local(e, "deadline", func() time.Time { return c.Now().Add(countdownFrom) })
		rem := deadline.Sub(c.Now())
		if rem <= 0 {
			*deadline = c.Now().Add(countdownFrom)
			rem = countdownFrom
		}
		// 在下一次整秒的边界醒来：显示的数字是向上取整的秒数，边界一过就是新的一位
		wake := rem % time.Second
		if wake == 0 {
			wake = time.Second
		}
		c.After(wake + time.Millisecond)
		st := commandStep("count-cmd", "rm -rf build", &chatui.Approval{
			State: chatui.ApprovalPending, Reason: "清掉旧的构建，下一步重新编译。", Remaining: rem,
			Scopes: []chatui.ApprovalScope{chatui.AllowConversation, chatui.AllowAlways}, KindLabel: "这类命令",
		})
		e.ChatCard(c, "count-card", min(e.Room, g.ControlWide*2), func(col float32) {
			h := e.ChatHost(col, false)
			messages(c, h, []*chatui.Msg{process("count-msg", 0, st)})
		})
	})

	// 点一下就换成另一个的图标钮：成对的两个图标，轮廓从一个流到另一个。复制和别处一样，勾亮两秒后自己变回来。
	type pair struct {
		id, a, b, la, lb, name string
		auto                   bool
	}
	pairs := []pair{
		{"copy", "copy", "check", "复制这段话", "已复制", "复制 ↔ 勾", true},
		{"send", "send", "stop", "发送", "停止回复", "发送 ↔ 停止", false},
		{"menu", "menu", "close", "打开菜单", "关闭菜单", "菜单 ↔ 关闭", false},
		{"play", "play", "pause", "播放", "暂停", "播放 ↔ 暂停", false},
	}
	e.Section(c, "icons", "图标变形：点一下，图标的轮廓流到另一个；变到一半再点，从当时的形状接着流", func() {
		e.Cells(c, "icons-row", func() {
			for _, p := range pairs {
				at := Local(e, "at-"+p.id, func() time.Time { return time.Time{} })
				on := Local(e, "on-"+p.id, func() bool { return false })
				if p.auto && !at.IsZero() {
					if left := at.Add(2 * time.Second).Sub(c.Now()); left > 0 {
						*on = true
						c.After(left + time.Millisecond)
					} else {
						*on, *at = false, time.Time{}
					}
				}
				icon, label := p.a, p.la
				if *on {
					icon, label = p.b, p.lb
				}
				e.Cell(c, "pair-"+p.id, p.name, func() {
					k.IconMorphButton(c, e.Key("pair-"+p.id), label, icon, winshell.IconButtonOptions{}, func() {
						if p.auto {
							*at = c.Now()
							*on = true
							return
						}
						*on = !*on
					})
				})
			}
		})
	})

	// 真的输入框：右下角圆键是同一颗钮，发送 ↔ 停止 图标流过去；她在回复时打几个字，键变回发送（插话）
	draft := Local(e, "draft", func() *chatui.Composer { return chatui.NewComposer() })
	busy := Local(e, "busy", func() bool { return false })
	e.Section(c, "composer", "输入框的圆键：发送、停止、插话是同一颗钮；点“她在回复”切过去，图标从纸飞机流成方块；回复中打字又变回发送", func() {
		ui.Row(c.Key(e.Key("composer-ctl"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
			label := "她在回复"
			if *busy {
				label = "回复完了"
			}
			k.Button(c, e.Key("toggle-busy"), label, false, false, func() { *busy = !*busy })
		})
		th := &k.Theme
		host := chatui.ComposerHost{T: k.ChatTokens(), Font: k.Font, Busy: *busy,
			C:       chatui.ComposerColors{Fill: th.Surface, Card: th.Background, Line: th.Border, Text: th.Text, Muted: th.TextMuted, Accent: th.Accent, OnAccent: th.AccentText, Danger: th.Danger},
			OnSend:  func() { (*draft).Main.Text = "" },
			OnStop:  func() { *busy = false },
			OnSteer: func() { (*draft).Main.Text = "" }}
		ui.Column(c.Key(e.Key("composer-box"))).Width(g.ControlWide * 2).Children(func() { host.Draw(c, *draft) })
	})
}
