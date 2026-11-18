package main

// 看大图（规格-原生界面第二版 3.8）。自己写，不用 overlay.Lightbox：它把图锁在 MaxWidth(Space(140)) 里按 1.4 的比例画，
// 图小得只剩一个角，点遮罩不关，也不能缩放。外壳照 overlay.Lightbox（Overlay → Modal 遮罩 → Arrive 淡入、圆形翻页和关闭按钮、
// OverlayShortcut 的 Esc 和方向键），缩放和平移的思路照 media.ZoomImage（状态放 ui.Local，kit.Settle 等量好尺寸），
// 它装不下的几处自己补：
//   - 适应窗口：图按比例放进窗口四周各留 viewInset 的区域；小图不放大，最多按原尺寸（viewMaxUp）；
//   - 缩放以鼠标为中心（ZoomImage 绕元素中心、一格一格走），范围是适应窗口的 viewMinRel 倍到原尺寸的 viewMaxS 倍；
//   - 点遮罩（图以外）关闭，拖动平移，双击在适应窗口和原尺寸之间切，键盘 + − 0 和方向键。
// 图按真实缩放后的 DIP 大小布局（Absolute + Left/Top/Size），不用 Scale/Translate：测试和命中都按看到的位置算。
// 原尺寸 = 一个图像像素一个 DIP。ADR-260：静态灯箱读取原文件，聊天缩略图只作为加载占位。

import (
	"fmt"
	"math"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/icon"
	"github.com/ELY-Open-Source/CrychicUI/kit"
	"github.com/ELY-Open-Source/CrychicUI/sound"
	"github.com/egoist/mygo/ui"
)

const (
	viewInsetUnits = 6   // 适应窗口时四周留白，Space(6) = 24 DIP
	viewMaxUp      = 1.0 // 小图适应窗口时不放大，按原尺寸（一个像素一个 DIP）显示：表情包、GIF 放大 2 倍就糊了（2026-11-02 主人："大小是不是太大了"），
	// macOS 预览、Windows 照片也都按原尺寸显示小图；想看细节用滚轮放大
	viewMinRel   = 0.5    // 缩小到适应窗口的一半为止：再小只剩一个点，想看全貌的时候适应窗口已经是全貌
	viewMaxS     = 8.0    // 放大到原尺寸的 8 倍为止（Windows 照片的上限是 800%）：够看清单个像素，再大没有信息
	viewKeyStep  = 1.25   // 键盘 + − 每按一次乘的倍数
	viewWheelK   = 0.0018 // 滚轮缩放：一格（Windows 默认 99 DIP）乘 e^0.178 ≈ 1.2，触摸板的小步按比例走
	viewSnap     = 0.01   // 缩放到离适应窗口不到 1% 就回到适应窗口（跟着窗口大小走、居中）
	viewDragSlop = 4      // 按下以后移动超过这么多 DIP 算拖动，松开不算点击

	viewOpenGuard = 500 * time.Millisecond // 刚打开这么久以内按下的不算点遮罩：系统默认的双击间隔，双击缩略图的第二下会落进来
)

// viewImage 看大图里的一张图：位图，加上"逻辑尺寸"。
// 逻辑尺寸按原文件和像素预算预先计算，缩略图换成原件后框不跳；0 表示未知，用位图大小。
type viewImage struct {
	bmp  *ui.Bitmap
	w, h int
}

// natural 这张图的像素大小（缩放用的"原尺寸"）：有逻辑尺寸且位图的比例对得上（或还是 1×1 的占位）就用逻辑尺寸，否则用位图自己的
func (v viewImage) natural() (float32, float32) {
	bw, bh := v.bmp.Size()
	if v.w <= 0 || v.h <= 0 {
		return float32(bw), float32(bh)
	}
	if bw <= 1 && bh <= 1 {
		return float32(v.w), float32(v.h)
	}
	if math.Abs(float64(bw)/float64(bh)/(float64(v.w)/float64(v.h))-1) > 0.05 {
		return float32(bw), float32(bh)
	}
	return float32(v.w), float32(v.h)
}

// viewGeom 一帧里算位置要的几个数：舞台（整个窗口）的大小、图的原尺寸、适应窗口时的留白
type viewGeom struct {
	sw, sh, iw, ih, inset float32
}

