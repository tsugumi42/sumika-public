package catalog

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// springDemo：弹簧反馈三样（规格-过渡动画 第十一节，照 Fluid 的 SpringSwitch、SpringButton、IslandToast）。
// 一屏放下：按钮按下回弹加挤压、悬停放大；开关滑钮滑动时按速度拉长；三颗按钮各弹出一条提示条。
// 顶栏的“动画速度”慢放、“重放”（R）三样都管；提示条是浮层，落在窗口顶上。
func springDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	nop := func() {}
	toast := Local(e, "toast", func() winshell.ToastState { return winshell.ToastState{} })
	autoStart := Local(e, "auto-start", func() bool { return true })
	notify := Local(e, "notify", func() bool { return false })
	sounds := Local(e, "sounds", func() bool { return true })

	e.Section(c, "buttons", "按钮：按下缩小，松手弹回；鼠标悬停略放大", func() {
		ui.Row(c.Key(e.Key("buttons-row"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Wrap().Children(func() {
			k.Button(c, e.Key("b-save"), "保存修改", true, false, nop)
			k.Button(c, e.Key("b-send"), "发送给晴枝", true, false, nop)
			k.Button(c, e.Key("b-later"), "稍后再说", false, false, nop)
			k.Button(c, e.Key("b-undo"), "撤回上一条", false, false, nop)
			k.IconButton(c, e.Key("b-copy"), "复制整段", chatui.Icon("copy", false), nop)
			k.Button(c, e.Key("b-off"), "发送给晴枝", true, true, nop)
		})
	})

	switchRow := func(name, label string, v *bool) {
		ui.Row(c.Key(e.Key(name + "-row"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Shrink(0).Children(func() {
			k.Switch(c, e.Key(name), label, v)
			ui.Text(c, label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine()
		})
	}
	e.Section(c, "switches", "开关：滑钮滑动时按速度拉长，停下变回正圆", func() {
		ui.Row(c.Key(e.Key("switches-row"))).Gap(g.SpaceSection).AlignItems(ui.Center).Wrap().Children(func() {
			switchRow("auto-start", "开机自启动", autoStart)
			switchRow("notify", "新消息提醒", notify)
			switchRow("sounds", "提示音", sounds)
		})
	})

	e.Section(c, "toasts", "提示条：从窗口顶上落下，约 2 秒后自己收起；连点会替换", func() {
		ui.Row(c.Key(e.Key("toasts-row"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Wrap().Children(func() {
			k.Button(c, e.Key("t-copy"), "复制这段话", false, false, func() { toast.Show("已复制", chatui.Icon("check", false)) })
			k.Button(c, e.Key("t-save"), "保存设置", true, false, func() { toast.Show("设置已保存", chatui.Icon("check", false)) })
			k.Button(c, e.Key("t-allow"), "允许运行", false, false, func() { toast.Show("已允许运行命令", chatui.Icon("terminal", false)) })
		})
		e.Line(c, "toast-out", map[bool]string{true: "提示条正在显示。", false: "点一颗按钮，看窗口顶上。"}[toast.Visible()])
	})
	k.Toast(c, e.Key("toast"), toast)
}
