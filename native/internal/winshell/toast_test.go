package winshell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// toastRig 是提示条的试验台：背景上一列文字，提示条是浮层；show 在帧和帧之间触发。
type toastRig struct {
	tt *ui.Tester
	st *ToastState
	k  *Kit
}

func newToastRig(reduce bool, material Material) *toastRig {
	r := &toastRig{st: &ToastState{}, k: testKit()}
	r.k.SetMaterial(material)
	r.tt = motionTester(func(c *ui.Context) {
		k := r.k
		ui.Column(c.Key("bg")).Fill().Background(k.P.Canvas).Padding(k.G.SpaceMedium).Gap(k.G.SpaceSmall).Children(func() {
			ui.Text(c.Key("l1"), "第一行字").FontSize(k.G.LabelSize)
			ui.Text(c.Key("l2"), "第二行字").FontSize(k.G.LabelSize)
			k.Button(c, "btn", "保存", true, false, func() {})
		})
		k.Toast(c, "t", r.st)
	}, 480, 240, reduce)
	r.tt.Frame()
	return r
}

// bounds 是提示条胶囊现在的框（读屏树里的）；没有就 ok 为假。
func (r *toastRig) bounds(label string) (ui.Rect, bool) {
	a, ok := r.tt.AccessByRole(ui.RoleStatus, label)
	return a.Bounds, ok
}

func (r *toastRig) lines() [3]ui.Rect {
	var out [3]ui.Rect
	for i, s := range []string{"第一行字", "第二行字"} {
		out[i], _ = r.tt.Find(s)
	}
	out[2], _ = r.tt.Find("保存")
	return out
}

func TestToastGrowsAndLeavesLayoutAlone(t *testing.T) {
	for _, m := range []Material{MaterialLiquid, MaterialBlur, MaterialSolid} {
		r := newToastRig(false, m)
		before := r.lines()
		r.st.Show("已复制", chatui.Icon("check", false))
		var widths, ys []float32
		for i := 0; i < 90; i++ { // 每帧 16 毫秒，走一秒半
			step(r.tt, 16*time.Millisecond)
			if b, ok := r.bounds("已复制"); ok {
				widths, ys = append(widths, b.W), append(ys, b.Y)
			}
			if got := r.lines(); got != before {
				t.Fatalf("材质 %v 第 %d 帧：提示条挤动了下面的元素：%v → %v", m, i, before, got)
			}
		}
		if len(widths) == 0 {
			t.Fatalf("材质 %v：读屏树里找不到提示条", m)
		}
		first, last := widths[0], widths[len(widths)-1]
		if first >= last {
			t.Errorf("材质 %v：宽度没长大：%v → %v", m, first, last)
		}
		// 单调长大：到第一次碰到终点之前每一帧都不小于上一帧
		for i := 1; i < len(widths) && widths[i-1] < last-1; i++ {
			if widths[i]+.01 < widths[i-1] {
				t.Errorf("材质 %v：第 %d 帧宽度缩了：%v → %v", m, i, widths[i-1], widths[i])
				break
			}
		}
		t.Logf("材质 %v：宽 %.1f → %.1f，最高点 y=%.1f，落点 y=%.1f", m, first, last, ys[0], ys[len(ys)-1])
	}
}

func TestToastAutoCloses(t *testing.T) {
	r := newToastRig(false, MaterialSolid)
	r.st.Show("设置已保存", chatui.Icon("check", false))
	r.tt.Frame()
	if !r.st.Visible() {
		t.Fatal("Show 之后应该可见")
	}
	step(r.tt, time.Second)
	if !r.st.Visible() || r.st.phase != toastOpen {
		t.Fatalf("1 秒时还应在停留，phase=%d", r.st.phase)
	}
	tt := r.tt
	for i := 0; i < 400 && r.st.Visible(); i++ {
		step(tt, 16*time.Millisecond)
	}
	if r.st.Visible() {
		t.Fatalf("几秒后应自动收起，phase=%d", r.st.phase)
	}
	tt.Frame()
	if tt.FramePending() {
		t.Error("收起后不该再要帧")
	}
	if b, ok := r.bounds("设置已保存"); ok && b.Y+b.H > 0 {
		t.Errorf("藏着的提示条还有一截在窗口里：%v", b)
	}
	// 再弹一次：第二次照样从上面落下来（元素藏着时通道还在，不是一出现就在终点）
	r.st.Show("已允许运行命令", nil)
	var ys []float32
	for i := 0; i < 40; i++ {
		step(tt, 16*time.Millisecond)
		if b, ok := r.bounds("已允许运行命令"); ok {
			ys = append(ys, b.Y)
		}
	}
	if len(ys) == 0 || ys[0] > -20 {
		t.Errorf("第二次应从窗口外落下来，第一帧 y=%v", ys)
	}
	if last := ys[len(ys)-1]; last < 50 {
		t.Errorf("第二次没落到位：y=%v", last)
	}
}

func TestToastReplaceKeepsPlace(t *testing.T) {
	r := newToastRig(false, MaterialSolid)
	r.st.Show("已复制", nil)
	settleTo := func() {
		for i := 0; i < 60; i++ {
			step(r.tt, 16*time.Millisecond)
		}
	}
	settleTo()
	b0, ok := r.bounds("已复制")
	if !ok {
		t.Fatal("没找到提示条")
	}
	r.st.Show("设置已保存，下次启动时生效", nil)
	for i := 0; i < 60; i++ {
		step(r.tt, 16*time.Millisecond)
		if b, ok := r.bounds("设置已保存，下次启动时生效"); ok && b.Y < b0.Y-1 {
			t.Fatalf("替换时胶囊又落了一次：y %.1f → %.1f", b0.Y, b.Y)
		}
	}
	b1, _ := r.bounds("设置已保存，下次启动时生效")
	if b1.W <= b0.W {
		t.Errorf("换成更长的字，宽度应变大：%.1f → %.1f", b0.W, b1.W)
	}
}

func TestToastReduceMotion(t *testing.T) {
	r := newToastRig(true, MaterialSolid)
	before := r.lines()
	r.st.Show("已复制", chatui.Icon("check", false))
	var bs []ui.Rect
	for i := 0; i < 20; i++ {
		step(r.tt, 16*time.Millisecond)
		if b, ok := r.bounds("已复制"); ok {
			bs = append(bs, b)
		}
		if got := r.lines(); got != before {
			t.Fatalf("减少动态效果：提示条挤动了下面的元素")
		}
	}
	if len(bs) < 2 {
		t.Fatal("没找到提示条")
	}
	for _, b := range bs[1:] {
		if b != bs[0] {
			t.Fatalf("减少动态效果时不该位移、变宽高：%v → %v", bs[0], b)
		}
	}
	for i := 0; i < 400 && r.st.Visible(); i++ {
		step(r.tt, 16*time.Millisecond)
	}
	if r.st.Visible() {
		t.Error("减少动态效果时也要自动收起")
	}
	r.tt.Frame()
	if r.tt.FramePending() {
		t.Error("收起后不该再要帧")
	}
}
