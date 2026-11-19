package uitest

// 元素框审计：找出“框比实际看得见的内容大”和“框和看得见的东西对不上”的几类情况。
// 读已提交的元素树（Tree），只测不改。分类依据见 docs/archive/notes/排查-元素框超出可见内容-2026-11-09.md。
//
// 坐标：Node.Box 是布局框（窗口坐标，已含滚动偏移，不含动画变换）；带变换的节点（Moved）不算。
// 裁剪：和 mygo 画的时候一致（paint.go clipRect）——滚动容器两个方向都裁，Clip 只裁各自开的方向，范围是边框内侧。

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 检查的类别，对应排查笔记里的五类。
const (
	ClassOverClip   = 1 // 超出最近裁剪祖先或窗口
	ClassOverParent = 2 // 超出没裁剪的父元素：内容真的画到外面
	ClassHiddenRoom = 3 // 隐藏、透明、零尺寸或收起，但还占位或还在无障碍树里
	ClassTextWide   = 4 // 文字框明显大于文字本身
	ClassHitArea    = 5 // 点击、悬停区超出可见范围，或盖住相邻控件
)

// 子类：同一类里按根因分，Classify 按它分组。
const (
	SubScrollAxis   = "滚动方向内容超出视口"       // 1：滚动容器沿滚动方向放不下，正常
	SubScrollCross  = "滚动容器交叉方向内容被切"     // 1：滚动容器另一个方向放不下，内容被切掉又滚不到
	SubClipAxis     = "Clip 切掉了内容"       // 1：Clip 开的方向上内容比框大
	SubWindow       = "超出窗口"             // 1：没有任何裁剪祖先，越过窗口边
	SubOverParent   = "越过不裁剪的父元素"        // 2
	SubInvisible    = "Invisible 占位"     // 3：不画、不进无障碍树，但占位
	SubOpacity0     = "透明度 0 还能点"        // 3：不画，但里面有没禁用的可点元素
	SubZeroSize     = "零尺寸还在无障碍树"        // 3
	SubHiddenSized  = "Hidden 却有尺寸"      // 3：布局应该不给地方
	SubClippedAway  = "被裁没了还在无障碍树"       // 3：整个在 Clip 之外（收起），还留在树里、还能聚焦
	SubScrolledOut  = "滚出视口的元素"          // 3：虚拟列表、滚动容器里滚出去的，无障碍可“滚入视野”
	SubOpacity0Off  = "透明度 0 但已禁用"       // 3：悬停才出现的操作条，按钮已禁用
	SubTextFill     = "文字被拉满（可选中）"       // 4
	SubTextHover    = "文字被拉满（在可点、悬停的块里）" // 4
	SubTextPlain    = "文字被拉满（普通）"        // 4
	SubHitShort     = "点击区被裁短"           // 5：框比可见的大，点击按可见的算
	SubHitOverlap   = "可点元素互相重叠"         // 5
	SubHitAbsolute  = "浮层可点元素盖住别的可点元素"   // 5
	SubHitModal     = "模态层盖住下面的控件"
	SubHitUnknown   = "自定义命中区，盒子不算数" // 5
	SubHitClippedAw = "可点元素整个被裁没"    // 5
)

// Finding 是一条发现：哪一类、哪个元素、框和限制多少、超出多少。
type Finding struct {
	Class int
	Sub   string
	Path  string // 祖先名字串起来；Name 取自 Key、Label、文字或控件名
	Name  string
	Box   ui.Rect
	Limit ui.Rect    // 比它的范围：裁剪框、父框或文字自然宽
	Over  [4]float32 // 上右下左各超出多少（超出为正）；第 4 类第 0 位放“多出的宽”
	Info  string
}

func (f Finding) String() string {
	return fmt.Sprintf("类%d/%s %s 框%.0f,%.0f %.0fx%.0f 限%.0f,%.0f %.0fx%.0f 超%v %s", f.Class, f.Sub, f.Path, f.Box.X, f.Box.Y, f.Box.W, f.Box.H, f.Limit.X, f.Limit.Y, f.Limit.W, f.Limit.H, f.Over, f.Info)
}

