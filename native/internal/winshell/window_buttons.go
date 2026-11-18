package winshell

import "github.com/egoist/mygo/ui"

// WindowControls 是自己画的最小化、最大化、关闭要做的事（主人 2026-11-09：系统的三个键太突兀，还压着后面的东西）。
// 宿主有真窗口才给；nil 时不画、也不占位（测试和预览）。
type WindowControls struct {
	Maximized               bool
	Minimize, Toggle, Close func()
}

var (
	iconMinimize = icon(`<path d="M6 12h12"/>`)
	iconMaximize = icon(`<rect x="6" y="6" width="12" height="12" rx="2"/>`)
	iconClose    = icon(`<path d="M6 6 18 18M18 6 6 18"/>`)
	iconRestore  = icon(`<rect x="5" y="9" width="10" height="10" rx="2"/><path d="M9 9V7a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v6a2 2 0 0 1-2 2h-2"/>`)
)

// windowInset 是聊天区里的浮层（标题条、输入框、窗口按钮胶囊）离窗口和聊天区边的距离，四边一样（ADR-326）。
// 右栏开关、换页时三个键的位置都不动；窄窗口地方小，少一个缝。
func (k *Kit) windowInset(narrow bool) float32 {
	if narrow {
		return k.G.SpaceSmall
	}
	return 2 * k.G.SpaceSmall
}

func (k *Kit) windowButtonsWidth() float32 {
	return 3*k.G.HitSmall + 2*k.G.SpaceMicro
}

// windowCapsulePad 是胶囊里三个键四周的距离：胶囊和聊天标题条同高。
func (k *Kit) windowCapsulePad() float32 { return (k.G.BarHeight - k.G.HitSmall) / 2 }

// windowRoom 是顶行内容右边要让出的宽度：胶囊加一个缝。没有窗口按钮时是 0。
func (k *Kit) windowRoom() float32 {
	if k.Win == nil {
		return 0
	}
	return k.windowButtonsWidth() + 2*k.windowCapsulePad() + k.G.SpaceSmall
}

// WindowButtons 只画三个键，放进已有的顶行（右栏、窄窗口的列表）。
func (k *Kit) WindowButtons(c *ui.Context) {
	if k.Win == nil {
		return
	}
	g, w := k.G, k.Win
	e := ui.Row(c.Key("window-buttons")).Shrink(0).Gap(g.SpaceMicro).AlignItems(ui.Center)
	k.surface(c, e, "window-buttons", "window-buttons", g.HitSmall/2, 0).Children(func() {
		k.Tip(c, k.windowButton(c, "window-minimize", "最小化", iconMinimize, false, w.Minimize), "最小化")
		label, glyph := "最大化", iconMaximize
		if w.Maximized {
			label, glyph = "还原", iconRestore
		}
		// 不加悬浮提示：Windows 11 停在这个键上会弹贴靠布局，两样叠在一起
		k.windowButton(c, "window-maximize", label, glyph, false, w.Toggle).MaximizeButton()
		k.Tip(c, k.windowButton(c, "window-close", "关闭", iconClose, true, w.Close), "关闭")
	})
}

// windowCapsule 是浮在顶行右端的胶囊：和聊天标题条同高、同材质，平级摆在它右边。调用方给它让位（windowRoom）。
// 返回胶囊本身（没有窗口按钮时是零元素）：浮动面板要认“点在胶囊上不算点外面”。
func (k *Kit) windowCapsule(c *ui.Context, inset float32) ui.Element {
	if k.Win == nil {
		return ui.Element{}
	}
	g, p := k.G, k.P
	width := k.windowButtonsWidth() + 2*k.windowCapsulePad()
	fill := ui.Box(c.Key("window-capsule-fill")).Absolute().Right(inset).Top(inset).Size(width, g.BarHeight).Radius(g.RadiusBar)
	k.Glass(fill, LevelHeader, p.Canvas.Alpha(OverlayAlpha))
	k.RaisedShadow(fill)
	e := ui.Row(c.Key("window-capsule")).Absolute().Right(inset).Top(inset).Size(width, g.BarHeight).Center().DragWindow()
	k.surface(c, e, "window-capsule", "header", g.RadiusBar, k.windowCapsulePad()).Children(func() { k.WindowButtons(c) })
	return e
}

func (k *Kit) windowButton(c *ui.Context, key, label string, glyph *ui.SVG, danger bool, fn func()) ui.Element {
	g, p := k.G, k.P
	b := ui.ButtonBase(c.Key(key)).Label(label).Size(g.HitSmall, g.HitSmall).Center().Radius(g.HitSmall / 2).OnClick(fn)
	bg, ink := ui.Transparent, p.Ink
	switch {
	case danger && (b.Hovered() || b.Pressed()):
		bg, ink = p.Danger, p.OnAccent
	case b.Pressed():
		bg = p.Press
	case b.Hovered():
		bg = p.Hover
	}
	b.TextColor(ink).Background(b.AnimateColor("fill", bg, ControlFade)).Children(func() { ui.Icon(c, glyph).Size(g.IconSize, g.IconSize) })
	return k.surface(c, b, key, "icon-button", g.HitSmall/2, 0)
}
