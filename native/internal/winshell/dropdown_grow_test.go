package winshell

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// ddRig 搭一个下拉：宽 300，top 是它上面留的空白（让它贴着窗口下沿，试朝上长），items 是选项。
type ddRig struct {
	k      *Kit
	tt     *ui.Tester
	infos  map[string]SurfaceInfo
	rects  map[string]ui.Rect
	open   bool
	chosen int
}

func newDDRig(t *testing.T, reduce bool, top float32, items []string, h int) *ddRig {
	t.Helper()
	r := &ddRig{k: testKit(), chosen: -1}
	r.infos, r.rects = collectSurfaces(r.k)
	r.tt = motionTester(func(c *ui.Context) {
		c.SetTheme(&r.k.Theme)
		ui.Column(c.Key("root")).Width(300).Padding(20).Children(func() {
			ui.Box(c.Key("space")).Height(top).Shrink(0)
			r.k.Dropdown(c, "dd", "模型", &r.open, 1, items, func(i int) { r.chosen = i }, nil)
		})
	}, 400, h, reduce)
	r.tt.Frame()
	r.tt.Frame()
	return r
}

// shot 把当前一帧存进 DROPDOWN_SHOTS 指的目录（没设就不存）。
func (r *ddRig) shot(t *testing.T, name string) {
	t.Helper()
	dir := os.Getenv("DROPDOWN_SHOTS")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, r.tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// panel 是面板（长大中的入口）现在的位置；多画一帧，登记的是上一帧排出来的位置。
func (r *ddRig) panel() ui.Rect {
	r.tt.Frame()
	return r.rects["dd-popup"]
}

func ddFull(k *Kit, rows int) float32 { return k.DropdownHeight(rows) }

func edgesMatch(pn, tr ui.Rect) bool {
	return abs32(pn.X-tr.X) <= .5 && abs32(pn.Y-tr.Y) <= .5 && abs32(pn.W-tr.W) <= .5
}

// 展开是入口自己在长：每一帧面板的上、左、右三条边都和入口重合，高度只增不减、不过冲，最后长到放得下全部选项；
// 收起时反着缩回入口的大小，放完以后画面和没展开前一样。
func TestDropdownGrowsAndShrinksInPlace(t *testing.T) {
	r := newDDRig(t, false, 0, []string{"甲", "乙", "丙"}, 400)
	g := r.k.G
	before := r.tt.Image()
	beforePix := append([]uint8(nil), before.Pix...)
	tr := r.rects["dd"]
	if tr.H != g.RowHeight {
		t.Fatalf("入口高 %v，应为 %v", tr.H, g.RowHeight)
	}
	full := ddFull(r.k, 3)
	if err := r.tt.Click("模型"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	var heights []float32
	for i := 0; i < 14; i++ {
		pn := r.panel()
		heights = append(heights, pn.H)
		if !edgesMatch(pn, tr) {
			t.Fatalf("第 %d 帧面板 %+v 的上、左、右边没和入口 %+v 重合", i, pn, tr)
		}
		if i == 4 {
			r.shot(t, "长到一半")
		}
		step(r.tt, 16*time.Millisecond)
	}
	for i, h := range heights {
		if h > full+.01 {
			t.Errorf("第 %d 帧高 %.2f 超过终点 %.2f（高度不许过冲）", i, h, full)
		}
		if i > 0 && h < heights[i-1]-.01 {
			t.Errorf("展开时高度回头了：%v", heights)
			break
		}
	}
	if heights[0] < g.RowHeight-.01 || heights[0] > g.RowHeight+full/4 {
		t.Errorf("起点 %.2f 应从入口的高 %.2f 附近出发", heights[0], g.RowHeight)
	}
	// 终点在动画放完以后量：高度是“利落”弹簧，收尾的尾巴比原来的 180 毫秒缓出长，不按帧数等（AGENTS：固定等待改成等事件）
	settle(r.tt)
	if got := r.panel().H; abs32(got-full) > .01 {
		t.Errorf("长到 %.2f，应为 %.2f", got, full)
	}
	if mid := heights[4]; mid <= g.RowHeight+1 || mid >= full-1 {
		t.Errorf("走到一半高度 %.2f 应在 %.2f 和 %.2f 之间", mid, g.RowHeight, full)
	}
	r.shot(t, "展开")
	if r.infos["dd-popup"].Radius != r.infos["dd"].Radius || r.infos["dd"].Radius != g.Pill(g.RowHeight) {
		t.Errorf("圆角入口 %v 面板 %v，应都是 %v", r.infos["dd"].Radius, r.infos["dd-popup"].Radius, g.Pill(g.RowHeight))
	}

	// 收起：Esc
	r.tt.Key(0, ui.KeyEscape)
	r.tt.Frame()
	if r.open {
		t.Fatal("Esc 没收起")
	}
	var shrink []float32
	for i := 0; i < 12; i++ {
		pn := r.panel()
		shrink = append(shrink, pn.H)
		if !edgesMatch(pn, tr) {
			t.Fatalf("收起第 %d 帧面板 %+v 的上、左、右边没和入口 %+v 重合", i, pn, tr)
		}
		if i == 3 {
			r.shot(t, "收到一半")
		}
		step(r.tt, 16*time.Millisecond)
	}
	for i := 1; i < len(shrink); i++ {
		if shrink[i] > shrink[i-1]+.01 {
			t.Errorf("收起时高度回头了：%v", shrink)
			break
		}
	}
	if shrink[0] < full-g.HitSmall {
		t.Errorf("收起第一帧高 %.2f，应还在接近 %.2f 的地方", shrink[0], full)
	}
	settle(r.tt)
	settle(r.tt)
	if !r.tt.Focused("模型") {
		t.Error("Esc 以后焦点没回到入口")
	}
	r.shot(t, "收起后")
	// 回到入口：画面里入口那一块和展开前一样（焦点环除外，只比离边 6 像素以里）
	after := r.tt.Image()
	b := after.Bounds()
	diff, dx0, dx1, dy0, dy1 := 0, 9999, -1, 9999, -1
	for y := int(tr.Y) + 6; y < int(tr.Y+tr.H)-6; y++ {
		for x := int(tr.X) + 6; x < int(tr.X+tr.W)-6; x++ {
			i := (y-b.Min.Y)*after.Stride + (x-b.Min.X)*4
			for j := 0; j < 4; j++ {
				if after.Pix[i+j] != beforePix[i+j] {
					diff++
					dx0, dx1, dy0, dy1 = min(dx0, x), max(dx1, x), min(dy0, y), max(dy1, y)
					break
				}
			}
		}
	}
	if diff != 0 {
		t.Errorf("收起以后入口里有 %d 个像素和展开前不一样，范围 x %d-%d y %d-%d", diff, dx0, dx1, dy0, dy1)
	}
	if r.tt.FramePending() {
		t.Error("动画放完还在要帧")
	}
}

// 键盘：上下移动、回车选中；选中的是高亮的那一项，收起并把焦点还给入口。
func TestDropdownKeyboard(t *testing.T) {
	r := newDDRig(t, true, 0, []string{"甲", "乙", "丙"}, 400)
	if err := r.tt.Click("模型"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	r.tt.Frame()
	for i := 0; i < 3; i++ {
		r.tt.Key(0, ui.KeyDown)
		r.tt.Frame()
	}
	r.tt.Key(0, ui.KeyEnter)
	r.tt.Frame()
	r.tt.Frame()
	if r.chosen != 2 {
		t.Errorf("选到 %d，应为 2", r.chosen)
	}
	if r.open {
		t.Error("回车选完该收起")
	}
	if !r.tt.Focused("模型") {
		t.Error("选完焦点没回到入口")
	}
}

// 选项很多：面板最高放 dropdownRows 行，再多在里面滚动。
func TestDropdownLongListScrolls(t *testing.T) {
	var items []string
	for i := 0; i < 15; i++ {
		items = append(items, fmt.Sprintf("选项%d", i+1))
	}
	r := newDDRig(t, true, 0, items, 700)
	if err := r.tt.Click("模型"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	pn := r.panel()
	if want := ddFull(r.k, dropdownRows); abs32(pn.H-want) > .01 {
		t.Errorf("面板高 %.2f，应封顶在 %.2f", pn.H, want)
	}
	r.shot(t, "长列表")
}

// 下面放不下就往上长：面板的下、左、右三条边和入口重合，入口在面板最下面一行。
func TestDropdownGrowsUpWhenNoRoomBelow(t *testing.T) {
	r := newDDRig(t, true, 300, []string{"甲", "乙", "丙"}, 400)
	tr := r.rects["dd"]
	if err := r.tt.Click("模型"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	pn := r.panel()
	if abs32(pn.X-tr.X) > .5 || abs32(pn.W-tr.W) > .5 || abs32((pn.Y+pn.H)-(tr.Y+tr.H)) > .5 {
		t.Errorf("朝上长：面板 %+v 的下、左、右边应和入口 %+v 重合", pn, tr)
	}
	if pn.H <= tr.H+r.k.G.HitSmall {
		t.Errorf("面板高 %.2f 没长开", pn.H)
	}
	r.shot(t, "朝上")
}

// 减少动态效果：点开直接是展开的，Esc 直接收回，不留拷贝。
func TestDropdownReduceMotionSnaps(t *testing.T) {
	r := newDDRig(t, true, 0, []string{"甲", "乙", "丙"}, 400)
	if err := r.tt.Click("模型"); err != nil {
		t.Fatal(err)
	}
	r.tt.Frame()
	if pn, full := r.panel(), ddFull(r.k, 3); abs32(pn.H-full) > .01 {
		t.Errorf("减少动态：点开后高 %.2f，应直接是 %.2f", pn.H, full)
	}
	r.tt.Key(0, ui.KeyEscape)
	settle(r.tt)
	if r.tt.FramePending() {
		t.Error("减少动态：收起后不该还有动画帧")
	}
	if _, still := r.tt.Find("丙"); still {
		t.Error("减少动态：收起后选项还在")
	}
}
