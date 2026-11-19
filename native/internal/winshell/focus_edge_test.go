package winshell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/chatui"
	"sumika-native/internal/design"
)

// 组件库体检（2026-11-17）的 D：焦点环、控件边界对各层底都过 3:1（WCAG 2.4.7、1.4.11）。
// 原来焦点环是角色色本身（浅色面上约 2:1）、离边 1；关着的开关和滑块轨道约 1.2:1。
func TestFocusRingAndEdgeContrast(t *testing.T) {
	for _, dark := range []bool{false, true} {
		s := chatui.DefaultSettings()
		s.Dark = dark
		k := NewKit(s, "")
		c := k.T.C
		for name, bg := range map[string]ui.Color{"页面底": c.Bg, "内容区": c.Surface, "凹块": c.Sunken} {
			if r := chatui.Contrast(k.Theme.Focus, bg); r < chatui.MinIconContrast {
				t.Errorf("dark=%v 焦点环对%s只有 %.2f:1", dark, name, r)
			}
		}
		if k.Theme.FocusRingOffset != design.FocusRingOffset {
			t.Errorf("dark=%v 焦点环离边应是 %v，现在 %v", dark, design.FocusRingOffset, k.Theme.FocusRingOffset)
		}
		for name, bg := range map[string]ui.Color{"页面底": c.Bg, "内容区": c.Surface} {
			if r := chatui.Contrast(k.P.Edge, bg); r < chatui.MinIconContrast {
				t.Errorf("dark=%v 控件边界色对%s只有 %.2f:1", dark, name, r)
			}
		}
	}
}

// 输入框点进去以后，那圈细线换成焦点色（原来聚焦和没聚焦一模一样）。
func TestTextFieldShowsFocus(t *testing.T) {
	k := testKit()
	name := "晴枝"
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c.Key("bg")).FillWidth().FillHeight().Background(k.P.Canvas).Center().Children(func() {
			k.TextField(c, "f", &name, "名字").Width(200)
		})
	}, 300, 80)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	edge := func() ui.Color { // 胶囊最左边、竖直正中：细线在这里是竖的一段
		px := tt.Image().RGBAAt(51, 40)
		return ui.Color{R: px.R, G: px.G, B: px.B, A: px.A}
	}
	before := edge()
	tt.ClickAt(150, 40)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	after := edge()
	dist := func(a, b ui.Color) int {
		d := func(x, y uint8) int { v := int(x) - int(y); return v * v }
		return d(a.R, b.R) + d(a.G, b.G) + d(a.B, b.B)
	}
	if after == before || dist(after, k.Theme.Focus) > dist(after, k.P.Line) {
		t.Errorf("点进输入框以后边上应是焦点色 %+v：之前 %+v，之后 %+v", k.Theme.Focus, before, after)
	}
}
