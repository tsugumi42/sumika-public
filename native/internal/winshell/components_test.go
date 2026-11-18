package winshell

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func abs32(v float32) float32 { return float32(math.Abs(float64(v))) }

type segFrame struct {
	track, cell ui.Rect
	indX, indW  float32
}

// 分段控件放在滚动区里：滚动的每一帧，选中块的位置（相对轨道）和选中项相对轨道的位置完全一致；
// 选中块只有选项真的换了才滑动，滚动不会把它当成位置变化重新播放（主人 2026-11-08 在真窗口里看到选中块滚动时飞出）。
func TestSegmentStaysAlignedWhileScrolling(t *testing.T) {
	k := testKit()
	sel, on := 3, false
	names := []string{"13", "14", "15", "16", "17", "18"}
	var frames []segFrame
	view := func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Scroll(c.Key("page")).Fill().Background(k.P.Pane).Children(func() {
			ui.Column(c).FillWidth().Padding(10).Gap(10).Children(func() {
				for i := range 12 {
					ui.Text(c.Key(fmt.Sprint("row", i)), fmt.Sprint("第 ", i, " 行")).Height(30)
				}
				k.Segment(c, "seg", &sel, names)
				k.Switch(c, "sw", "开关", &on)
				for i := range 30 {
					ui.Text(c.Key(fmt.Sprint("after", i)), fmt.Sprint("后 ", i, " 行")).Height(30)
				}
			})
		})
		if hs := k.SegHandles["seg"]; hs != nil {
			frames = append(frames, segFrame{hs.Track.Bounds(c), hs.Cells[sel].Bounds(c), hs.IndX, hs.IndW})
		}
	}
	tt := ui.NewTester(view, 600, 500)
	tt.SetScale(1.25)
	for range 4 {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	settled := len(frames)
	var base float32
	for step := 0; step < 40; step++ {
		tt.Wheel(300, 250, 0, -3)
		for range 3 {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
	}
	for i := settled; i < len(frames); i++ {
		f := frames[i]
		if f.track.W == 0 || f.cell.W == 0 {
			continue
		}
		if i == settled {
			base = f.indX
		}
		if d := abs32((f.cell.X - f.track.X) - f.indX); d > 0.5 {
			t.Fatalf("第 %d 帧：选中块 x=%.2f，选中项相对轨道 x=%.2f，差 %.2f", i, f.indX, f.cell.X-f.track.X, d)
		}
		if d := abs32(f.cell.Y - f.track.Y - k.G.SpaceTight); d > 0.5 {
			t.Fatalf("第 %d 帧：选中项离轨道顶 %.2f，应为 %.2f（选中块按轨道画，纵向不会飘）", i, f.cell.Y-f.track.Y, k.G.SpaceTight)
		}
		if abs32(f.indX-base) > 0.01 || abs32(f.indW-f.cell.W) > 0.5 {
			t.Fatalf("第 %d 帧：只滚动，选中块动了 x %.2f → %.2f，宽 %.2f（选项宽 %.2f）", i, base, f.indX, f.indW, f.cell.W)
		}
	}
	if len(frames)-settled < 60 {
		t.Fatalf("只量到 %d 帧", len(frames)-settled)
	}
}

// 真换了选项：选中块在几帧里滑到新的那一格，最后和选项对齐。
func TestSegmentSlidesOnlyWhenSelectionChanges(t *testing.T) {
	k := testKit()
	sel := 0
	var frames []segFrame
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c).Fill().Padding(10).Children(func() { k.Segment(c, "seg", &sel, []string{"甲", "乙", "丙"}) })
		if hs := k.SegHandles["seg"]; hs != nil {
			frames = append(frames, segFrame{hs.Track.Bounds(c), hs.Cells[sel].Bounds(c), hs.IndX, hs.IndW})
		}
	}, 400, 200)
	for range 4 {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	if err := tt.Click("丙"); err != nil {
		t.Fatal(err)
	}
	from := len(frames)
	moved := map[float32]bool{}
	for range 40 {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	for _, f := range frames[from:] {
		moved[float32(math.Round(float64(f.indX)))] = true
	}
	last := frames[len(frames)-1]
	if len(moved) < 3 {
		t.Fatalf("换选项后选中块没有滑动，只出现过 %d 个位置", len(moved))
	}
	if abs32((last.cell.X-last.track.X)-last.indX) > 0.5 {
		t.Fatalf("滑完没对齐：选中块 %.2f，选项 %.2f", last.indX, last.cell.X-last.track.X)
	}
}

// 分段控件四边等距、圆角同心（LESSONS：嵌套块四边等距，内圆角 = 外圆角 − 内边距）：
// 选中项离轨道的左、右、上、下四个距离相等，都是内边距；选中块圆角 = 轨道圆角 − 内边距。
func TestSegmentFourSidesEqualAndConcentric(t *testing.T) {
	k := testKit()
	sel := 0
	var track, first, last ui.Rect
	var trackR, cellR float32
	k.Surface = func(c *ui.Context, e ui.Element, info SurfaceInfo) {
		switch info.ID {
		case "seg":
			trackR = info.Radius
		case "seg0":
			cellR = info.Radius
		}
	}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c).Fill().Padding(10).Children(func() { k.Segment(c, "seg", &sel, []string{"按天", "按周", "按月"}) })
		if hs := k.SegHandles["seg"]; hs != nil {
			track, first, last = hs.Track.Bounds(c), hs.Cells[0].Bounds(c), hs.Cells[2].Bounds(c)
		}
	}, 400, 200)
	for range 3 {
		tt.Advance(50 * time.Millisecond)
		tt.Frame()
	}
	pad := k.G.SpaceTight
	gaps := map[string]float32{
		"左": first.X - track.X, "右": track.X + track.W - (last.X + last.W),
		"上": first.Y - track.Y, "下": track.Y + track.H - (first.Y + first.H),
	}
	for side, v := range gaps {
		if abs32(v-pad) > 0.5 {
			t.Errorf("%s边距 %.2f，应与内边距 %.2f 相等：%v", side, v, pad, gaps)
		}
	}
	// 单行的轨道是胶囊（圆角 = 高度一半），选项和选中块圆角 = 轨道圆角 − 内边距
	if want := track.H / 2; abs32(trackR-want) > 0.01 {
		t.Errorf("轨道圆角 %v，应为高 %v 的一半 %v", trackR, track.H, want)
	}
	if want := trackR - pad; abs32(cellR-want) > 0.01 {
		t.Errorf("选中块圆角 %v，应为轨道圆角 %v 减内边距 %v", cellR, trackR, pad)
	}
}

