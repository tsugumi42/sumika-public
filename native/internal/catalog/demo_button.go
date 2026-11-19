package catalog

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// buttonDemo：Kit.Button 的主、次、禁用，Kit.IconButton 的平时和禁用。
func buttonDemo(c *ui.Context, e Env) {
	k := e.Kit
	nop := func() {}
	copyIcon, starIcon := chatui.Icon("copy", false), chatui.Icon("star", false)
	e.Section(c, "states", "状态排", func() {
		e.Cells(c, "row", func() {
			e.Cell(c, "secondary", "次要", func() { k.Button(c, e.Key("b-secondary"), "稍后再说", false, false, nop) })
			e.Cell(c, "primary", "主按钮", func() { k.Button(c, e.Key("b-primary"), "保存修改", true, false, nop) })
			e.Cell(c, "secondary-off", "次要·禁用", func() { k.Button(c, e.Key("b-secondary-off"), "撤回上一条", false, true, nop) })
			e.Cell(c, "primary-off", "主按钮·禁用", func() { k.Button(c, e.Key("b-primary-off"), "发送给晴枝", true, true, nop) })
			e.Cell(c, "icon", "图标钮", func() { k.IconButton(c, e.Key("i-copy"), "复制整段", copyIcon, nop) })
			e.Cell(c, "icon-off", "图标钮·禁用", func() {
				k.IconButtonWith(c, e.Key("i-copy-off"), "复制整段", copyIcon, winshell.IconButtonOptions{Disabled: true}, nop)
			})
		})
	})

	sent, starred := Local(e, "sent", func() int { return 0 }), Local(e, "starred", func() bool { return false })
	e.Section(c, "play", "试一试", func() {
		e.Play(c, "send", func() {
			ui.Row(c.Key(e.Key("send-row"))).Gap(k.G.SpaceSmall).AlignItems(ui.Center).Children(func() {
				k.Button(c, e.Key("send"), "发送给晴枝", true, false, func() { *sent++ })
				k.Button(c, e.Key("undo"), "撤回上一条", false, *sent == 0, func() { *sent-- })
				k.IconButton(c, e.Key("star"), "收藏这段对话", starIcon, func() { *starred = !*starred })
			})
			e.Line(c, "send-out", fmt.Sprintf("已经发出 %d 条。%s", *sent, map[bool]string{true: "对话已收藏。", false: "对话还没收藏。"}[*starred]))
		})
	})
}
