package winshell

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/chatui"
	"sumika-native/internal/design/colorx"
)

// 设计硬化（2026-11-17 体检，主人在画廊对比小样里挑定 A5、B1、C2）落进正式令牌和组件以后的样子。

func okL(c ui.Color) float64 { return colorx.FromColor(c).L }

// A5：凹槽是半透明暗层，放在页面底上正好深 WellDepth；亮色放到白卡片上也差不多深同样多（原来实色在卡片上深一倍多）。
func TestWellIsRelative(t *testing.T) {
	for _, dark := range []bool{false, true} {
		s := chatui.DefaultSettings()
		s.Dark = dark
		k := NewKit(s, "")
		c := k.T.C
		if k.P.Well.A == 255 || k.P.Well.A == 0 {
			t.Fatalf("dark=%v 凹槽底应是半透明的一层，现在 A=%d", dark, k.P.Well.A)
		}
		onBg := okL(k.P.Well.Over(c.Bg)) - okL(c.Bg)
		if math.Abs(onBg+WellDepth) > .005 {
			t.Errorf("dark=%v 凹槽在页面底上应深 %.1f，现在 %.1f", dark, WellDepth*100, -onBg*100)
		}
		card := k.raisedFill()
		if card.A < 255 {
			card = card.Over(c.Bg)
		}
		onCard := okL(k.P.Well.Over(card)) - okL(card)
		if onCard > -.03 {
			t.Errorf("dark=%v 凹槽在卡片上应看得出凹（至少深 3），现在 %.1f", dark, -onCard*100)
		}
		if !dark && math.Abs(onCard-onBg) > .01 {
			t.Errorf("亮色凹槽在卡片上和页面底上应深得差不多：%.1f / %.1f", -onCard*100, -onBg*100)
		}
		if k.P.Track != c.Sunken {
			t.Errorf("dark=%v “平的”那档不跟着凹槽变，应仍是实色凹块底", dark)
		}
	}
}

// B1：模式和位置类的选中是带影子的浮起块，标签类是不带影子的强调色浅底；夜间标签类的浅底比没选中的亮。
func TestSelectByRole(t *testing.T) {
	for _, dark := range []bool{false, true} {
		s := chatui.DefaultSettings()
		s.Dark = dark
		k := NewKit(s, "")
		if k.selectFill(selMode) != k.raisedFill() {
			t.Errorf("dark=%v 模式类的选中应是浮起块的底", dark)
		}
		if k.selectFill(selTag) != k.softSelect() {
			t.Errorf("dark=%v 标签类的选中应是强调色浅底", dark)
		}
		if dark && okL(k.selectFill(selTag)) <= okL(k.P.Track) {
			t.Errorf("夜间筛选片的选中应比没选中的亮")
		}
	}
}

// C1：二级字压深到亮 0.38 / 夜 0.81，三级字对页面底仍有 4.5:1，三档明度拉开。
func TestTextSteps(t *testing.T) {
	for _, dark := range []bool{false, true} {
		s := chatui.DefaultSettings()
		s.Dark = dark
		c := NewKit(s, "").T.C
		want := .38
		if dark {
			want = .81
		}
		if l := okL(c.Text2); math.Abs(l-want) > .01 {
			t.Errorf("dark=%v 二级字明度应约 %.2f，现在 %.3f", dark, want, l)
		}
		if math.Abs(okL(c.Text2)-okL(c.Text3)) < .1 {
			t.Errorf("dark=%v 二级字和三级字应拉开至少 10 个明度：%.3f / %.3f", dark, okL(c.Text2), okL(c.Text3))
		}
		if r := chatui.Contrast(c.Text3, c.Bg); r < chatui.MinTextContrast {
			t.Errorf("dark=%v 三级字对页面底应至少 4.5:1，现在 %.2f", dark, r)
		}
	}
}
