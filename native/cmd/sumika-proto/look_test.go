package main

import (
	"image/color"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 外观的字号、圆角、疏密铺满整窗（审查 B1）：拉到最大，导航、顶行、会话行、内距、圆角都比默认大；拉到最小都比默认小。
// 测完把令牌恢复成默认（令牌是包级变量）。
func TestLookScalesWholeWindow(t *testing.T) {
	defer applyLook(chatui.Derive(chatui.DefaultSettings()))
	type sizes struct{ navH, navR, header, row, search, inset float32 }
	measure := func(s chatui.Settings) sizes {
		a := newApp()
		a.Look = s
		tt := ui.NewTester(a.view, 1400, 900)
		tt.Frame()
		tt.Frame()
		root := geometryTree(tt)
		nav, head, row, search := root.Find("nav-local"), root.Find("search-row"), root.Find("conversation-A"), root.Find("search")
		if nav == nil || head == nil || row == nil || search == nil {
			t.Fatalf("外观 %+v 下找不到导航、顶行、会话行或搜索框", s)
		}
		return sizes{nav.Box.H, nav.Radius[0], head.Box.H, row.Box.H, search.Box.H, head.Pad[0]}
	}
	def := measure(chatui.DefaultSettings())
	big := measure(chatui.Settings{Text: chatui.TextMax, Radius: chatui.RadiusMax, Density: chatui.DensityMax, Wallpaper: true, Glass: true})
	small := measure(chatui.Settings{Text: chatui.TextMin, Radius: chatui.RadiusMin, Density: chatui.DensityMin, Wallpaper: true, Glass: true})
	t.Logf("默认 %+v\n最大 %+v\n最小 %+v", def, big, small)
	for _, c := range []struct {
		what         string
		d, big, smal float32
	}{
		{"导航按钮高", def.navH, big.navH, small.navH}, {"导航按钮圆角", def.navR, big.navR, small.navR},
		{"顶行高", def.header, big.header, small.header}, {"会话行高", def.row, big.row, small.row},
		{"搜索框高", def.search, big.search, small.search}, {"会话栏内距", def.inset, big.inset, small.inset},
	} {
		if !(c.big > c.d+0.5) || !(c.smal < c.d-0.5) {
			t.Errorf("%s 没跟着外观变：最小 %.1f、默认 %.1f、最大 %.1f", c.what, c.smal, c.d, c.big)
		}
	}
}

// 深色下外层字重跟令牌减 40（审查 B2）。
func TestDarkWeights(t *testing.T) {
	defer applyLook(chatui.Derive(chatui.DefaultSettings()))
	s := chatui.DefaultSettings()
	s.Dark = true
	applyLook(chatui.Derive(s))
	if normalWeight != 360 || mediumWeight != 460 || strongWeight != 560 || boldWeight != 660 {
		t.Errorf("深色字重 %d/%d/%d/%d，期望 360/460/560/660", normalWeight, mediumWeight, strongWeight, boldWeight)
	}
}

// 会话行按下有自己的底色（审查 B7），按下时预览字仍过 4.5（换第二级灰）。
func TestConversationRowPressed(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Dark, a.Group = dark, 1
		a.Selected["local"] = "A" // 群聊分段里的 B 不是选中的
		tt := ui.NewTester(a.view, 1400, 700)
		tt.Frame()
		tt.Frame()
		row := geometryTree(tt).Find("conversation-B")
		if row == nil {
			t.Fatal("找不到会话行 B")
		}
		at := func() color.RGBA { return tt.Image().RGBAAt(int(row.Box.X+row.Box.W-3), int(row.Box.Y+row.Box.H/2)) }
		before := at()
		tt.Press(row.Box.X+row.Box.W-20, row.Box.Y+row.Box.H/2)
		tt.Frame()
		tt.Frame()
		pressed := at()
		if pressed == before {
			t.Errorf("深=%v 按下会话行底色没变：%v", dark, pressed)
		}
		r, ok := tt.Find("随时可以聊聊")
		if !ok {
			t.Fatal("找不到预览字")
		}
		img, best := tt.Image(), 0.0
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X+r.W); x++ {
				best = max(best, contrast(img.RGBAAt(x, y), pressed))
			}
		}
		if best < 4.5 {
			t.Errorf("深=%v 按下时预览字对比 %.2f < 4.5", dark, best)
		}
		tt.Release(row.Box.X+row.Box.W-20, row.Box.Y+row.Box.H/2)
	}
}