// fit 适应窗口的比例（DIP 每像素）：放进四周留白以内的最大尺寸，小图最多 viewMaxUp 倍
func (g viewGeom) fit() float32 {
	aw, ah := g.sw-2*g.inset, g.sh-2*g.inset
	if aw <= 0 || ah <= 0 || g.iw <= 0 || g.ih <= 0 {
		return 1
	}
	return min(aw/g.iw, ah/g.ih, viewMaxUp)
}

// scaleRange 缩放范围
func (g viewGeom) scaleRange() (lo, hi float32) {
	f := g.fit()
	return f * viewMinRel, max(viewMaxS, f)
}

// viewState 看大图的缩放状态（界面线程，放在 ui.Local 里，关掉就清）。
// s 为 0 是"适应窗口"：每帧按当前窗口重算，窗口变大变小跟着走；否则是 DIP 每像素。cx、cy 是图中心相对舞台中心的偏移
type viewState struct {
	s, cx, cy float32

	// 指针手势（HandleInput 里写）。HandleInput 里不碰元素，要关就记 closeReq，下一次构建接上
	down     bool
	onImage  bool      // 按下的位置在图上
	moved    bool      // 已经算拖动
	openedAt time.Time // 第一次建出来的时间（窗口帧的时钟）；零值是还没建过
	dx0, dy0 float32   // 按下的位置
	cx0, cy0 float32   // 按下时图的偏移
	closeReq bool
}

func (v *viewState) scale(g viewGeom) float32 {
	if v.s > 0 {
		return v.s
	}
	return g.fit()
}

// rect 图在舞台里的位置和大小
func (v *viewState) rect(g viewGeom) (x, y, w, h float32) {
	s := v.scale(g)
	w, h = g.iw*s, g.ih*s
	return g.sw/2 + v.cx - w/2, g.sh/2 + v.cy - h/2, w, h
}

// clampPan 图比舞台大的方向边不能拉进舞台里面，小的方向居中
func clampPan(c, imgSize, stageSize float32) float32 {
	if imgSize <= stageSize {
		return 0
	}
	lim := (imgSize - stageSize) / 2
	return max(-lim, min(lim, c))
}

func (v *viewState) clamp(g viewGeom) {
	s := v.scale(g)
	v.cx, v.cy = clampPan(v.cx, g.iw*s, g.sw), clampPan(v.cy, g.ih*s, g.sh)
}

// anchorZoom 缩放到 s1，舞台上 (px, py)（相对舞台中心）下的那个图点不动，返回新的偏移
func anchorZoom(cx, cy, s0, s1, px, py float32) (float32, float32) {
	k := s1 / s0
	return px - (px-cx)*k, py - (py-cy)*k
}

// setScale 缩放到 s1（已在范围内），以 (px, py) 为中心；离适应窗口不到 viewSnap 就回到适应窗口
func (v *viewState) setScale(g viewGeom, s1, px, py float32) {
	fit := g.fit()
	if float32(math.Abs(float64(s1/fit-1))) < viewSnap {
		v.s, v.cx, v.cy, v.cx0, v.cy0 = 0, 0, 0, 0, 0 // 手势和 openedAt 留着
		return
	}
	v.cx, v.cy = anchorZoom(v.cx, v.cy, v.scale(g), s1, px, py)
	v.s = s1
	v.clamp(g)
}

// zoomBy 乘上 f 倍，超出范围就停在边上
func (v *viewState) zoomBy(g viewGeom, f, px, py float32) {
	lo, hi := g.scaleRange()
	s0 := v.scale(g)
	s1 := max(lo, min(hi, s0*f))
	if s1 != s0 {
		v.setScale(g, s1, px, py)
	}
}

// toggle 双击：适应窗口 ↔ 原尺寸（一个图像像素一个 DIP），以 (px, py) 为中心。适应窗口本来就是原尺寸时改成再大一倍
func (v *viewState) toggle(g viewGeom, px, py float32) {
	fit := g.fit()
	if v.s == 0 {
		target := float32(1)
		if float32(math.Abs(float64(target/fit-1))) < 0.05 {
			target = fit * 2
		}
		lo, hi := g.scaleRange()
		v.setScale(g, max(lo, min(hi, target)), px, py)
		return
	}
	v.setScale(g, fit, px, py)
}

