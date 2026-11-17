package main

// 看大图（viewer.go）的测试。窗口按主人的屏幕：逻辑 2048×1030、像素比 1.25。
// 图是编的纯色位图，位置和大小从 Tester.Find 读（图按真实缩放后的 DIP 大小布局，Find 读到的就是看到的）。
// 期望值在测试里用公式独立算（可用区域 = 窗口 − 四周各 24 DIP），不调被测的 fit。

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/button"
	"github.com/egoist/mygo/ui"
)

const (
	vw, vh   = 2048, 1030
	vAvailW  = vw - 48 // 适应窗口可用的宽、高
	vAvailH  = vh - 48
	vLabel   = "测试"
	vWheelDY = -99 // Windows 滚轮向前一格
)

type viewHarness struct {
	imgs   []viewImage
	at     int
	open   bool
	closed int    // 看大图自己关了几次（lb.Changed）
	field  string // 页面上输入框里的字，查键盘守门用
	thumb  bool   // 页面左上角有个缩略图按钮，点它打开看大图（查"双击缩略图"）
	st     viewState
}

func solidBitmap(w, h int) *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+3] = 200, 255
	}
	return ui.NewBitmap(img)
}

func vimgs(sizes ...[2]int) []viewImage {
	var out []viewImage
	for _, s := range sizes {
		out = append(out, viewImage{bmp: solidBitmap(s[0], s[1]), w: s[0], h: s[1]})
	}
	return out
}

func viewTester(h *viewHarness) *ui.Tester {
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Background(c.Theme().Background).Children(func() {
			ui.TextInput(c, &h.field).Label("输入框")
			if h.thumb && button.Outline(c, "缩略图").Clicked() {
				h.at, h.open = 0, true
			}
			lb := lightbox(c, vLabel, h.imgs, &h.at, &h.open, &h.st)
			if lb.Changed() {
				h.closed++
			}
		})
	}, vw, vh)
	tt.SetScale(1.25)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	return tt
}

func (h *viewHarness) show(tt *ui.Tester, i int) {
	h.at, h.open = i, true
	tt.Frame()
	tt.Frame()
	tt.Advance(600 * time.Millisecond) // 过了刚打开的保护期（viewOpenGuard），之后点遮罩才算
}

// rect 图现在的位置和大小，从缩放状态算（Tester.Find 读到的是被窗口裁过的，图超出窗口时不是真大小）；
// 图整个在窗口里时，再和画出来的（Find）对一次，两边必须一致
func (h *viewHarness) rect(t *testing.T, tt *ui.Tester) ui.Rect {
	t.Helper()
	iw, ih := h.imgs[h.at].natural()
	x, y, w, hh := h.st.rect(viewGeom{sw: vw, sh: vh, iw: iw, ih: ih, inset: 24})
	r := ui.Rect{X: x, Y: y, W: w, H: hh}
	if x >= 0 && y >= 0 && x+w <= vw && y+hh <= vh {
		got, ok := tt.Find(imageLabel(vLabel, h.at, len(h.imgs)))
		if !ok || !approx(got.X, x, 1) || !approx(got.Y, y, 1) || !approx(got.W, w, 1) || !approx(got.H, hh, 1) {
			t.Fatalf("画出来的图 %v（找到=%v）和状态算的 %v 对不上", got, ok, r)
		}
	}
	return r
}

func approx(a, b, tol float32) bool { return float32(math.Abs(float64(a-b))) <= tol }