// 键盘：焦点在分段上，左右方向键切换选项。
func TestSegmentKeyboardArrows(t *testing.T) {
	k := testKit()
	sel := 1
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c).Fill().Padding(10).Children(func() { k.Segment(c, "seg", &sel, []string{"甲", "乙", "丙"}) })
	}, 400, 200)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if err := tt.Click("乙"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if sel != 2 {
		t.Fatalf("右方向键后选中 %d，应为 2", sel)
	}
	tt.Key(0, ui.KeyLeft)
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if sel != 0 {
		t.Fatalf("左方向键两次后选中 %d，应为 0", sel)
	}
}

// 筛选片：点哪个选哪个，放不下的换行，选中项变了才报告变化。
func TestChipsSelectAndWrap(t *testing.T) {
	k := testKit()
	sel := 0
	changes := 0
	labels := []string{"全部", "事实", "约定", "喜好", "经历", "关系", "计划", "习惯", "梦想", "禁忌"}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c).Fill().Padding(10).Children(func() {
			if k.Chips(c, "chips", labels, &sel) {
				changes++
			}
		})
	}, 260, 300)
	tt.Frame()
	a, _ := tt.Find("全部")
	z, _ := tt.Find("禁忌")
	if z.Y <= a.Y {
		t.Fatalf("放不下的筛选片没有换行：全部 y=%v 禁忌 y=%v", a.Y, z.Y)
	}
	if err := tt.Click("约定"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if sel != 2 || changes != 1 {
		t.Fatalf("点约定后 sel=%d changes=%d", sel, changes)
	}
	if err := tt.Click("约定"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if changes != 1 {
		t.Fatalf("点已选中的不该再报变化：%d", changes)
	}
}