// input 舞台收到的指针输入（HandleInput）。返回有没有接这个事件。
// 点遮罩（图以外）关闭：按下和松开之间没拖动、按下的地方不在图上；按钮和箭头是舞台的兄弟，按它们不到这里
func (v *viewState) input(ev ui.InputEvent, g viewGeom) bool {
	if g.sw <= 0 || g.sh <= 0 {
		return false
	}
	px, py := ev.X-g.sw/2, ev.Y-g.sh/2
	switch ev.Kind {
	case ui.InputScroll:
		v.zoomBy(g, float32(math.Exp(-float64(ev.DY)*viewWheelK)), px, py)
		return true
	case ui.InputZoom:
		if ev.Scale > 0 {
			v.zoomBy(g, ev.Scale, px, py)
		}
		return true
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		x, y, w, h := v.rect(g)
		v.down, v.moved = true, false
		v.dx0, v.dy0, v.cx0, v.cy0 = ev.X, ev.Y, v.cx, v.cy
		v.onImage = ev.X >= x && ev.X < x+w && ev.Y >= y && ev.Y < y+h
		// 习惯性双击缩略图：第一下松开才打开看大图，第二下按下落进来，在遮罩上松开就会关掉。
		// 引擎按"同一个元素上的快速连按"数连击，换了元素就从 1 重数，所以从 Clicks 认不出这一串；按时间认：
		// 刚打开的一个双击间隔内按下的不算点遮罩
		if v.openedAt.IsZero() || ev.Time.Sub(v.openedAt) < viewOpenGuard {
			v.moved = true
		}
		if ev.Clicks > 0 && ev.Clicks%2 == 0 && v.onImage { // 2、4 次算双击；连点时 3 不算
			v.toggle(g, px, py)
			v.moved = true // 双击不再算单击
			v.cx0, v.cy0 = v.cx, v.cy
		}
		return true
	case ui.InputPointerMove:
		if !v.down {
			return false
		}
		dx, dy := ev.X-v.dx0, ev.Y-v.dy0
		if !v.moved && math.Hypot(float64(dx), float64(dy)) <= viewDragSlop {
			return true
		}
		v.moved = true
		if v.s > 0 {
			s := v.s
			v.cx, v.cy = clampPan(v.cx0+dx, g.iw*s, g.sw), clampPan(v.cy0+dy, g.ih*s, g.sh)
		}
		return true
	case ui.InputPointerUp:
		if ev.Button != 0 || !v.down {
			return false
		}
		v.down = false
		if !v.moved && !v.onImage {
			v.closeReq = true
		}
		return true
	}
	return false
}

// viewImages 配上逻辑尺寸；动图复用列表的播放产物，静态图使用原件的解码尺寸。
func viewImages(imgs []seg, bmps []*ui.Bitmap) []viewImage {
	out := make([]viewImage, len(imgs))
	for i, s := range imgs {
		out[i].bmp = bmps[i]
		if !s.Animated && s.Width > 0 && s.Height > 0 {
			out[i].w, out[i].h = fitSize(s.Width, s.Height, int(imgMaxPixels))
		}
	}
	return out
}