// 四种图各打开一次：适应窗口的尺寸、居中、比例都对；比例数字写进日志
func TestViewerFitToWindow(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"普通 4:3", 1200, 900},
		{"竖长 1:5", 300, 1500},
		{"横长 5:1", 1500, 300},
		{"小图 64×64", 64, 64},
		{"比窗口大的 3000×2000", 1536, 1024},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			h := &viewHarness{imgs: vimgs([2]int{cs.w, cs.h})}
			tt := viewTester(h)
			h.show(tt, 0)
			r, ok := tt.Find(imageLabel(vLabel, 0, 1))
			if !ok {
				t.Fatalf("没找到大图：%q", tt.Texts())
			}
			s := min(float32(vAvailW)/float32(cs.w), float32(vAvailH)/float32(cs.h), viewMaxUp)
			wantW, wantH := float32(cs.w)*s, float32(cs.h)*s
			if !approx(r.W, wantW, 1) || !approx(r.H, wantH, 1) {
				t.Fatalf("适应窗口后 %.1f×%.1f，要 %.1f×%.1f", r.W, r.H, wantW, wantH)
			}
			if cx, cy := r.X+r.W/2, r.Y+r.H/2; !approx(cx, vw/2, 1) || !approx(cy, vh/2, 1) {
				t.Fatalf("图中心 (%.1f, %.1f)，要在窗口中心 (%d, %d)", cx, cy, vw/2, vh/2)
			}
			if r.X < 24-0.5 || r.Y < 24-0.5 || r.X+r.W > vw-24+0.5 || r.Y+r.H > vh-24+0.5 {
				t.Fatalf("图 %v 越过了四周 24 DIP 的留白", r)
			}
			t.Logf("%s：原图 %d×%d，显示 %.1f×%.1f DIP，适应比例 %.3f，占窗口宽 %.0f%%、高 %.0f%%",
				cs.name, cs.w, cs.h, r.W, r.H, r.W/float32(cs.w), r.W/vw*100, r.H/vh*100)
		})
	}
}

// 小图不放大，按原尺寸显示（2026-11-02 主人："大小是不是太大了"，原来放大 2 倍）
func TestViewerSmallImageCap(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{64, 64})}
	tt := viewTester(h)
	h.show(tt, 0)
	if r, _ := tt.Find(imageLabel(vLabel, 0, 1)); !approx(r.W, 64, 1) {
		t.Fatalf("64 像素的图显示 %.1f DIP，要 64（原尺寸）", r.W)
	}
}

// 窗口变小，适应窗口跟着走
func TestViewerFitFollowsWindow(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{1200, 900})}
	tt := viewTester(h)
	h.show(tt, 0)
	tt.SetSize(800, 500)
	tt.Frame()
	tt.Frame()
	r, _ := tt.Find(imageLabel(vLabel, 0, 1))
	s := min(float32(800-48)/1200, float32(500-48)/900, 2)
	if !approx(r.W, 1200*s, 1) || !approx(r.H, 900*s, 1) {
		t.Fatalf("窗口缩到 800×500 后图 %.1f×%.1f，要 %.1f×%.1f", r.W, r.H, 1200*s, 900*s)
	}
}

