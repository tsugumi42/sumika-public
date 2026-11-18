package winshell

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

// 选中块滑动（设计语言第九节“滑块指示器”）：一组里选中的那块是独立的一块，换选中项时从旧位置滑到新位置，
// 不是旧的变暗、新的变亮。分段、设置分类、单选筛选片、导航图标、会话行都用这一个函数，不各写各的。
//
// 做法：选中块锚在选中项“现在”的位置上（每帧量选中项相对容器的矩形），所以滚动、改宽、拖分栏时它始终贴着选中项，
// 不会被当成换了选项重播。只有选中项的编号变了的那一帧，才把“旧块在哪 − 新项在哪”踢给四个差值通道，让它们回到 0：
// 横排看左右两条边、竖排看上下两条边，每条边一根弹簧：走向那一侧的前缘用快弹簧、另一侧的后缘用慢弹簧，所以块先被拉长再收回原宽。减少动态效果时弹簧直接给终点，差值恒为 0，一帧到位。

// SlideState 是一组选中块的记忆：容器和每个选项的句柄、上一次选的是谁、上一帧画在哪。每组一份，放在 Kit 里或控件自己的状态里。
type SlideState struct {
	host  *ui.Handle
	items map[string]*ui.Handle
	sel   string
	has   bool
	cur   ui.Rect // 上一帧选中块画在哪（相对容器），换选项时从这里出发
	// vertical 是这次移动是竖着的（看上下两条边）；towardEnd 是往右、往下走（后一条边是前缘）
	vertical, towardEnd bool
	Rect                ui.Rect // 这一帧选中块画在哪（相对容器），给测试量位置、宽度
	epoch               map[string]int
	on                  map[string]bool
	Moving              bool // 还在滑（任一差值没回到 0.25 以内）
}

// FillKey 是选项 id 的“悬停、按下底色”动画通道名。选项被选中的那些帧里不用这条通道，通道里留着的是上次悬停或按下时的半透明深色；
// 如果取消选中后接着用它，就会从那个深色淡回透明，在刚被选中块盖着的旧选项上播出一段独立的淡出。
// 所以每次从“选中”变成“没选中”换一个新通道名：取消选中后的第一帧，新通道直接从“没悬停、没按下”的终值开始，不淡。
func (s *SlideState) FillKey(id string, selected bool) string {
	if s.epoch == nil {
		s.epoch, s.on = map[string]int{}, map[string]bool{}
	}
	if !selected && s.on[id] { // 刚取消选中：换新通道
		s.epoch[id]++
	}
	s.on[id] = selected
	return fmt.Sprint("fill-", s.epoch[id])
}

// Host 是容器的句柄（量它的位置用）。
func (s *SlideState) Host() *ui.Handle {
	if s.host == nil {
		s.host = &ui.Handle{}
	}
	return s.host
}

// UseHost 让容器句柄用调用方已有的那一个。
func (s *SlideState) UseHost(h *ui.Handle) { s.host = h }

// Item 是选项 id 的句柄；调用方在造选项时 Bind 它。
func (s *SlideState) Item(id string) *ui.Handle {
	if s.items == nil {
		s.items = map[string]*ui.Handle{}
	}
	h := s.items[id]
	if h == nil {
		h = &ui.Handle{}
		s.items[id] = h
	}
	return h
}

// UseItem 让选项 id 用调用方已有的句柄。
func (s *SlideState) UseItem(id string, h *ui.Handle) {
	if s.items == nil {
		s.items = map[string]*ui.Handle{}
	}
	s.items[id] = h
}

// SlideOptions 是选中块的画法。
type SlideOptions struct {
	// Paint 画选中块；r 是它这一帧的矩形（窗口坐标）。nil 是强调色浅底（Palette.Self）的平块。
	Paint func(pt *ui.Painter, r ui.Rect)
	// Radius 是选中块的圆角，也是 nil Paint 时用的圆角。
	Radius float32
	// ClipRadius 是容器的圆角：选中块（含回弹过冲的那一点）裁到容器里，不画到容器外面。
	ClipRadius float32
	// Under 在选中块下面先画一层（单选筛选片的各片底色）；hr 是容器的矩形（窗口坐标）。
	Under func(pt *ui.Painter, hr ui.Rect)
	// OwnWhenSettled 为真时，选中块只在滑动期间由容器画，滑完交还选中项自己画底（虚拟列表的会话行：
	// 行滚动时底跟着行走，容器画的块跟不上）。
	OwnWhenSettled bool
}