// Path 是从根到这个节点、有名字的祖先串起来的路径（最多留最后 8 段）。
func (n *Node) Path() string {
	var parts []string
	for p := n; p != nil; p = p.Parent {
		name := p.Key
		if name == "" {
			name = p.Label
		}
		if name == "" && p.Widget != "" {
			name = p.Widget
		}
		if name == "" && p.Text != "" {
			t := []rune(p.Text)
			if len(t) > 12 {
				t = t[:12]
			}
			name = "“" + string(t) + "”"
		}
		if name != "" {
			parts = append(parts, name)
		}
	}
	if len(parts) > 8 {
		parts = parts[:8]
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ">")
}

// clipOf 是节点 p 给里面内容画的裁剪框（和 mygo 的 clipRect 一致），没有裁剪返回 ok=false；
// axisX、axisY 说明哪个方向在裁。
func clipOf(p *Node) (r ui.Rect, axisX, axisY, scroll bool, ok bool) {
	scroll = p.Scroll
	axisX, axisY = p.ClipX || p.Scroll, p.ClipY || p.Scroll
	if !axisX && !axisY && !p.HasClipInset {
		return ui.Rect{}, false, false, false, false
	}
	r = p.Box
	return r, axisX || p.HasClipInset, axisY || p.HasClipInset, scroll, true
}

// edge 是 n 的框超出 limit 的四边量（上右下左，超出为正）。
func edge(n, limit ui.Rect) [4]float32 {
	return [4]float32{limit.Y - n.Y, n.X + n.W - (limit.X + limit.W), n.Y + n.H - (limit.Y + limit.H), limit.X - n.X}
}

func (n *Node) live() bool {
	return !n.Hidden && !n.Invisible && !n.Inert && n.Box.W > 0 && n.Box.H > 0
}

// inMovedTree 是自己或祖先带动画变换：位置不是布局框，不审计。
func (n *Node) inMovedTree() bool {
	for p := n; p != nil; p = p.Parent {
		if p.Moved {
			return true
		}
	}
	return false
}

// dead 是自己或祖先被藏起来（Hidden、Invisible、Inert）：不画也不进无障碍树，不审计。
// AccessHidden 只是不进无障碍树，照常绘制，所以不算。
func (n *Node) dead() bool {
	for p := n; p != nil; p = p.Parent {
		if p.Hidden || p.Invisible || p.Inert {
			return true
		}
	}
	return false
}

// Audit 在一棵树上跑五类检查，返回发现；win 是窗口的逻辑尺寸。
// 带变换的子树数进 skipped（不审计）。
func Audit(root *Node, win ui.Rect) (out []Finding, skipped int) {
	add := func(f Finding) { out = append(out, f) }
	var all []*Node
	root.Walk(func(n *Node) { all = append(all, n) })
	for _, n := range all {
		if n.Hidden && n.Box.W > 0 && n.Box.H > 0 && n.Parent != nil && !n.Parent.Hidden {
			add(Finding{Class: ClassHiddenRoom, Sub: SubHiddenSized, Path: n.Path(), Name: n.Name(), Box: n.Box, Info: "Hidden 的节点布局框不为 0"})
		}
		if n.dead() {
			if n.Invisible && n.Box.W > 0 && n.Box.H > 0 && !n.Parent.deadSelf() {
				add(Finding{Class: ClassHiddenRoom, Sub: SubInvisible, Path: n.Path(), Name: n.Name(), Box: n.Box, Info: "占位但不画、不进无障碍树"})
			}
			continue
		}
		if n.inMovedTree() {
			if n.Moved {
				skipped++
			}
			continue
		}
		if n.Box.W <= 0 || n.Box.H <= 0 {
			if n.Parent != nil && (n.Label != "" || n.Text != "" || n.Clickable || n.Focusable) {
				add(Finding{Class: ClassHiddenRoom, Sub: SubZeroSize, Path: n.Path(), Name: n.Name(), Box: n.Box, Info: "框 0 大小但有文字、标签或可点"})
			}
			continue
		}
		if n.OpacitySet && n.Opacity <= 0 && !n.IgnoreInput {
			sub, info := SubOpacity0Off, "透明度 0；里面的可点元素都已禁用，只留在无障碍树里"
			if hasLiveControl(n) {
				sub, info = SubOpacity0, "透明度 0；里面有没禁用的可点元素，看不见却点得到"
			}
			add(Finding{Class: ClassHiddenRoom, Sub: sub, Path: n.Path(), Name: n.Name(), Box: n.Box, Info: info})
		}
		checkOverflow(n, win, add)
		checkText(n, add)
	}
	checkHit(all, win, add)
	return
}