// 关闭：点遮罩、Esc、右上角按钮都关；点图本身、点翻页按钮不关
func TestViewerClose(t *testing.T) {
	open := func(n int) (*viewHarness, *ui.Tester) {
		sizes := make([][2]int, n)
		for i := range sizes {
			sizes[i] = [2]int{1200, 900}
		}
		h := &viewHarness{imgs: vimgs(sizes...)}
		tt := viewTester(h)
		h.show(tt, 0)
		return h, tt
	}
	t.Run("点遮罩", func(t *testing.T) {
		h, tt := open(1)
		for _, p := range [][2]float32{{20, 500}, {1024, 10}, {2040, 1020}, {200, 900}} { // 左边、上边、右下角、图左下的空地
			h.open, h.closed = true, 0
			tt.Frame()
			tt.Frame()
			tt.Advance(600 * time.Millisecond)
			tt.ClickAt(p[0], p[1])
			tt.Frame()
			if h.open || h.closed != 1 {
				t.Fatalf("点 %v 没关：open=%v closed=%d", p, h.open, h.closed)
			}
		}
	})
	t.Run("点图本身不关", func(t *testing.T) {
		h, tt := open(1)
		r, _ := tt.Find(imageLabel(vLabel, 0, 1))
		for _, p := range [][2]float32{{r.X + r.W/2, r.Y + r.H/2}, {r.X + 2, r.Y + 2}, {r.X + r.W - 2, r.Y + r.H - 2}} {
			tt.ClickAt(p[0], p[1])
			tt.Frame()
			if !h.open || h.closed != 0 {
				t.Fatalf("点图上 %v 关了：open=%v closed=%d", p, h.open, h.closed)
			}
		}
	})
	t.Run("Esc", func(t *testing.T) {
		h, tt := open(1)
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if h.open || h.closed != 1 {
			t.Fatalf("Esc 没关：open=%v closed=%d", h.open, h.closed)
		}
	})
	t.Run("关闭按钮", func(t *testing.T) {
		h, tt := open(1)
		if err := tt.Click("关闭"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if h.open || h.closed != 1 {
			t.Fatalf("按钮没关：open=%v closed=%d", h.open, h.closed)
		}
	})
	t.Run("点翻页按钮只翻页不关", func(t *testing.T) {
		h, tt := open(3)
		if err := tt.Click("下一张"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
		if !h.open || h.closed != 0 || h.at != 1 {
			t.Fatalf("点下一张：open=%v closed=%d at=%d，要开着、第 1 张", h.open, h.closed, h.at)
		}
		tt.Key(0, ui.KeyRight)
		tt.Frame()
		tt.Key(0, ui.KeyLeft)
		tt.Frame()
		if h.at != 1 || !h.open {
			t.Fatalf("→ ← 以后 at=%d open=%v，要还在第 1 张", h.at, h.open)
		}
		if err := tt.Click("上一张"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if h.at != 0 || !h.open {
			t.Fatalf("点上一张：at=%d open=%v", h.at, h.open)
		}
	})
	t.Run("翻到头的按钮点了不关", func(t *testing.T) {
		h, tt := open(3)
		if err := tt.Click("上一张"); err != nil { // 在第 0 张，上一张是灰的
			t.Fatal(err)
		}
		tt.Frame()
		if !h.open || h.closed != 0 || h.at != 0 {
			t.Fatalf("点灰着的上一张：open=%v closed=%d at=%d，要原样", h.open, h.closed, h.at)
		}
	})
}

// 在图上取一个点（图内的比例），缩放以后这个点还在鼠标下面
func pointAt(r ui.Rect, x, y float32) (float32, float32) { return (x - r.X) / r.W, (y - r.Y) / r.H }

func TestViewerWheelZoomKeepsPointUnderCursor(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{1200, 900})}
	tt := viewTester(h)
	h.show(tt, 0)
	fit := h.rect(t, tt)
	cx, cy := float32(1250), float32(640) // 偏离中心的一点，在图上
	for i := 0; i < 5; i++ {
		tt.Wheel(cx, cy, 0, vWheelDY)
		tt.Frame()
	}
	r0 := h.rect(t, tt)
	if r0.W < vw || r0.H < vh {
		t.Fatalf("放大到 %.0f×%.0f，还没盖满窗口，测不了锚点", r0.W, r0.H)
	}
	u0, v0 := pointAt(r0, cx, cy)
	for i := 0; i < 3; i++ {
		tt.Wheel(cx, cy, 0, vWheelDY)
		tt.Frame()
		r1 := h.rect(t, tt)
		u1, v1 := pointAt(r1, cx, cy)
		dx, dy := (u1-u0)*r1.W, (v1-v0)*r1.H
		if r1.W <= r0.W*1.1 || math.Abs(float64(dx)) > 3 || math.Abs(float64(dy)) > 3 {
			t.Fatalf("第 %d 格：宽 %.0f → %.0f，鼠标下的图点偏了 (%.2f, %.2f) DIP", i+1, r0.W, r1.W, dx, dy)
		}
		t.Logf("第 %d 格：宽 %.0f → %.0f（×%.3f），图点偏移 (%.2f, %.2f) DIP", i+1, r0.W, r1.W, r1.W/r0.W, dx, dy)
		r0 = r1
	}
	// 放大了 ≥ 适应窗口，再往回滚回来：到适应窗口为止不会缩得比一半还小
	for i := 0; i < 30; i++ {
		tt.Wheel(cx, cy, 0, -vWheelDY)
		tt.Frame()
	}
	rmin := h.rect(t, tt)
	if !approx(rmin.W, fit.W*0.5, 1.5) {
		t.Fatalf("一直缩小停在宽 %.1f，要是适应窗口 %.1f 的一半", rmin.W, fit.W)
	}
	for i := 0; i < 60; i++ {
		tt.Wheel(cx, cy, 0, vWheelDY)
		tt.Frame()
	}
	rmax := h.rect(t, tt)
	if !approx(rmax.W, 1200*8, 2) {
		t.Fatalf("一直放大停在宽 %.1f，要是原尺寸的 8 倍 %d", rmax.W, 1200*8)
	}
}

// 缩小以后回到适应窗口那一格，吸附成"适应窗口"（居中、跟着窗口走）
func TestViewerZoomSnapsBackToFit(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{1200, 900})}
	tt := viewTester(h)
	h.show(tt, 0)
	fit := h.rect(t, tt)
	tt.Wheel(1500, 700, 0, vWheelDY)
	tt.Frame()
	if r := h.rect(t, tt); r.W < fit.W*1.1 {
		t.Fatalf("放大一格没放大：%.1f → %.1f", fit.W, r.W)
	}
	tt.Wheel(1500, 700, 0, -vWheelDY)
	tt.Frame()
	r := h.rect(t, tt)
	if !approx(r.W, fit.W, 1) || !approx(r.X, fit.X, 1) || !approx(r.Y, fit.Y, 1) {
		t.Fatalf("放大一格再缩小一格应回到原位：%v → %v", fit, r)
	}
}

