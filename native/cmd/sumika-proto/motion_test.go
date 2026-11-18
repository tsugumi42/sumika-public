package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 回弹铺到位移上（ADR-285 第 6 条）：开关圆钮、分段选中块切换时冲过终点再回来；减少动态效果时直接到位。
// 每 16ms 推进一帧，记下位置的轨迹：最远点要越过终点、中途要有不在两端的位置。

func trajectory(tt *ui.Tester, n int, pos func() float32) []float32 {
	xs := []float32{}
	for range n {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		xs = append(xs, pos())
	}
	return xs
}

func checkBounce(t *testing.T, what string, xs []float32, from, to float32) {
	t.Helper()
	dir := float32(1)
	if to < from {
		dir = -1
	}
	far, mid := from, false
	for _, x := range xs {
		if (x-far)*dir > 0 {
			far = x
		}
		if (x-from)*dir > 0.5 && (to-x)*dir > 0.5 {
			mid = true
		}
	}
	if !mid {
		t.Errorf("%s 没有中间位置，是直接跳过去的：%v", what, xs)
	}
	if (far-to)*dir < 0.2 {
		t.Errorf("%s 没有冲过终点（终点 %.2f，最远 %.2f），不是回弹", what, to, far)
	}
	if last := xs[len(xs)-1]; !near(last, to) {
		t.Errorf("%s 最后没停在终点：%.2f，期望 %.2f", what, last, to)
	}
}

var switchApp *app

func switchThumbX(tt *ui.Tester) float32 { return switchApp.SwitchX["look-glass"] }

var segApp *app

func segIndicatorX(tt *ui.Tester) float32 { return segApp.SegHandles["venues"].IndX }

func TestSwitchThumbBounces(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		a := newApp()
		switchApp = a
		a.Page = "settings"
		tt := ui.NewTester(a.view, 2048, 1030)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		tt.Frame()
		from := switchThumbX(tt)
		a.Look.Glass = !a.Look.Glass
		tt.Frame()
		travel := (38 - 18 - 2*2) * a.T.S.Text / 16 // 轨道 38 − 圆钮 18 − 两边各 2
		to := from + travel
		if !a.Look.Glass {
			to = from - travel
		}
		if reduce {
			tt.Frame()
			if x := switchThumbX(tt); !near(x, to) {
				t.Errorf("减少动态效果时开关圆钮没直接到位：%.2f，期望 %.2f", x, to)
			}
			continue
		}
		checkBounce(t, "开关圆钮", trajectory(tt, 60, func() float32 { return switchThumbX(tt) }), from, to)
	}
}

func TestSegmentIndicatorBounces(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		a := newApp()
		segApp = a
		tt := ui.NewTester(a.view, 1400, 700)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		tt.Frame()
		tt.Frame()
		from := segIndicatorX(tt)
		a.Group = 1 - a.Group
		tt.Frame()
		tt.Frame() // 下一帧才量到新的选中格位置
		root := geometryTree(tt)
		trackNode := root.Find("venues")
		cells := trackNode.Children
		to := cells[a.Group].Box.X - trackNode.Box.X
		if reduce {
			if x := segIndicatorX(tt); !near(x, to) {
				t.Errorf("减少动态效果时选中块没直接到位：%.2f，期望 %.2f", x, to)
			}
			continue
		}
		checkBounce(t, "分段选中块", append([]float32{segIndicatorX(tt)}, trajectory(tt, 60, func() float32 { return segIndicatorX(tt) })...), from, to)
	}
}

// 选中格的字要看得清：选中块不能盖在字上（2026-11-06 第一版的选中块是单独的浮层元素，把"群聊"两个字盖没了）。
func TestSegmentSelectedTextVisible(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Dark, a.Group = dark, 1
		tt := ui.NewTester(a.view, 1400, 700)
		for range 40 {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
		}
		r, ok := tt.Find("群聊")
		if !ok {
			t.Fatal("找不到群聊分段")
		}
		img := tt.Image()
		bg := img.RGBAAt(int(r.X)+1, int(r.Y)+1)
		best := 0.0
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X+r.W); x++ {
				best = max(best, contrast(img.RGBAAt(x, y), bg))
			}
		}
		if best < 4.5 {
			t.Errorf("深=%v 选中的分段字看不见：字和底最大对比 %.2f", dark, best)
		}
	}
}