// hasLiveControl：自己或后代有没禁用、没穿透的可点或可聚焦元素。
func hasLiveControl(n *Node) bool {
	found := false
	n.Walk(func(x *Node) {
		if (x.Clickable || x.Focusable) && !x.Disabled && !x.IgnoreInput && !x.PassThrough && !x.dead() {
			found = true
		}
	})
	return found
}

func (n *Node) deadSelf() bool { return n != nil && (n.Hidden || n.Invisible || n.Inert) }

// visibleRect 是 n 的框被全部裁剪祖先（含窗口）裁完剩下的部分；空返回 ok=false。
func visibleRect(n *Node, win ui.Rect) (ui.Rect, bool) {
	r, _, ok := visibleRectWhy(n, win)
	return r, ok
}

// visibleRectWhy 同 visibleRect；被裁没时 scrolled 说明是不是被滚动容器沿滚动方向裁掉的（滚出视口）。
func visibleRectWhy(n *Node, win ui.Rect) (out ui.Rect, scrolled, ok bool) {
	r := n.Box
	for p := n.Parent; p != nil; p = p.Parent {
		c, ax, ay, _, ok := clipOf(p)
		if !ok {
			continue
		}
		if ax {
			x0, x1 := math.Max(float64(r.X), float64(c.X)), math.Min(float64(r.X+r.W), float64(c.X+c.W))
			r.X, r.W = float32(x0), float32(x1-x0)
		}
		if ay {
			y0, y1 := math.Max(float64(r.Y), float64(c.Y)), math.Min(float64(r.Y+r.H), float64(c.Y+c.H))
			r.Y, r.H = float32(y0), float32(y1-y0)
		}
		if r.W <= 0 || r.H <= 0 {
			return ui.Rect{}, p.Scroll && (p.ScrollY() && r.H <= 0 || p.ScrollX() && r.W <= 0), false
		}
	}
	x0, y0 := math.Max(float64(r.X), float64(win.X)), math.Max(float64(r.Y), float64(win.Y))
	x1, y1 := math.Min(float64(r.X+r.W), float64(win.X+win.W)), math.Min(float64(r.Y+r.H), float64(win.Y+win.H))
	if x1 <= x0 || y1 <= y0 {
		return ui.Rect{}, false, false
	}
	return ui.Rect{X: float32(x0), Y: float32(y0), W: float32(x1 - x0), H: float32(y1 - y0)}, false, true
}

// checkOverflow 查第 1、2 类：n 的框超出最近的裁剪祖先（第 1 类），或超出没有裁剪的父元素（第 2 类）。
// 只报最上面那个越界的元素：它的父元素在同一个限制里没有越界，里面的后代跟着越界不重复报。
func checkOverflow(n *Node, win ui.Rect, add func(Finding)) {
	p := n.Parent
	if p == nil {
		return
	}
	// 第 2 类：父元素在这个方向上不裁剪，子元素的框越过父元素的框。
	// 浮层（Absolute）脱离文档流，越出所属容器另有检查（CheckTree 的浮层一条），这里不算。
	// 文字里的行内元素（Kind==1 的父）的位置由排版给出，框是整段文字的外框，不是布局子元素，不算。
	if !n.Absolute && p.Kind != 1 {
		o := edge(n.Box, p.Box)
		_, ax, ay, _, _ := clipOf(p)
		var bad [4]float32
		any := false
		for side, d := range o {
			horizontal := side == 1 || side == 3
			if d > 1 && ((horizontal && !ax) || (!horizontal && !ay)) {
				bad[side], any = d, true
			}
		}
		if any && !ancestorOverflowsSame(p, bad) {
			add(Finding{Class: ClassOverParent, Sub: SubOverParent, Path: n.Path(), Name: n.Name(), Box: n.Box, Limit: p.Box, Over: bad, Info: "父 " + p.Name()})
		}
	}
	// 第 1 类：最近的裁剪祖先。
	for c := p; c != nil; c = c.Parent {
		cb, ax, ay, scroll, ok := clipOf(c)
		if !ok {
			continue
		}
		// 滚动容器的“滚动方向”内容放不下是正常的；其他方向放不下是内容被切掉又滚不到。
		o := edge(n.Box, cb)
		var bad [4]float32
		any, crossOnly, axisOnly := false, false, false
		for side, d := range o {
			horizontal := side == 1 || side == 3
			if d > 1 && ((horizontal && ax) || (!horizontal && ay)) {
				bad[side], any = d, true
				scrollAxis := (horizontal && c.ScrollX()) || (!horizontal && c.ScrollY())
				if scroll && scrollAxis {
					axisOnly = true
				} else {
					crossOnly = true
				}
			}
		}
		if any && !nodeOverflowsSameClip(p, c, bad) {
			sub := SubClipAxis
			info := "裁剪者 " + c.Name()
			switch {
			case scroll && axisOnly && !crossOnly:
				sub = SubScrollAxis
			case scroll:
				sub = SubScrollCross
			}
			add(Finding{Class: ClassOverClip, Sub: sub, Path: n.Path(), Name: n.Name(), Box: n.Box, Limit: cb, Over: bad, Info: info})
		}
		return
	}
	// 没有裁剪祖先：窗口本身就是裁剪。（上面的循环找到裁剪祖先就已经 return。）
	if o := edge(n.Box, win); (o[0] > 1 || o[1] > 1 || o[2] > 1 || o[3] > 1) && !windowOverflows(p, win) {
		add(Finding{Class: ClassOverClip, Sub: SubWindow, Path: n.Path(), Name: n.Name(), Box: n.Box, Limit: win, Over: o, Info: "没有裁剪祖先"})
	}
}