// SlideIndicator 在 host 上画选中块，返回“选中项这一帧要不要自己画底”：
// 还没量到选中项位置的第一帧、选中项不在屏幕里（虚拟列表没构建它）、或 OwnWhenSettled 且已经滑完时为 true。
// host 是容器元素，st 是这一组的记忆（容器句柄、各选项句柄），sel 是选中项的 id（没有选中给空串）。
func (k *Kit) SlideIndicator(c *ui.Context, host ui.Element, st *SlideState, sel string, o SlideOptions) (own bool) {
	host.Bind(st.Host())
	hb := st.Host().Bounds(c)
	var live ui.Rect
	measured := false
	if h := st.items[sel]; h != nil && sel != "" && hb.W > 0 && hb.H > 0 {
		if b := h.Bounds(c); b.W > 0 && b.H > 0 && rectsOverlap(b, hb) {
			live = ui.Rect{X: b.X - hb.X, Y: b.Y - hb.Y, W: b.W, H: b.H}
			measured = true
		}
	}
	if !measured {
		// 量不到：选中项自己上底，也不踢（旧块在哪都作废，新项出现直接到位）
		st.has, st.sel, st.Moving = false, sel, false
		return true
	}
	if st.has && st.sel != sel {
		// 换了选项：横排看左右两条边、竖排看上下两条边，各自从旧块的边出发回到新项的边；
		// 另一个方向（横排的上下）直接跟着新项，不加弹簧
		dx, dy := live.X-st.cur.X, live.Y-st.cur.Y
		st.vertical = abs32f(dy) > abs32f(dx)
		if st.vertical {
			st.towardEnd = dy > 0
			host.Jump("edge-a", st.cur.Y-live.Y)
			host.Jump("edge-b", st.cur.Y+st.cur.H-(live.Y+live.H))
		} else {
			st.towardEnd = dx > 0
			host.Jump("edge-a", st.cur.X-live.X)
			host.Jump("edge-b", st.cur.X+st.cur.W-(live.X+live.W))
		}
	}
	st.sel, st.has = sel, true
	// 往哪边走，那一侧的边是前缘（快弹簧），另一侧是后缘（慢弹簧）：块先被拉长，后缘再追上来，到位时恢复原宽
	// （照开源库 Fluid 的 PillTabs，MIT）。第一次出现直接放在终点：通道的第一个目标就是初值。
	fast, slow := slowSpring(k.T.Spring.StretchLead), slowSpring(k.T.Spring.StretchTrail) // “拉伸”这一对预设
	specA, specB := fast, slow                                                            // a 边是左、上，b 边是右、下；往左、上走时 a 边是前缘
	if st.towardEnd {
		specA, specB = slow, fast
	}
	ea := host.Spring("edge-a", 0, specA)
	eb := host.Spring("edge-b", 0, specB)
	r := live
	if st.vertical {
		r.Y, r.H = live.Y+ea, max(live.H+eb-ea, 0)
	} else {
		r.X, r.W = live.X+ea, max(live.W+eb-ea, 0)
	}
	st.cur, st.Rect = r, r
	st.Moving = max(abs32f(ea), abs32f(eb)) > slideSettle
	own = o.OwnWhenSettled && !st.Moving
	if own && o.Under == nil {
		return true
	}
	paint := o.Paint
	if paint == nil {
		radius := o.Radius // 选中块是强调色浅底的平块，不加阴影；选中项自己画底时用同一个色、同一个圆角（交接不闪）
		paint = func(pt *ui.Painter, ir ui.Rect) { k.selectPaint(pt, ir, radius, selMode) }
	}
	host.Draw(func(pt *ui.Painter, hr ui.Rect) {
		pt.Clip(hr, o.ClipRadius, func() {
			if o.Under != nil {
				o.Under(pt, hr)
			}
			if !own {
				paint(pt, ui.Rect{X: hr.X + r.X, Y: hr.Y + r.Y, W: r.W, H: r.H})
			}
		})
	})
	return own
}

// slideSettle 是差值小到多少算“滑完”（DIP）：小于四分之一像素看不出来。
const slideSettle float32 = .25

func abs32f(v float32) float32 { return float32(math.Abs(float64(v))) }

func rectsOverlap(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// slides 取（第一次用时建）按键记的一组选中块记忆。
func (k *Kit) slides(key string) *SlideState {
	if k.slideSt == nil {
		k.slideSt = map[string]*SlideState{}
	}
	s := k.slideSt[key]
	if s == nil {
		s = &SlideState{}
		k.slideSt[key] = s
	}
	return s
}

func slideID(i int) string { return fmt.Sprint(i) }