// 消息出现（R068；审查 C4）：切到设置再切回来，消息重新挂上，第一帧就在原位、看得清，不重播；
// 新发的消息从下面升上来：第一帧比停稳的位置低（或还看不见）。
func TestEnterOnlyForNewMessages(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1400, 700)
	tt.Frame() // 打开会话的第一帧：已有的消息直接在原位，不集体播出现动画
	r, ok := tt.Find("窗边的纸灯亮起来了。今天想聊些什么？")
	if !ok {
		t.Fatal("找不到第一条消息")
	}
	opened := inkTop(tt, r)
	for range 3 {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	before := inkTop(tt, r)
	if opened != before || opened < 0 {
		t.Errorf("打开会话时已有的消息播了出现动画：第一帧字上沿 %d，停稳 %d", opened, before)
	}
	a.Page = "settings"
	tt.Advance(2 * time.Second)
	tt.Frame()
	a.Page = "local"
	tt.Frame() // 切回来的第一帧
	if after := inkTop(tt, r); after != before || after < 0 {
		t.Errorf("切页回来消息重播了进场：第一帧字的上沿 %d，原来 %d", after, before)
	}
	// 新发一条：第一帧还在进场（看不清或不在原位）
	a.E.submit(a.cv(), "刚发的一条消息")
	tt.Frame()
	nr, ok := tt.Find("刚发的一条消息")
	if !ok {
		t.Fatal("找不到新消息")
	}
	first := inkTop(tt, nr)
	tt.Advance(2 * time.Second)
	tt.Frame()
	nr, _ = tt.Find("刚发的一条消息")
	if settled := inkTop(tt, nr); first >= 0 && first <= settled {
		t.Errorf("新消息没有从下面升上来：第一帧字上沿 %d，停稳 %d", first, settled)
	}
}

// inkTop 是 r 这块区域（往下多看 12）里第一行深色像素的 y（物理像素），没有就 -1。
func inkTop(tt *ui.Tester, r ui.Rect) int {
	img := tt.Image()
	k := float32(img.Bounds().Dx()) / 1400
	for y := int(r.Y * k); y < int((r.Y+r.H+12)*k); y++ {
		for x := int(r.X * k); x < int((r.X+r.W)*k); x++ {
			if p := img.RGBAAt(x, y); int(p.R)+int(p.G)+int(p.B) < 3*140 {
				return y
			}
		}
	}
	return -1
}

// 窄窗口换层有过渡（审查 B8、R065）：从列表进聊天，第一帧标题偏右或还看不见，停稳后回原位；启动第一帧不播。
func TestNarrowLayerSlides(t *testing.T) {
	for _, start := range []int{0, 1, 2} { // 从任何一层启动，第一帧都不当成换层
		b := newApp()
		b.Layer = start
		bt := ui.NewTester(b.view, 640, 700)
		bt.Frame()
		if b.LayerDir != 0 {
			t.Fatalf("从第 %d 层启动，第一帧就当成换层了", start)
		}
	}
	a := newApp()
	tt := ui.NewTester(a.view, 640, 700)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	a.Layer = 1
	tt.Frame()
	first := titleLeft(tt, a.cv().Name, 640, 0)
	tt.Advance(time.Second)
	tt.Frame()
	settled := titleLeft(tt, a.cv().Name, 640, 0)
	if settled < 0 {
		t.Fatal("停稳后找不到聊天标题")
	}
	if first >= 0 && first <= settled {
		t.Errorf("进聊天没有从右边滑进来：第一帧标题左缘 %d，停稳 %d", first, settled)
	}
}

// titleLeft 是聊天标题（会话名）那一行里第一个深色像素的 x（物理像素），没有就 -1。
func titleLeft(tt *ui.Tester, name string, w, from float32) int {
	r, ok := tt.Find(name)
	if !ok {
		return -1
	}
	img := tt.Image()
	k := float32(img.Bounds().Dx()) / w
	for x := int(from * k); x < img.Bounds().Dx(); x++ { // 从 from 往右找（抽屉那一侧，避开遮罩后面的字）
		for y := int(r.Y * k); y < int((r.Y+r.H)*k); y++ {
			if p := img.RGBAAt(x, y); int(p.R)+int(p.G)+int(p.B) < 3*110 {
				return x
			}
		}
	}
	return -1
}

// 窄窗右栏抽屉从右边滑出来（审查 B8）：打开那一帧"资料与活动"偏右或还看不见，停稳后回原位。
func TestDrawerSlides(t *testing.T) {
	a := newApp()
	a.Layer = 1
	tt := ui.NewTester(a.view, 900, 700)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	a.Layer, a.Right = 2, true
	tt.Frame()
	tt.Frame() // 打开那一帧给起点，下一帧开始往原位走
	first := titleLeft(tt, "资料与活动", 900, 900-sideMin-1)
	for range 40 {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	settled := titleLeft(tt, "资料与活动", 900, 900-sideMin-1)
	if settled < 0 {
		t.Fatal("停稳后找不到抽屉标题")
	}
	if first >= 0 && first <= settled {
		t.Errorf("抽屉没有从右边滑出：第一帧 %d，停稳 %d", first, settled)
	}
}
