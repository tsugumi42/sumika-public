package winshell

import (
	"image"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 按下缩放（ADR-349）：按住后图标钮缩到 scale.press-icon，松手弹回原大；系统减少动态效果时不缩，只留按下的底色。
func TestPressScale(t *testing.T) {
	// held 是按住 300 毫秒后的覆盖量，released 是松手 600 毫秒后的覆盖量
	run := func(reduce bool) (held, released float64) {
		k := testKit()
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Box(c.Key("bg")).FillWidth().FillHeight().Background(k.P.Composer).Center().Children(func() {
				k.IconButton(c, "b", "试试", IconSide, func() {})
			})
		}, 80, 80)
		tt.SetScale(1.25)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		tt.Press(40, 40)
		tt.Frame()
		tt.Advance(300 * time.Millisecond)
		tt.Frame()
		held = coverage(tt.Image())
		tt.Release(40, 40)
		tt.Frame()
		tt.Advance(600 * time.Millisecond)
		tt.Frame()
		released = coverage(tt.Image())
		return
	}
	still, hover := run(true)
	held, released := run(false)
	if r := held / still; math.Abs(r-0.96*0.96) > 0.03 {
		t.Errorf("按住后覆盖量是不缩时的 %.3f 倍，缩到 .96 应约 %.3f", r, 0.96*0.96)
	}
	// 松手后指针还在按钮上，显示悬停底色；回到原大就和不缩那一版松手后一样
	if r := released / hover; math.Abs(r-1) > 0.01 {
		t.Errorf("松手 600 毫秒后覆盖量是不缩时的 %.3f 倍，应回到 1", r)
	}
}

// coverage 是和左上角底色不同的颜色差总和（按下底色加图标的“面积”，抗锯齿的边按差多少算一部分）。
func coverage(img *image.RGBA) float64 {
	bg := img.RGBAAt(0, 0)
	var sum float64
	b := img.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := img.RGBAAt(x, y)
			sum += math.Abs(float64(c.R)-float64(bg.R)) + math.Abs(float64(c.G)-float64(bg.G)) + math.Abs(float64(c.B)-float64(bg.B))
		}
	}
	return sum
}
