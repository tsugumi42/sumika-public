package main

import (
	"fmt"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 输入框里折叠的粘贴内容（chatui.AttText）：点药丸弹出对话框看全文；斜杠命令补全的名单转换也放这里。

// pasteView 是查看框的状态：只读看，不改；要改就删掉药丸重新贴。
type pasteView struct {
	open bool
	text string
}

func (a *app) openPasteView(att chatui.Att) { a.pastePane = pasteView{open: true, text: att.Text} }

// wbPasteDialog 画查看框：外框用 Kit.Dialog，正文是一块可选的文字，放不下就滚动。
func (a *app) wbPasteDialog(c *ui.Context, kit *winshell.Kit) {
	v := &a.pastePane
	if !v.open {
		v.text = "" // 关掉后不再占着几千字
		return
	}
	title := fmt.Sprintf("粘贴的内容 · %d 字", utf8.RuneCountInString(v.text))
	kit.Dialog(c, &v.open, title, "关闭粘贴的内容", func() {
		g, p := kit.G, kit.P
		ui.Scroll(c.Key("paste-view")).FillWidth().MaxHeight(g.HitSmall * 12).Children(func() {
			ui.Text(c.Key("paste-text"), v.text).FontSize(g.BodySize).FixedLineHeight(g.BodyLine).TextColor(p.Ink).Wrap().FillWidth().Selectable()
		})
	})
}

// slashItems 把后台的命令名单转成输入框补全用的条目。
func slashItems(cmds []api.SlashCommand) []chatui.SlashItem {
	out := make([]chatui.SlashItem, len(cmds))
	for i, c := range cmds {
		out[i] = chatui.SlashItem{Name: c.Name, Summary: c.Summary}
	}
	return out
}
