package catalog

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// menusDemo：菜单这一族的展开收起（主人 2026-11-15“先做菜单这批”，照 Fluid 的 MenuMorph）。
// 按钮菜单、带搜索的选择器、右键菜单：都从贴着按钮（或右键那一点）的那条边长开，关掉时缩回去；窗口下面放不下就朝上长。
// 审批卡的“⋯”是 mygo 自己画项的菜单，也挂了同一种展开（在“审批卡”那一条里点）；消息的右键菜单 mygo 没给口子，还是直接出现。
func menusDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	last := Local(e, "last", func() string { return "" })
	pillOpen := Local(e, "pill-open", func() bool { return false })
	pickOpen := Local(e, "pick-open", func() bool { return false })
	query := Local(e, "query", func() string { return "" })
	model := Local(e, "model", func() string { return "跟全局" })
	act := func(s string) func() { return func() { *last = s } }

	e.Section(c, "button", "按钮菜单：从按钮下沿长开，关掉时缩回按钮", func() {
		// 放进一行：直接放在区块里会被拉满宽
		ui.Row(c.Key(e.Key("pill-row"))).AlignItems(ui.Center).Children(func() {
			k.PillMenu(c, e.Key("pill"), "晴枝", "换个角色回复", chatui.Icon("user", false), pillOpen, []winshell.MenuItem{
				{Label: "晴枝", Checked: true, Action: act("换成晴枝")},
				{Label: "雾栞", Action: act("换成雾栞")},
				{Label: "铃花", Action: act("换成铃花")},
				{Label: "不再让她回复", Danger: true, Action: act("不再让她回复")},
			})
		})
	})

	e.Section(c, "picker", "选择器：顶上是搜索框，打字筛选时只有高度跟着变，不重新长一遍", func() {
		pick := func(name string) func() { return func() { *model = name; *last = "选了 " + name } }
		k.Picker(c, e.Key("picker"), "回复用的模型", *model, pickOpen, query, "搜索模型", []winshell.MenuItem{
			{Label: "跟全局", Action: pick("跟全局")},
			{Label: "Agnes 2.5 Flash", Group: "agnes", Action: pick("Agnes 2.5 Flash")},
			{Label: "Agnes 2.5 Pro", Group: "agnes", Action: pick("Agnes 2.5 Pro")},
			{Label: "DeepSeek V4.1 Flash", Group: "deepseek", Action: pick("DeepSeek V4.1 Flash")},
		})
	})

	e.Section(c, "context", "右键菜单：在下面这块上点右键（或 Shift+F10），从点的那里长开", func() {
		card := ui.Row(c.Key(e.Key("ctx-card"))).Label("右键这里").FillWidth().Height(g.HitRegular * 2).Padding(g.SpaceMedium).AlignItems(ui.Center).
			Radius(g.RadiusControl).Background(k.T.C.Sunken).Focusable()
		card.Children(func() {
			ui.Text(c, "在这块上点右键").FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(k.P.Muted)
		})
		k.ContextMenu(c, card, e.Key("ctx"), []winshell.MenuItem{
			{Label: "复制", Shortcut: "Ctrl+C", Action: act("复制")},
			{Label: "回复", Action: act("回复")},
			{Label: "标为已读", Action: act("标为已读")},
			{Label: "删除…", Danger: true, Disabled: true, Trailing: "还没有这个功能"},
		})
	})

	// 对话框：从打开它的那颗按钮长出来，关掉缩回去（左右两颗，看起点跟着按钮走）
	dlgOpen := Local(e, "dlg-open", func() bool { return false })
	e.Section(c, "dialog", "对话框：从点的那颗按钮长出来、移到正中，关掉时缩回那颗按钮", func() {
		ui.Row(c.Key(e.Key("dlg-row"))).FillWidth().AlignItems(ui.Center).Children(func() {
			k.Button(c, e.Key("dlg-a"), "整理一次记忆…", false, false, func() { *dlgOpen = true })
			ui.Box(c).Grow(1)
			k.Button(c, e.Key("dlg-b"), "清空这个会话…", false, false, func() { *dlgOpen = true })
		})
	})
	k.ActionDialog(c, e.Key("dlg"), dlgOpen, winshell.DialogSpec{Title: "确定吗？", Actions: []winshell.DialogAction{
		{ID: "ok", Label: "好的", Default: true}, {ID: "cancel", Label: "再想想"},
	}, OnAction: func(id string) {
		*last = map[string]string{"ok": "对话框选了好的", "cancel": "对话框选了再想想"}[id]
	}}, func() {
		ui.Text(c, "这一步做完就回不去了。按 Esc、点外面或右上角的关闭也都会缩回按钮。").FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).Wrap()
	})

	out := "选一项，这里写选了什么。"
	if *last != "" {
		out = fmt.Sprintf("刚才：%s。", *last)
	}
	e.Line(c, "menus-out", out)
}