// ScrollX、ScrollY 说明滚动容器沿哪个方向滚（Tree 从 flags 读出）。
func (n *Node) ScrollX() bool { return n.Scroll && n.scrollX }
func (n *Node) ScrollY() bool { return n.Scroll && n.scrollY }

func windowOverflows(p *Node, win ui.Rect) bool {
	for ; p != nil; p = p.Parent {
		o := edge(p.Box, win)
		if o[0] > 1 || o[1] > 1 || o[2] > 1 || o[3] > 1 {
			return true
		}
	}
	return false
}

// ancestorOverflowsSame 说明父元素自己是不是也越过了它的父（同一个方向），是的话子元素不重复报。
func ancestorOverflowsSame(p *Node, bad [4]float32) bool {
	if p.Parent == nil || p.Absolute {
		return false
	}
	o := edge(p.Box, p.Parent.Box)
	_, ax, ay, _, _ := clipOf(p.Parent)
	for side, d := range bad {
		if d <= 0 {
			continue
		}
		horizontal := side == 1 || side == 3
		if o[side] > 1 && ((horizontal && !ax) || (!horizontal && !ay)) {
			return true
		}
	}
	return false
}

// nodeOverflowsSameClip：n 的父 p 到裁剪者 c 之间，p 是不是已经在同一边越过了 c 的裁剪框。
func nodeOverflowsSameClip(p, c *Node, bad [4]float32) bool {
	if p == c {
		return false
	}
	cb, _, _, _, _ := clipOf(c)
	o := edge(p.Box, cb)
	for side, d := range bad {
		if d > 0 && o[side] > 1 {
			return true
		}
	}
	return false
}

// checkText 查第 4 类：文字框比文字自己的最大内容宽还宽。只分桶，不下对错。
func checkText(n *Node, add func(Finding)) {
	if n.Kind != 1 || n.Text == "" || n.TextW <= 0 || n.TextLines != 1 {
		return // 折行的文字框宽就是容器宽，不算“比文字大”
	}
	extra := n.Box.W - n.Pad[1] - n.Pad[3] - n.TextW
	if extra <= 2 {
		return
	}
	sub := SubTextPlain
	info := "拉满: "
	if n.WidthFill {
		info += "FillWidth"
	} else {
		info += "对齐拉伸或 grow"
	}
	for p := n; p != nil; p = p.Parent {
		if p.Clickable || p.Hover {
			sub, info = SubTextHover, info+"；"+p.Name()+"可点或悬停"
			break
		}
	}
	if n.Selectable {
		sub = SubTextFill
	}
	add(Finding{Class: ClassTextWide, Sub: sub, Path: n.Path(), Name: n.Name(), Box: n.Box, Over: [4]float32{extra}, Info: info})
}

