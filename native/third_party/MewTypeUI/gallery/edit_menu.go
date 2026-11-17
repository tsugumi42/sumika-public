package gallery

import (
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/overlay"
)

func init() {
	Register(Page{ID: "overlay/edit-menu", Area: "overlay", Title: "正文编辑命令", View: editMenuPage})
}

func editMenuPage(c *ui.Context) {
	type model struct {
		body  ui.Handle
		event string
	}
	s := ui.Local(c.Root(), "edit-menu-demo", func() model { return model{} })
	overlayPage(c, "正文编辑命令", func() {
		ui.Text(c, "选中正文后右键，或 Shift+F10；复制经过 CopyData 将 🙂 还原成 [微笑]。")
		ui.Column(c).Selectable().Bind(&s.body).CopyData(func(parts []ui.SelectedText) transfer.Data {
			var lines []string
			for _, p := range parts {
				lines = append(lines, string([]rune(p.Text)[p.Start:p.End]))
			}
			return transfer.TextData(strings.ReplaceAll(strings.Join(lines, "\n"), "🙂", "[微笑]"))
		}).Children(func() {
			ui.Text(c, "第一段正文🙂，可以跨段选择。")
			ui.Text(c, "第二段正文：全选只覆盖这一条消息。")
		})
		overlay.EditContextMenu(c, s.body, func(m *ui.Menu) {
			if m.Item("查看消息").Chosen() {
				s.event = "宿主动作已执行"
			}
		})
		ui.Text(c, "这条消息不在正文的选择范围内。")
		ui.Text(c, s.event)
	})
}
