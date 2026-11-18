package winshell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 右键菜单和按钮菜单一样从点的那里展开（主人 2026-11-16：“右键菜单展开时有跳变”）：右键以后面板露出来的高度逐帧变大，不是一下子整块出现。
func TestContextMenuUnfolds(t *testing.T) {
	k := testKit()
	k.SetMaterial(MaterialSolid)
	tt := motionTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		c.Root().Background(ui.RGB(0, 0, 0))
		ui.Column(c.Key("root")).Padding(20).Children(func() {
			card := ui.Row(c.Key("card")).Label("右键这里").Size(200, 60).Background(ui.RGB(0, 0, 0)).Focusable()
			k.ContextMenu(c, card, "ctx", []MenuItem{{Label: "复制"}, {Label: "回复"}, {Label: "标为已读"}})
		})
	}, 400, 400, false)
	tt.Frame()
	tt.Frame()
	if err := tt.RightClick("右键这里"); err != nil {
		t.Fatal(err)
	}
	// 面板在黑底上：数 x=150 那一列（右键点在卡片正中 x=120，菜单从那里往右下开）亮起来的行数
	lit := func() int {
		img := tt.Image()
		n := 0
		for y := 0; y < img.Bounds().Dy(); y++ {
			if lum(img.RGBAAt(150, y)) > 60 {
				n++
			}
		}
		return n
	}
	var hs []int
	for i := 0; i < 40; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		hs = append(hs, lit())
		if !tt.FramePending() {
			break
		}
	}
	full := hs[len(hs)-1]
	mid := 0
	for _, h := range hs {
		if h > 3 && h < full-3 {
			mid++
		}
	}
	if full < 60 || mid < 3 {
		t.Errorf("右键菜单没有逐帧展开：每帧亮起的行数 %v（全高 %d）", hs, full)
	}
}
