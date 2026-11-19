package winshell

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// 控件的立体感只有三种，每个控件归到一种，底色、边线、阴影只通过这里上：
//
//	凹下去 Sunken：等人往里填东西的（输入框、搜索框、步进器外框、下拉入口）。凹槽底（Palette.Well）+ 一圈细线 + 很浅的上沿内阴影；
//	平的   Flat：贴在面上的（按钮、图标钮、分段轨道、没选中的筛选片）。只有底色，悬停、按下各深一层；
//	浮起来 Raised：选中块、卡片、开关和滑块的圆钮（内容档）；菜单、下拉面板、对话框（浮层档，FloatShadow）。
//
// 阴影颜色只从 shadowInk 一处取（主题令牌 color.shadow-a 的色相），不在各处写黑色加透明度。

// 阴影的形状：内容档 0,1,3；浮层档 0,8,24；凹块的上沿内阴影 0,1,2。
const (
	raisedY, raisedBlur float32 = 1, 3
	floatY, floatBlur   float32 = 8, 24
	sunkenY, sunkenBlur float32 = 1, 2
	sunkenRing          float32 = 1 // 凹块的细线粗细，也是下拉面板边线的粗细
)

// 阴影的浓度（亮 / 暗）：内容档直接用令牌 color.shadow-a 的浓度；浮层档、凹块上沿另取。
const (
	floatAlphaLight, floatAlphaDark   float32 = .16, .5
	sunkenAlphaLight, sunkenAlphaDark float32 = .07, .25
)

// shadowInk 是所有阴影的颜色：令牌 shadow-a 的色相，浓度由 a 定（a<0 用令牌自己的浓度）。
func (k *Kit) shadowInk(a float32) ui.Color {
	c := k.T.C.ShadowA
	if a >= 0 {
		c.A = uint8(math.Round(float64(min(a, 1)) * 255))
	}
	return c
}

func (k *Kit) pick(light, dark float32) float32 {
	if k.S.Dark {
		return dark
	}
	return light
}

// Pill 是单行控件的圆角：高度的一半。多行的才用 RadiusControl。
func (g Geometry) Pill(height float32) float32 { return height / 2 }

// Inner 是同心的内层圆角：外层圆角减内距，不小于 0。
func (g Geometry) Inner(outer, inset float32) float32 { return max(outer-inset, 0) }

// pillPadding 给胶囊控件上内距：四边都是 inset（几何检查“每层四边同档，单行控件不豁免”）。
// 不必为弧线左右多留：胶囊高 44、圆角 22 时，字在竖直方向占中间约 ±8，这一段弧线离边最多收进 1.5，离边 inset 碰不到。
func (k *Kit) pillPadding(e ui.Element, inset float32) ui.Element {
	return e.Padding(inset)
}

// Sunken 是“凹下去”：凹槽底（Palette.Well，半透明，放在哪块面上都比那块面深同样多）、一圈细线（Palette.Line，悬停深一档 LineStrong）、很浅的上沿内阴影。
// 细线画在里侧（InsetShadow），不撑大控件；键盘焦点环仍是 mygo 的。悬停变化 140 毫秒淡变。
// 能打字的框用 SunkenField。
func (k *Kit) Sunken(e ui.Element) ui.Element { return k.sunken(e, false) }

// SunkenField 是能打字的“凹下去”（输入框、搜索框、多行框、可搜索的选择器、步进器）：focused 时细线换成 2 宽的焦点色，
// 鼠标点进去也看得出“正在这一格打字”（WCAG 2.4.7；以前聚焦和没聚焦一模一样，2026-11-17 组件库体检）。
// 框里的输入要关掉 mygo 自己的焦点环（FocusRing(false)），不然键盘进来时内外两圈。
func (k *Kit) SunkenField(e ui.Element, focused bool) ui.Element { return k.sunken(e, focused) }

// focusRingWidth 是输入框聚焦时那圈线的宽（和 mygo 焦点环同宽）。
const focusRingWidth float32 = 2

func (k *Kit) sunken(e ui.Element, focused bool) ui.Element {
	p := k.P
	ring, w := p.Line, sunkenRing
	switch {
	case focused:
		ring, w = k.Theme.Focus, focusRingWidth
	case e.Hovered():
		ring = k.T.C.LineStrong
	}
	return e.Background(p.Well).
		InsetShadow(0, 0, 0, w, ring).
		InsetShadow(0, sunkenY, sunkenBlur, 0, k.shadowInk(k.pick(sunkenAlphaLight, sunkenAlphaDark))).
		Transition(SelectFade)
}

// flatFill 是“平的”这一档某个状态的底：rest 是平时的底（透明或凹块色），悬停、按下各深一层。
func (k *Kit) flatFill(rest ui.Color, pressed, hovered bool) ui.Color {
	p := k.P
	if rest == ui.Transparent {
		switch {
		case pressed:
			return p.Press
		case hovered:
			return p.Hover
		}
		return rest
	}
	switch {
	case pressed:
		return p.Selected.Mix(p.Ink, .08)
	case hovered:
		return p.Selected
	}
	return rest
}

// Flat 是“平的”：只有底色，状态换底 140 毫秒淡变。调用方自己判断禁用（禁用时传 false, false）。
func (k *Kit) Flat(e ui.Element, rest ui.Color, pressed, hovered bool) ui.Element {
	return e.Background(e.AnimateColor("fill", k.flatFill(rest, pressed, hovered), ControlFade))
}

// raisedFill 是浮起的一块的底：浅色是卡片白，深色没有白块，亮一档。
func (k *Kit) raisedFill() ui.Color {
	if k.S.Dark {
		return k.P.Ink.Alpha(.11)
	}
	return k.P.Composer
}

// Raised 是“浮起来”的内容档：选中块、卡片的底加一层细阴影。
func (k *Kit) Raised(e ui.Element) ui.Element {
	return k.RaisedShadow(e.Background(k.raisedFill()))
}

// RaisedShadow 只加内容档的影子（底另有来源：玻璃、卡片自己的色）。
func (k *Kit) RaisedShadow(e ui.Element) ui.Element {
	return e.Shadow(0, raisedY, raisedBlur, 0, k.shadowInk(-1))
}

// RaisedPaint 是自己画的浮起块（分段的选中块、开关和滑块的圆钮）的影子，画在块的底下；块自己的底由调用方填。
func (k *Kit) RaisedPaint(pt *ui.Painter, r ui.Rect, radius float32) {
	pt.Shadow(r, radius, 0, raisedY, raisedBlur, 0, k.shadowInk(-1))
}

// FloatShadow 是浮层档：菜单、下拉面板、对话框、浮动右栏。比内容档深、散得开。
func (k *Kit) FloatShadow(e ui.Element) ui.Element {
	return e.Shadow(0, floatY, floatBlur, 0, k.shadowInk(k.pick(floatAlphaLight, floatAlphaDark)))
}

// Lift 是 Raised 的旧名，留给还没改名的调用方。
func (k *Kit) Lift(e ui.Element) ui.Element { return k.Raised(e) }
