package overlay

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestEditContextMenuScopeCopyDataAndFocus(t *testing.T) {
	var handle ui.Handle
	var parts []ui.SelectedText
	var focus bool
	copies, hostActions := 0, 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Column(c).Fill().Children(func() {
			ui.Column(c.Key("body")).Selectable().Bind(&handle).CopyData(func(selected []ui.SelectedText) transfer.Data {
				copies++
				var text []string
				for _, p := range selected {
					text = append(text, string([]rune(p.Text)[p.Start:p.End]))
				}
				return transfer.TextData(strings.ReplaceAll(strings.Join(text, "\n"), "🙂", "[微笑]"))
			}).Children(func() { ui.Text(c, "正文🙂"); ui.Text(c, "第二段") })
			ui.Column(c).Selectable().Children(func() { ui.Text(c, "另一条消息") })
			EditContextMenu(c, handle, func(m *ui.Menu) {
				if m.Item("宿主动作").Chosen() {
					hostActions++
				}
			})
			parts, focus = c.Resolve(handle).SelectedTexts(), handle.FocusWithin(c)
		})
	}, 400, 300)
	click(t, tt, "正文🙂")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	check(t, "keyboard copy uses body formatter", tt.Clipboard() == "正文[微笑]\n第二段" && copies == 1)
	if err := tt.RightClick("正文🙂"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	check(t, "menu copy uses same CopyData", tt.Clipboard() == "正文[微笑]\n第二段" && copies == 2)
	check(t, "menu keeps original focus and selection", focus && len(parts) == 2 && parts[0].Start == 0 && parts[1].End == len([]rune("第二段")))
	tt.Key(ui.Cmd, ui.KeyC)
	check(t, "copy still works after menu closes", copies == 3 && tt.Clipboard() == "正文[微笑]\n第二段")
	click(t, tt, "正文🙂")
	tt.Key(ui.Shift, ui.KeyF10)
	if err := tt.ChooseMenuItem("Select All"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyC)
	check(t, "select all stays within persistent body scope", focus && tt.Clipboard() == "正文[微笑]\n第二段")
	if err := tt.RightClick("第二段"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("宿主动作"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	check(t, "host action shares one standard menu", hostActions == 1 && focus && len(parts) == 2)
}
