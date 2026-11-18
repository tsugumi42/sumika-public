package catalog

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// overviewDemo：族谱总览（主人 2026-11-16“接着做动画，还有继续补齐组件并且保持风格统一”，第 1 步）。
// 每一族的静止样子按立体感三档（凹下去 / 平的 / 浮起来）和“状态、头像、气泡”并排放在一屏里，
// 用来对着《设计自查》第六节一眼看出谁不统一：同一档的应该长得像一家人。顶栏切亮色、夜间、圆角、疏密一起看。
// 这一页只摆静止样子，不放交互说明；要点的去各自那一条。
func overviewDemo(c *ui.Context, e Env) {
	k := e.Kit
	g := k.G
	text := Local(e, "text", func() string { return "晴枝" })
	search := Local(e, "search", func() string { return "" })
	seg := Local(e, "seg", func() int { return 0 })
	chip := Local(e, "chip", func() int { return 1 })
	on := Local(e, "on", func() bool { return true })
	off := Local(e, "off", func() bool { return false })
	checked := Local(e, "checked", func() bool { return true })
	ddOpen := Local(e, "dd-open", func() bool { return false })
	nop := func() {}

	ui.Row(c.Key(e.Key("two-cols"))).FillWidth().Gap(g.SpaceSection).AlignItems(ui.Start).Children(func() {
		ui.Column(c.Key(e.Key("left"))).Grow(1).MinWidth(0).Gap(g.SpaceSection).Children(func() {
			e.Section(c, "sunken", "凹下去：等人往里填东西的", func() {
				e.Cells(c, "sunken-cells", func() {
					e.Cell(c, "field", "输入框", func() { k.TextField(c, e.Key("field"), text, "名字").Width(g.ControlWide) })
					e.Cell(c, "search", "搜索框", func() { k.SearchField(c, e.Key("search"), search, "搜索会话", nil).Width(g.ControlWide) })
					e.Cell(c, "stepper", "步进器", func() {
						k.Stepper(c, e.Key("stepper"), "条数", 8, winshell.StepperSpec{Min: 1, Max: 20, Unit: "条"}, func(float64) {})
					})
					e.Cell(c, "dropdown", "下拉入口", func() {
						k.SettingSelect(c, e.Key("dropdown"), "模型", ddOpen, 0, []string{"跟全局", "DeepSeek V4.1 Flash"}, func(int) {})
					})
				})
			})

			e.Section(c, "flat", "平的：贴在面上的", func() {
				e.Cells(c, "flat-cells", func() {
					e.Cell(c, "secondary", "次要按钮", func() { k.Button(c, e.Key("secondary"), "稍后再说", false, false, nop) })
					e.Cell(c, "icon", "图标钮", func() { k.IconButton(c, e.Key("icon"), "复制整段", chatui.Icon("copy", false), nop) })
					e.Cell(c, "disabled", "禁用", func() { k.Button(c, e.Key("disabled"), "发送给晴枝", false, true, nop) })
					e.Cell(c, "segment", "分段", func() { k.Segment(c, e.Key("segment"), seg, []string{"白名单", "黑名单"}) })
					e.Cell(c, "chips", "筛选片", func() { k.Chips(c, e.Key("chips"), []string{"全部", "私聊", "群聊"}, chip) })
					e.Cell(c, "value-row", "可点的值行", func() {
						ui.Column(c.Key(e.Key("value-row-box"))).Width(g.ControlWide).Children(func() {
							k.ValueRow(c, "回复用的模型", "跟全局", true, false, nop)
						})
					})
				})
			})

			e.Section(c, "raised", "浮起来：选中块、卡片、圆钮", func() {
				e.Cells(c, "raised-cells", func() {
					e.Cell(c, "primary", "主按钮", func() { k.Button(c, e.Key("primary"), "保存修改", true, false, nop) })
					e.Cell(c, "switch-on", "开关（开）", func() { k.Switch(c, e.Key("switch-on"), "开", on) })
					e.Cell(c, "switch-off", "开关（关）", func() { k.Switch(c, e.Key("switch-off"), "关", off) })
					e.Cell(c, "check", "勾选", func() { k.Check(c, e.Key("check"), "记住这个选择", checked, false) })
					e.Cell(c, "group", "分组面", func() {
						ui.Column(c.Key(e.Key("group-box"))).Width(g.ControlWide * 1.6).Children(func() {
							k.SettingGroup(c, e.Key("group"), "记忆", func() {
								k.SettingItem(c, e.Key("group-item"), "每次回话最多想起几条", "", winshell.SettingItemOptions{}, func() { k.SettingValue(c, "8 条") })
							})
						})
					})
				})
			})
			e.Section(c, "avatars", "头像", func() {
				e.Cells(c, "avatar-cells", func() {
					for i, name := range []string{"晴枝", "雾栞", "铃花"} {
						e.Cell(c, "avatar-"+name, name, func() { k.Avatar(c, e.Key("avatar-"+name), name, i, 40, nil) })
					}
					e.Cell(c, "square", "群（方头像）", func() { k.SquareAvatar(c, e.Key("square"), "巴巴博一", 3, 40, nil) })
				})
			})
		})
		ui.Column(c.Key(e.Key("right"))).Grow(1).MinWidth(0).Gap(g.SpaceSection).Children(func() {
			e.Section(c, "states", "状态：用图形，少写字", func() {
				e.Cells(c, "states-cells", func() {
					e.Cell(c, "badges", "徽标", func() {
						ui.Row(c.Key(e.Key("badges-row"))).Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
							k.Badge(c, e.Key("b-n"), "只能取数", winshell.BadgeNeutral)
							k.Badge(c, e.Key("b-g"), "能展开", winshell.BadgeGood)
							k.Badge(c, e.Key("b-w"), "要登录", winshell.BadgeWarn)
						})
					})
					e.Cell(c, "dot", "改过的小圆点", func() { k.ModifiedDot(c, e.Key("dot"), "原来是 6 条") })
					e.Cell(c, "note", "说明一行", func() {
						ui.Column(c.Key(e.Key("note-box"))).Width(g.ControlWide * 1.6).Gap(g.SpaceSmall).Children(func() {
							k.Note(c, "关掉的网站不会自动展开。", false)
							k.Note(c, "保存失败：配置文件被占用。", true)
						})
					})
					e.Cell(c, "notebar", "输入框位置的提示条", func() {
						ui.Column(c.Key(e.Key("notebar-box"))).Width(g.ControlWide * 1.6).Children(func() { k.NoteBar(c, e.Key("notebar"), "这里只能看，不能发消息") })
					})
					e.Cell(c, "empty", "空状态", func() {
						ui.Column(c.Key(e.Key("empty-box"))).Width(g.ControlWide * 1.6).Children(func() {
							k.EmptyState(c, e.Key("empty"), "还没有提醒", "点上面的新建，或者在聊天里让她记一个", "")
						})
					})
					e.Cell(c, "error", "出错状态", func() {
						ui.Column(c.Key(e.Key("error-box"))).Width(g.ControlWide * 1.6).Children(func() {
							k.ErrorState(c, e.Key("error"), "读不到设置", "配置文件被别的程序占着", "重试")
						})
					})
				})
			})

			e.Section(c, "bubbles", "气泡：两边是一种做法", func() {
				ui.Column(c.Key(e.Key("preview-box"))).FillWidth().Children(func() { k.Preview(c) })
			})
		})
	})
}
