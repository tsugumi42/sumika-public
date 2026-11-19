package winshell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Disclose（主人 2026-11-15“设置页折叠”）：展开时外框的高度从 0 单调长到内容本来的高、不过冲，内容一直按全高排（字不挤）；
// 收起时单调收到 0，收完不再画；减少动态效果时直接到位。
func TestDiscloseGrowsAndShrinks(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		k := testKit()
		open := false
		var box ui.Handle
		var outer ui.Rect
		tt := motionTester(func(c *ui.Context) {
			c.SetTheme(&k.Theme)
			outer = box.Bounds(c)
			ui.Column(c.Key("root")).Width(300).Children(func() {
				ui.Text(c, "上面").Height(20)
				ui.Column(c.Key("wrap")).FillWidth().Bind(&box).Children(func() {
					k.Disclose(c, "d", open, 0, func() {
						for _, s := range []string{"第一行", "第二行", "第三行"} {
							ui.Text(c, s).Height(30)
						}
					})
				})
				ui.Text(c, "下面").Height(20)
			})
		}, 400, 400, reduce)
		tt.Frame()
		tt.Frame()
		if outer.H > .5 {
			t.Fatalf("收着时占了 %.1f 高", outer.H)
		}
		open = true
		var hs []float32
		for i := 0; i < 60; i++ {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
			hs = append(hs, outer.H)
			if i > 3 && !tt.FramePending() {
				break
			}
		}
		tt.Frame()
		full := outer.H
		if full < 89 || full > 91 {
			t.Fatalf("reduce=%v 展开到位高 %.1f，应是三行 90：%v", reduce, full, hs)
		}
		mid := 0
		for i, h := range hs {
			if h > full+.01 {
				t.Fatalf("reduce=%v 展开第 %d 帧 %.2f 超过全高 %.2f（高度不许过冲）", reduce, i, h, full)
			}
			if i > 0 && h < hs[i-1]-.01 {
				t.Fatalf("reduce=%v 展开时高度回头：%v", reduce, hs)
			}
			if h > 5 && h < full-5 {
				mid++
			}
		}
		if !reduce && mid < 3 {
			t.Errorf("展开中途只有 %d 帧，应是逐帧长开：%v", mid, hs)
		}
		if reduce && mid > 1 {
			t.Errorf("减少动态效果时高度还在逐帧长：%v", hs)
		}
		open = false
		hs = hs[:0]
		for i := 0; i < 60; i++ {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
			hs = append(hs, outer.H)
			if !tt.FramePending() {
				break
			}
		}
		tt.Frame()
		tt.Frame()
		for i := 1; i < len(hs); i++ {
			if hs[i] > hs[i-1]+.01 {
				t.Fatalf("reduce=%v 收起时高度回头：%v", reduce, hs)
			}
		}
		if outer.H > .5 || tt.HasText("第一行") {
			t.Errorf("reduce=%v 收完还占 %.1f 高或还画着内容", reduce, outer.H)
		}
	}
}