func b2f(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// lightbox 看大图。imgs 每一张都要有位图；at 是正在看的那张，open 为真才画；st 是缩放状态，调用方留着（关掉时这里清掉）。
// 关闭（按钮、Esc、点遮罩）都是 *open = false 并让返回的元素 Changed，调用方照 overlay.Lightbox 的用法接
func lightbox(c *ui.Context, label string, imgs []viewImage, at *int, open *bool, st *viewState) ui.Element {
	if label == "" || len(imgs) == 0 || at == nil || open == nil || st == nil || *at < 0 || *at >= len(imgs) {
		panic(fmt.Sprintf("看大图 %q 需要图、正在看的一张和 open", label))
	}
	for _, im := range imgs {
		if im.bmp == nil {
			panic(fmt.Sprintf("看大图 %q 的图不许是 nil", label))
		}
	}
	t := c.Theme()
	root := ui.Box(c)
	shut := func() {
		*open = false
		root.SetChanged()
	}
	if *open && st.closeReq { // 上一次输入里点了遮罩
		shut()
	}
	if !*open {
		*st = viewState{}
		return root
	}
	if st.openedAt.IsZero() {
		st.openedAt = c.Now()
	}
	shown, last := *at, len(imgs)-1
	back, forth := "chevron-left", "chevron-right"
	prev, next := ui.KeyLeft, ui.KeyRight
	if c.Direction() == ui.RightToLeft {
		back, forth, prev, next = forth, back, next, prev
	}
	ink := t.Background
	if t.Dark {
		ink = t.Text
	}
	turn := func(to int) {
		*at = to
		*st = viewState{openedAt: st.openedAt}
		c.Invalidate() // 这一遍已经按旧的一张建完了，再建一遍
		sound.Play(sound.Swipe)
		kit.Log("看大图", "shown", "index", to)
	}
	arrow := func(name, glyph string, off bool) ui.Element {
		b := kit.Press(ui.ButtonBase(c).Label(name).Tooltip(name).Size(t.Space(11), t.Space(11)).Shrink(0).Center().Radius(999)).Disabled(off)
		return b.Background(b.AnimateColor("bg", ink.Alpha(0.12+0.12*b2f(b.Hovered())), ui.Snappy)).TextColor(ink).
			Children(func() { ui.Icon(c, icon.Must(glyph)).Size(16, 16) })
	}
	ui.Overlay(c, func() {
		scrim := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Modal().Role(ui.RoleDialog).Label(label)
		var closeBtn, backBtn, forthBtn ui.Element
		var g viewGeom
		kit.Arrive(scrim.Background(kit.Shade(0.88)), ui.Pose{Fade: 1}, 0).Children(func() {
			stage := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0)
			b := stage.LayoutBounds()
			kit.Settle(c, stage, []float32{b.W, b.H}, func() []float32 { r := stage.LayoutBounds(); return []float32{r.W, r.H} })
			iw, ih := imgs[shown].natural()
			g = viewGeom{sw: b.W, sh: b.H, iw: iw, ih: ih, inset: t.Space(viewInsetUnits)}
			if g.sw > 0 && g.sh > 0 {
				st.clamp(g)
			}
			stage.HandleInput(func(ev ui.InputEvent) bool { return st.input(ev, g) })
			stage.Children(func() {
				if g.sw <= 0 || g.sh <= 0 {
					return // 第一帧还没量出窗口大小，Settle 会再建一次
				}
				x, y, w, h := st.rect(g)
				c.Key(shown)
				img := ui.Image(c, imgs[shown].bmp).Absolute().Left(x).Top(y).Size(w, h).Fit(ui.FillBox).Label(imageLabel(label, shown, len(imgs)))
				kit.Arrive(img, ui.Pose{Fade: 1}, 0)
			})
			// 按钮是舞台的兄弟，叠在它上面：按它们不会被当成点遮罩
			closeBtn = arrow("关闭", "x", false).Absolute().End(t.Space(4)).Top(max(t.Space(4), roomOf(c.TitleBar()).Bar+t.Space(2))) // 系统三键在窗口右上角，关闭键排到它们下面
			if last > 0 {
				mid := b.H/2 - t.Space(11)/2
				backBtn = arrow("上一张", back, shown == 0).Absolute().Left(t.Space(4)).Top(mid)
				forthBtn = arrow("下一张", forth, shown == last).Absolute().End(t.Space(4)).Top(mid)
			}
		})
		switch {
		case closeBtn.Clicked(), scrim.OverlayShortcut(0, ui.KeyEscape):
			shut()
		case g.sw > 0 && (scrim.OverlayShortcut(0, ui.KeyEqual) || scrim.OverlayShortcut(ui.Shift, ui.KeyEqual)):
			st.zoomBy(g, viewKeyStep, 0, 0)
			c.Invalidate()
		case g.sw > 0 && scrim.OverlayShortcut(0, ui.KeyMinus):
			st.zoomBy(g, 1/viewKeyStep, 0, 0)
			c.Invalidate()
		case g.sw > 0 && scrim.OverlayShortcut(0, ui.Key0):
			*st = viewState{openedAt: st.openedAt}
			c.Invalidate()
		case last > 0 && (backBtn.Clicked() || scrim.OverlayShortcut(0, prev)) && shown > 0:
			turn(shown - 1)
		case last > 0 && (forthBtn.Clicked() || scrim.OverlayShortcut(0, next)) && shown < last:
			turn(shown + 1)
		}
	})
	return root
}

// imageLabel 大图的无障碍名字（测试也靠它找图的位置）：单张是"<标签>·大图"，多张带序号
func imageLabel(label string, i, n int) string {
	if n == 1 {
		return label + "·大图"
	}
	return fmt.Sprintf("%s·大图 %d/%d", label, i+1, n)
}
