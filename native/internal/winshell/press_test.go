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
	// 松手后指针还在按钮上：显示悬停底色，鼠标悬停放大到 1.025（面积 1.025²）
	if r := released / hover; math.Abs(r-float64(pressHover*pressHover)) > 0.02 {
		t.Errorf("松手 600 毫秒后覆盖量是不缩时的 %.3f 倍，应是悬停的 %.3f", r, pressHover*pressHover)
	}
}

// pressRig 是一颗按钮，按钮四周留白；width 读它这一帧的宽（读屏树的框带着缩放）。
type pressRig struct {
	tt *ui.Tester
}

func newPressRig(reduce, disabled bool) *pressRig {
	k := testKit()
	tt := motionTester(func(c *ui.Context) {
		ui.Box(c.Key("bg")).Fill().Background(k.P.Canvas).Padding(40).Children(func() {
			k.Button(c, "b", "保存修改", true, disabled, func() {})
		})
	}, 240, 160, reduce)
	tt.Frame()
	return &pressRig{tt}
}

func (r *pressRig) bounds() ui.Rect {
	a, _ := r.tt.AccessByRole(ui.RoleButton, "保存修改")
	return a.Bounds
}

func (r *pressRig) centre() (float32, float32) {
	b := r.bounds()
	return b.X + b.W/2, b.Y + b.H/2
}

// run 走 n 帧（每帧 16 毫秒），返回每帧的宽。
func (r *pressRig) run(n int) []float32 {
	out := make([]float32, 0, n)
	for i := 0; i < n; i++ {
		step(r.tt, 16*time.Millisecond)
		out = append(out, r.bounds().W)
	}
	return out
}

func minMax(v []float32) (lo, hi float32) {
	lo, hi = v[0], v[0]
	for _, x := range v {
		lo, hi = min(lo, x), max(hi, x)
	}
	return
}

// 按下回弹：按下中途缩小，松手后弹回（过程中有超过原大的一帧），指针移开后回到 1；悬停放大 1.025。
func TestPressSpring(t *testing.T) {
	r := newPressRig(false, false)
	w0 := r.bounds().W
	x, y := r.centre()
	r.tt.Move(x, y)
	hover := r.run(60)
	if got := hover[len(hover)-1]; math.Abs(float64(got/w0-pressHover)) > .003 {
		t.Errorf("鼠标悬停停稳后宽度是原来的 %.4f 倍，应是 %v", got/w0, pressHover)
	}
	r.tt.Press(x, y)
	down := r.run(60)
	lo, _ := minMax(down)
	if want := w0 * float32(testKit().T.D.Num.ScalePress); lo > want*1.01 {
		t.Errorf("按下后最窄 %.2f，应缩到约 %.2f", lo, want)
	}
	if got := down[len(down)-1]; math.Abs(float64(got/w0)-testKit().T.D.Num.ScalePress) > .004 {
		t.Errorf("按住停稳后宽度是原来的 %.4f 倍", got/w0)
	}
	r.tt.Release(x, y)
	r.tt.Move(x+200, y+120) // 指针移开：回到 1，不再悬停放大
	up := r.run(90)
	_, hi := minMax(up)
	if hi < w0+.1 {
		t.Errorf("松手回弹的过程中应有超过原大的一帧：最宽 %.3f，原宽 %.3f", hi, w0)
	}
	if got := up[len(up)-1]; math.Abs(float64(got-w0)) > .05 {
		t.Errorf("松手并移开后应回到原宽 %.3f，现在 %.3f", w0, got)
	}
	// 挤压：缩放通道在动时宽高不再是同一个比例（缩小时横向比纵向多留一点）
	r2 := newPressRig(false, false)
	b0 := r2.bounds()
	x, y = r2.centre()
	r2.tt.Press(x, y)
	step(r2.tt, 40*time.Millisecond)
	b := r2.bounds()
	if wr, hr := b.W/b0.W, b.H/b0.H; wr <= hr+.001 || wr >= 1 {
		t.Errorf("按下途中宽高比例应有差：宽 %.4f 倍、高 %.4f 倍", wr, hr)
	}
}

// 触屏不悬停放大；减少动态效果、禁用时按下也不缩。
func TestPressSpringSkips(t *testing.T) {
	r := newPressRig(false, false)
	w0 := r.bounds().W
	x, y := r.centre()
	r.tt.UsePointer(ui.PointerTouch, 1)
	r.tt.Press(x, y)
	r.run(40)
	r.tt.Release(x, y)
	got := r.run(80)
	if last := got[len(got)-1]; math.Abs(float64(last-w0)) > .05 {
		t.Errorf("触屏点一下之后不该停在悬停放大的大小：%.3f（原宽 %.3f）", last, w0)
	}
	for name, rig := range map[string]*pressRig{"减少动态效果": newPressRig(true, false), "禁用": newPressRig(false, true)} {
		w0 := rig.bounds().W
		x, y := rig.centre()
		rig.tt.Move(x, y)
		rig.tt.Press(x, y)
		lo, hi := minMax(rig.run(40))
		rig.tt.Release(x, y)
		lo2, hi2 := minMax(rig.run(40))
		if min(lo, lo2) < w0-.01 || max(hi, hi2) > w0+.01 {
			t.Errorf("%s：按钮不该缩放或挤压，宽 %.3f～%.3f（原宽 %.3f）", name, min(lo, lo2), max(hi, hi2), w0)
		}
	}
}

func TestPressStretch(t *testing.T) {
	for _, c := range []struct{ v, want float32 }{{0, 0}, {1, .03}, {-1, -.03}, {100, .05}, {-100, -.05}} {
		if got := pressStretch(c.v); math.Abs(float64(got-c.want)) > 1e-6 {
			t.Errorf("pressStretch(%v)=%v，应为 %v", c.v, got, c.want)
		}
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