// 双击在适应窗口和原尺寸（一个像素一个 DIP）之间切，以点的位置为中心；再双击回来
func TestViewerDoubleClickToggle(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{2400, 1600})}
	tt := viewTester(h)
	h.show(tt, 0)
	fit := h.rect(t, tt)
	px, py := float32(1300), float32(560)
	u, v := pointAt(fit, px, py)
	tt.ClickAt(px, py)
	tt.ClickAt(px, py) // 同一点连点两下
	tt.Frame()
	r := h.rect(t, tt)
	if !h.open || !approx(r.W, 2400, 1) || !approx(r.H, 1600, 1) {
		t.Fatalf("双击后 open=%v 尺寸 %.1f×%.1f，要原尺寸 2400×1600", h.open, r.W, r.H)
	}
	u1, v1 := pointAt(r, px, py)
	if math.Abs(float64(u1-u))*2400 > 3 || math.Abs(float64(v1-v))*1600 > 3 {
		t.Fatalf("双击放大后鼠标下的图点偏了：(%.4f, %.4f) → (%.4f, %.4f)", u, v, u1, v1)
	}
	tt.ClickAt(px, py)
	tt.ClickAt(px, py)
	tt.Frame()
	back := h.rect(t, tt)
	if !approx(back.W, fit.W, 1) || !approx(back.X, fit.X, 1) || !approx(back.Y, fit.Y, 1) {
		t.Fatalf("再双击应回到适应窗口：%v → %v", fit, back)
	}
}