// 滑块圆钮深浅都白（Bun --thumb；审查 B6）。
func TestSliderThumbWhite(t *testing.T) {
	a := newApp()
	a.Dark, a.Page = true, "settings"
	tt := ui.NewTester(a.view, 2048, 1030)
	tt.Frame()
	tt.Frame()
	n := geometryTree(tt).Find("look-text")
	if n == nil {
		t.Fatal("找不到字号滑块那一行")
	}
	img := tt.Image()
	for y := int(n.Box.Y); y < int(n.Box.Y+n.Box.H); y++ {
		for x := int(n.Box.X); x < int(n.Box.X+n.Box.W); x++ {
			if p := img.RGBAAt(x, y); p.R == 252 && p.G == 252 && p.B == 252 {
				return
			}
		}
	}
	t.Error("深色下字号滑块里找不到白色圆钮")
}

// 设置页左栏只放分类、固定 275 宽（照 Codex 桌面版侧栏默认宽），分类项左对齐（主人 11-06 看图点头）。
func TestSettingsSidebar(t *testing.T) {
	for _, w := range []int{2048, 1280} {
		a := newApp()
		a.Page = "settings"
		tt := ui.NewTester(a.view, w, 900)
		tt.Frame()
		tt.Frame()
		root := geometryTree(tt)
		side, item := root.Find("sidebar"), root.Find("appearance")
		if side == nil || item == nil {
			t.Fatal("找不到设置左栏或外观分类项")
		}
		if !near(side.Box.W, settingsListWidth) {
			t.Errorf("宽 %d：设置左栏 %.1f，期望 %v", w, side.Box.W, settingsListWidth)
		}
		if len(item.Children) == 0 || !near(item.Children[0].Box.X-item.Box.X, spaceSmall) {
			t.Errorf("宽 %d：外观分类项的图标没有靠左（离左边 %.1f）", w, item.Children[0].Box.X-item.Box.X)
		}
	}
}

// 发送键、下拉箭头用 SVG 图标，不用文字符号（审查 D1；字形和基线不受控）。
func TestNoTextGlyphIcons(t *testing.T) {
	a := newApp()
	a.Scenes, a.Layer = true, 1
	tt := ui.NewTester(a.view, 2048, 1030)
	tt.Frame()
	tt.Frame()
	for _, g := range []string{"↑", "⌄"} {
		for _, s := range tt.Texts() {
			if strings.Contains(s, g) {
				t.Errorf("界面上还有文字符号 %q：%q", g, s)
			}
		}
	}
}

// 空状态说明写到"为什么空、什么时候会有"，常驻只占一行（R072；设计语言第十一节；审查 D3）。
func TestEmptyStatesOneLine(t *testing.T) {
	for _, w := range []int{2048, 1100} { // 1100 宽时右栏压到 292，最窄
		a := newApp()
		a.Search = "找不到的词"
		tt := ui.NewTester(a.view, w, 1030)
		tt.Frame()
		tt.Frame()
		for _, c := range []struct {
			s   string
			pad float32 // 这条文字自己的上下内距
		}{{"没有找到会话，换个词试试", 2 * listInset}, {"还没有活动，她查资料时会列在这里", 0}} {
			s := c.s
			r, ok := tt.Find(s)
			if !ok {
				t.Errorf("没有空状态说明 %q", s)
				continue
			}
			if r.H > labelLine*1.5+c.pad {
				t.Errorf("宽 %d：%q 折成了不止一行（高 %.1f）", w, s, r.H)
			}
		}
	}
}
