package winshell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 按钮族的几个状态（组件规格总则第六节查出的缺口，2026-11-11）。

// 主按钮悬停换成角色色阶梯的 hover 档（原来悬停、按下都不变色）。
func TestPrimaryButtonHover(t *testing.T) {
	k := testKit()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c.Key("bg")).FillWidth().FillHeight().Background(k.P.Composer).Center().Children(func() {
			k.Button(c, "b", "发送", true, false, func() {})
		})
	}, 120, 80)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	at := func() ui.Color {
		px := tt.Image().RGBAAt(60-12, 40) // 按钮左边、文字之外的一点
		return ui.Color{R: px.R, G: px.G, B: px.B, A: px.A}
	}
	before := at()
	tt.Move(60, 40)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if got := at(); got == before || got != k.P.AccentHover {
		t.Errorf("主按钮悬停：之前 %+v，之后 %+v，应是 AccentHover %+v", before, got, k.P.AccentHover)
	}
}

// 禁用的图标钮点了没反应，整体降透明度。
func TestIconButtonDisabled(t *testing.T) {
	k := testKit()
	clicked := 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c.Key("bg")).FillWidth().FillHeight().Background(k.P.Composer).Center().Children(func() {
			k.IconButtonWith(c, "b", "试试", IconSide, IconButtonOptions{Disabled: true}, func() { clicked++ })
		})
	}, 80, 80)
	tt.Frame()
	tt.ClickAt(40, 40)
	tt.Frame()
	if clicked != 0 {
		t.Fatalf("禁用的图标钮被点了 %d 次", clicked)
	}
	info, ok := tt.Access("试试")
	if !ok || !info.Disabled {
		t.Errorf("读屏应报禁用：%+v %v", info, ok)
	}
}