// 习惯性双击聊天里的缩略图：第一下松开才打开看大图，第二下按下落进看大图（引擎换了元素就从 1 重数连击，认不出这一串）。
// 落在遮罩上松开不关；过了保护期以后看大图里自己的双击和点遮罩照旧
func TestViewerDoubleClickFromThumbnail(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{1200, 900}), thumb: true}
	tt := viewTester(h)
	th, ok := tt.Find("缩略图")
	if !ok {
		t.Fatalf("没找到缩略图按钮：%q", tt.Texts())
	}
	px, py := th.X+th.W/2, th.Y+th.H/2
	tt.ClickAt(px, py) // 第一下：点缩略图，松开后打开
	tt.Frame()
	tt.Frame()
	if !h.open {
		t.Fatal("点缩略图没打开看大图")
	}
	fit := h.rect(t, tt)
	if px >= fit.X {
		t.Fatalf("缩略图 (%.0f, %.0f) 落在图上了（图 %v），测不了落在遮罩上", px, py, fit)
	}
	tt.ClickAt(px, py) // 第二下：同一点，现在落在遮罩上
	tt.Frame()
	if !h.open || h.closed != 0 {
		t.Fatalf("双击缩略图的第二下关掉了看大图：open=%v closed=%d", h.open, h.closed)
	}
	ix, iy := fit.X+fit.W/2, fit.Y+fit.H/2
	// 以后在看大图里自己双击（图上）照旧放大：隔开一段时间再连点两下
	tt.Advance(2 * time.Second)
	tt.ClickAt(ix, iy)
	tt.ClickAt(ix, iy)
	tt.Frame()
	// 1200×900 的图比可用区域小，适应窗口就是原尺寸，双击放大一倍（toggle 的规矩）
	if r := h.rect(t, tt); !approx(r.W, 2400, 1) {
		t.Fatalf("看大图里自己的双击没放大一倍：%v", r)
	}
	// 再双击缩回适应窗口（放大一倍时图铺满了窗口，没有遮罩可点），之后点遮罩照旧关闭
	tt.Advance(2 * time.Second)
	tt.ClickAt(ix, iy)
	tt.ClickAt(ix, iy)
	tt.Frame()
	tt.Advance(2 * time.Second)
	tt.ClickAt(20, 500)
	tt.Frame()
	if h.open {
		t.Fatal("连击串结束以后点遮罩没关")
	}
}

// 同一件事直接喂输入：刚打开的保护期里按下不算点遮罩（含连击数为 1 的），过了保护期照常；还没建出来（没有 openedAt）也在保护期里
func TestViewerOpenGuard(t *testing.T) {
	t.Parallel()
	g := viewGeom{sw: vw, sh: vh, iw: 2400, ih: 1600, inset: 24}
	t0 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	click := func(v *viewState, at time.Time) {
		v.input(ui.InputEvent{Kind: ui.InputPointerDown, X: 10, Y: 10, Clicks: 1, Time: at}, g)
		v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: 10, Y: 10, Clicks: 1, Time: at}, g)
	}
	cases := []struct {
		name   string
		opened time.Time
		at     time.Time
		close  bool
	}{
		{"刚打开 100 毫秒", t0, t0.Add(100 * time.Millisecond), false},
		{"保护期末尾前一毫秒", t0, t0.Add(viewOpenGuard - time.Millisecond), false},
		{"过了保护期", t0, t0.Add(viewOpenGuard + time.Millisecond), true},
		{"还没建出来", time.Time{}, t0.Add(time.Hour), false},
	}
	for _, cs := range cases {
		v := viewState{openedAt: cs.opened}
		click(&v, cs.at)
		if v.closeReq != cs.close {
			t.Errorf("%s：点遮罩 closeReq=%v，要 %v", cs.name, v.closeReq, cs.close)
		}
	}
}

// 放大以后按住拖动平移；拖过头停在边上；没放大时拖不动；拖动结束不算点遮罩
func TestViewerDragPan(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{2400, 1600})}
	tt := viewTester(h)
	h.show(tt, 0)
	fit := h.rect(t, tt)
	// 适应窗口时从空地拖一下：不动、不关
	tt.Press(100, 500)
	tt.Move(160, 520)
	tt.Release(160, 520)
	tt.Frame()
	if r := h.rect(t, tt); !h.open || r != fit {
		t.Fatalf("适应窗口时拖动：open=%v 图 %v → %v", h.open, fit, r)
	}
	// 放大到原尺寸（2400×1600，比窗口大）
	tt.ClickAt(1024, 515)
	tt.ClickAt(1024, 515)
	tt.Frame()
	r0 := h.rect(t, tt)
	tt.Press(1000, 500)
	tt.Move(1040, 530)
	tt.Move(1100, 560)
	tt.Release(1100, 560)
	tt.Frame()
	r1 := h.rect(t, tt)
	if !h.open || !approx(r1.X-r0.X, 100, 1) || !approx(r1.Y-r0.Y, 60, 1) {
		t.Fatalf("拖 (100, 60) 以后图动了 (%.1f, %.1f)，open=%v", r1.X-r0.X, r1.Y-r0.Y, h.open)
	}
	// 拖过头：左边缘不进窗口
	tt.Press(100, 500)
	tt.Move(2000, 900)
	tt.Release(2000, 900)
	tt.Frame()
	r2 := h.rect(t, tt)
	if r2.X > 0.5 || r2.Y > 0.5 || r2.X+r2.W < vw-0.5 && r2.W > vw {
		t.Fatalf("拖过头：图 %v 把空地拉进了窗口", r2)
	}
	if !approx(r2.X, 0, 1) || !approx(r2.Y, 0, 1) {
		t.Fatalf("向右下拖到头图应贴左上角，现在在 (%.1f, %.1f)", r2.X, r2.Y)
	}
}