// checkHit 查第 5 类：可点、可聚焦、悬停的元素，点击区按可见部分算（mygo 的命中测试跟着裁剪走）。
func checkHit(all []*Node, win ui.Rect, add func(Finding)) {
	type hit struct {
		n   *Node
		vis ui.Rect
	}
	var hits []hit
	for _, n := range all {
		if !(n.Clickable || n.Focusable) || n.Disabled || n.IgnoreInput || n.PassThrough || n.dead() || n.inMovedTree() || n.Box.W <= 0 || n.Box.H <= 0 {
			continue
		}
		if n.HasHit {
			add(Finding{Class: ClassHitArea, Sub: SubHitUnknown, Path: n.Path(), Name: n.Name(), Box: n.Box, Info: "HitTest 自定义"})
		}
		vis, scrolled, ok := visibleRectWhy(n, win)
		if !ok {
			sub, info := SubClippedAway, "可点或可聚焦，整个被 Clip 裁没"
			if scrolled {
				sub, info = SubScrolledOut, "可点或可聚焦，滚出了视口（无障碍可滚入视野）"
			}
			add(Finding{Class: ClassHiddenRoom, Sub: sub, Path: n.Path(), Name: n.Name(), Box: n.Box, Info: info})
			continue
		}
		if vis.W < n.Box.W-1 || vis.H < n.Box.H-1 {
			add(Finding{Class: ClassHitArea, Sub: SubHitShort, Path: n.Path(), Name: n.Name(), Box: n.Box, Limit: vis, Info: fmt.Sprintf("可见 %.0fx%.0f / 框 %.0fx%.0f", vis.W, vis.H, n.Box.W, n.Box.H)})
		}
		hits = append(hits, hit{n, vis})
	}
	// 重叠：两个可点元素的可见部分相交，且不是祖先和后代。
	for i := range hits {
		for j := i + 1; j < len(hits); j++ {
			a, b := hits[i], hits[j]
			if related(a.n, b.n) {
				continue
			}
			ix := math.Min(float64(a.vis.X+a.vis.W), float64(b.vis.X+b.vis.W)) - math.Max(float64(a.vis.X), float64(b.vis.X))
			iy := math.Min(float64(a.vis.Y+a.vis.H), float64(b.vis.Y+b.vis.H)) - math.Max(float64(a.vis.Y), float64(b.vis.Y))
			if ix <= 1 || iy <= 1 {
				continue
			}
			sub := SubHitOverlap
			if inModal(a.n) != inModal(b.n) {
				sub = SubHitModal // 模态层（对话框、抽屉的遮罩）本来就把下面的点击拦下
			} else if a.n.Absolute || b.n.Absolute || inAbsolute(a.n) != inAbsolute(b.n) {
				sub = SubHitAbsolute
			}
			add(Finding{Class: ClassHitArea, Sub: sub, Path: a.n.Path(), Name: a.n.Name() + " × " + b.n.Name(), Box: a.n.Box, Limit: b.n.Box, Over: [4]float32{float32(ix), float32(iy)}, Info: "另一个 " + b.n.Path()})
		}
	}
}

func related(a, b *Node) bool {
	for p := a; p != nil; p = p.Parent {
		if p == b {
			return true
		}
	}
	for p := b; p != nil; p = p.Parent {
		if p == a {
			return true
		}
	}
	return false
}

func inModal(n *Node) bool {
	for p := n; p != nil; p = p.Parent {
		if p.Modal {
			return true
		}
	}
	return false
}

func inAbsolute(n *Node) bool {
	for p := n; p != nil; p = p.Parent {
		if p.Absolute {
			return true
		}
	}
	return false
}

// Group 是同一根因的一组发现。
type Group struct {
	Class int
	Sub   string
	Name  string // 元素名（取叶子名，不带路径）
	Count int
	Eg    Finding
}

// Classify 把发现按（类、子类、元素名）分组，按数量从多到少排。
func Classify(fs []Finding) []Group {
	idx := map[string]*Group{}
	var out []*Group
	for _, f := range fs {
		name := f.Name
		if i := strings.Index(name, " × "); i >= 0 {
			name = name[:i]
		}
		k := fmt.Sprintf("%d|%s|%s", f.Class, f.Sub, name)
		g := idx[k]
		if g == nil {
			g = &Group{Class: f.Class, Sub: f.Sub, Name: name, Eg: f}
			idx[k] = g
			out = append(out, g)
		}
		g.Count++
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Count > out[j].Count
	})
	res := make([]Group, len(out))
	for i, g := range out {
		res[i] = *g
	}
	return res
}
