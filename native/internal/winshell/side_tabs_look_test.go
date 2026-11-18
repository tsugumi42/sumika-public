package winshell

import (
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 右栏顶行的小胶囊放得下几个选项卡：右栏默认宽放 5 个图标或 3 个两到四字的文字，最窄也放得下 4 个图标。
func TestSideTabsRoom(t *testing.T) {
	k := NewKit(chatui.DefaultSettings(), "")
	k.Win = &WindowControls{Minimize: func() {}, Toggle: func() {}, Close: func() {}}
	icons := []*ui.SVG{IconInfo, IconHome, IconSpark, IconSearch, IconSettings}
	tabIcons := func(c *ui.Context, n int) func() {
		return func() {
			ui.Row(c.Key("tabs")).Grow(1).MinWidth(0).Gap(k.G.SpaceMicro).AlignItems(ui.Center).Children(func() {
				for i := range n {
					k.IconButton(c, "tab-"+strconv.Itoa(i), "选项卡"+strconv.Itoa(i), icons[i], func() {})
				}
			})
		}
	}
	tabWords := func(c *ui.Context, words ...string) func() {
		return func() {
			ui.Row(c.Key("tabs")).Grow(1).MinWidth(0).Gap(k.G.SpaceMicro).AlignItems(ui.Center).Children(func() {
				for i, word := range words {
					ui.Text(c.Key(i), word).Grow(1).MinWidth(0).FontSize(k.G.LabelSize).FixedLineHeight(k.G.MetaLine).SingleLine()
				}
			})
		}
	}
	draw := func(c *ui.Context) {
		c.Root().Background(k.P.Canvas)
		ui.Row(c.Key("all")).Fill().Gap(k.G.SpaceSmall).Children(func() {
			for i, panel := range []struct {
				w    float32
				tabs func() func()
			}{
				{SideDefault, func() func() { return nil }},
				{SideDefault, func() func() { return tabWords(c, "资料", "活动", "日志") }},
				{SideDefault, func() func() { return tabIcons(c, 5) }},
				{SideMin, func() func() { return tabIcons(c, 4) }},
				{SideMax, func() func() { return tabIcons(c, 5) }},
			} {
				ui.Box(c.Key(i)).Width(panel.w).FillHeight().Shrink(0).Children(func() {
					st := &State{}
					k.side(c, st, &SideSpec{Tabs: panel.tabs(), ActivityNote: "活动记录"}, sideColumn, nil)
				})
			}
		})
	}
	tt := ui.NewTester(draw, 1720, 200)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetScale(1.25)
	tt.Frame()
	tt.Frame()
	for _, i := range []int{0, 1, 2, 3, 4} {
		if _, ok := tt.Find("选项卡" + strconv.Itoa(i)); !ok {
			t.Fatalf("没有第 %d 个选项卡", i)
		}
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "右栏顶行.png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
	}
}