// 键盘：+ − 以窗口中心缩放，0 回适应窗口；开着的时候不进页面上的输入框，关着的时候照常打字
func TestViewerKeys(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{1200, 900})}
	tt := viewTester(h)
	h.show(tt, 0)
	fit := h.rect(t, tt)
	tt.Key(0, ui.KeyEqual)
	tt.Frame()
	r := h.rect(t, tt)
	if !approx(r.W, fit.W*1.25, 1) || !approx(r.X+r.W/2, vw/2, 1) || !approx(r.Y+r.H/2, vh/2, 1) {
		t.Fatalf("按 +：%v → %v，要宽 ×1.25、中心不动", fit, r)
	}
	tt.Key(ui.Shift, ui.KeyEqual)
	tt.Frame()
	if r2 := h.rect(t, tt); !approx(r2.W, r.W*1.25, 1) {
		t.Fatalf("按 Shift+=（就是 +）：宽 %.1f → %.1f", r.W, r2.W)
	}
	tt.Key(0, ui.KeyMinus)
	tt.Frame()
	if r3 := h.rect(t, tt); !approx(r3.W, r.W, 1) {
		t.Fatalf("按 −：宽 %.1f，要回到 %.1f", r3.W, r.W)
	}
	tt.Key(0, ui.KeyEqual)
	tt.Frame()
	tt.Key(0, ui.Key0)
	tt.Frame()
	if r4 := h.rect(t, tt); r4 != fit {
		t.Fatalf("按 0 应回适应窗口：%v → %v", fit, r4)
	}
	if h.field != "" {
		t.Fatalf("看大图开着，按键进了输入框：%q", h.field)
	}
}

func TestViewerKeysNotWhenClosed(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{1200, 900})}
	tt := viewTester(h)
	if err := tt.Click("输入框"); err != nil {
		t.Fatal(err)
	}
	tt.Type("+-0")
	tt.Frame()
	if h.field != "+-0" {
		t.Fatalf("看大图关着，输入框里应有 +-0，现在是 %q", h.field)
	}
	if _, ok := tt.Find(imageLabel(vLabel, 0, 1)); ok {
		t.Fatal("看大图关着却画出了图")
	}
}

// 连着翻页：翻到另一张缩放状态重置成适应窗口
func TestViewerTurnResetsZoom(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{1200, 900}, [2]int{900, 1200})}
	tt := viewTester(h)
	h.show(tt, 0)
	tt.Wheel(1024, 515, 0, vWheelDY)
	tt.Frame()
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	tt.Frame()
	r := h.rect(t, tt)
	s := min(float32(vAvailW)/900, float32(vAvailH)/1200, 2)
	if h.at != 1 || !approx(r.W, 900*s, 1) || !approx(r.H, 1200*s, 1) {
		t.Fatalf("翻到第 2 张：at=%d 图 %.1f×%.1f，要适应窗口 %.1f×%.1f", h.at, r.W, r.H, 900*s, 1200*s)
	}
}

// 纯函数：以点为中心缩放，点下面的图点不动
func TestAnchorZoomInvariant(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ cx, cy, s0, s1, px, py float32 }{
		{0, 0, 1, 2, 100, 50}, {30, -20, 0.5, 4, -300, 200}, {-80, 60, 3, 1.5, 0, 0}, {10, 10, 1, 8, 1000, 500},
	} {
		cx1, cy1 := anchorZoom(c.cx, c.cy, c.s0, c.s1, c.px, c.py)
		// 图点坐标 = (点 − 中心偏移) / 比例
		u0, u1 := (c.px-c.cx)/c.s0, (c.px-cx1)/c.s1
		v0, v1 := (c.py-c.cy)/c.s0, (c.py-cy1)/c.s1
		if !approx(u0, u1, 1e-3) || !approx(v0, v1, 1e-3) {
			t.Errorf("%+v：图点 (%.3f, %.3f) → (%.3f, %.3f)", c, u0, v0, u1, v1)
		}
	}
}

func TestClampPan(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ pan, img, stage, want float32 }{
		{50, 100, 200, 0},    // 图比舞台小：居中
		{50, 200, 200, 0},    // 一样大
		{999, 400, 200, 100}, // 大的：边贴住
		{-999, 400, 200, -100},
		{30, 400, 200, 30},
	} {
		if got := clampPan(c.pan, c.img, c.stage); got != c.want {
			t.Errorf("clampPan(%v, %v, %v) = %v，要 %v", c.pan, c.img, c.stage, got, c.want)
		}
	}
}

// natural：逻辑尺寸先于位图；位图比例对不上就信位图
func TestViewImageNatural(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		v            viewImage
		wantW, wantH float32
	}{
		{"没有逻辑尺寸", viewImage{bmp: solidBitmap(400, 300)}, 400, 300},
		{"位图是显示档，逻辑尺寸是 1536 档", viewImage{bmp: solidBitmap(768, 512), w: 1536, h: 1024}, 1536, 1024},
		{"位图还是 1×1 占位", viewImage{bmp: solidBitmap(1, 1), w: 1536, h: 1024}, 1536, 1024},
		{"逻辑尺寸和位图比例对不上", viewImage{bmp: solidBitmap(400, 300), w: 300, h: 400}, 400, 300},
	}
	for _, c := range cases {
		if w, h := c.v.natural(); w != c.wantW || h != c.wantH {
			t.Errorf("%s：%v×%v，要 %v×%v", c.name, w, h, c.wantW, c.wantH)
		}
	}
}

// 画出来的像素跟着状态走：左右渐变的图（红通道 = 横向位置），缩放以后鼠标下面那个像素的红值不变
func TestViewerWheelZoomRendersAnchored(t *testing.T) {
	const iw, ih = 2400, 1600
	img := image.NewRGBA(image.Rect(0, 0, iw, ih))
	for y := 0; y < ih; y++ {
		for x := 0; x < iw; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 255 / iw), uint8(y * 255 / ih), 0, 255})
		}
	}
	h := &viewHarness{imgs: []viewImage{{bmp: ui.NewBitmap(img), w: iw, h: ih}}}
	tt := viewTester(h)
	h.show(tt, 0)
	cx, cy := float32(1300), float32(600)
	for i := 0; i < 3; i++ { // 放大到盖满窗口
		tt.Wheel(cx, cy, 0, vWheelDY)
		tt.Frame()
	}
	at := func() color.RGBA { return tt.Image().RGBAAt(int(cx*1.25), int(cy*1.25)) }
	before := at()
	tt.Wheel(cx, cy, 0, vWheelDY)
	tt.Frame()
	after := at()
	r := h.rect(t, tt)
	if r.W < vw || r.H < vh {
		t.Fatalf("放大到 %.0f×%.0f，没盖满窗口", r.W, r.H)
	}
	dr, dg := int(after.R)-int(before.R), int(after.G)-int(before.G)
	if dr < -2 || dr > 2 || dg < -2 || dg > 2 || before.R == 0 || before.A != 255 {
		t.Fatalf("放大一格前后鼠标下的像素 %v → %v，红绿通道（图内位置）应一样（±2）", before, after)
	}
}
