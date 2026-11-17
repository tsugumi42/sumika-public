package main

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"unicode/utf8"
)

// 外轮廓12／四边8／无内层表面，基座保留IME、焦点和编辑行为。
func (a *app) textField(c *ui.Context, key string, value *string, label string) ui.Element {
	e := ui.TextInputBase(c.Key(key), value).Label(label).Height(rowHeight).Padding(fieldInset).Radius(radiusControl).Background(a.P.Bubble).FontSize(labelSize).FixedLineHeight(metaLine)
	return a.surface(c, e, key, "input", radiusControl, fieldInset)
}

// 选择项沿浮层的12／4／8推导，选项仍由SelectBase处理方向键和返回焦点。
func (a *app) choice(c *ui.Context, key, label string, value *string, items []string, name func(string) string) ui.Element {
	selectParts := ui.SelectBase(c.Key(key), value)
	e := selectParts.Trigger.Label(label).Height(rowHeight).Padding(fieldInset).Gap(spaceSmall).Radius(radiusControl).Background(a.P.Bubble)
	e.Children(func() {
		ui.Text(c, name(*value)).FontSize(labelSize).FixedLineHeight(metaLine)
		ui.Icon(c, chatui.Icon("down", false)).Size(iconSmall, iconSmall) // 照 Bun controls.tsx:193 的 down 图标（审查 D1）
	})
	a.surface(c, e, key, "choice", radiusControl, fieldInset)
	selectParts.Popup(func(panel ui.Element) {
		panel.Padding(spaceTight).Gap(spaceTight).Radius(radiusControl).Background(a.P.Composer)
		a.surface(c, panel, key+"-popup", "choice-popup", radiusControl, spaceTight)
		for _, id := range items {
			item := selectParts.Item(id).Height(hitSmall).MinWidth(hitSmall).Padding(controlInset).Radius(max(a.T.RInnerMin, radiusControl-spaceTight)) // 和外框同心：外框圆角减面板内距，内层下限同几何检查（4）
			if item.Highlighted() {
				item.Background(a.P.Selected)
			}
			item.Children(func() { ui.Text(c, name(id)).FontSize(labelSize).FixedLineHeight(metaLine) })
			a.surface(c, item, key+"-"+id, "choice-item", radiusSegment, controlInset)
		}
	})
	return e
}

// 开关照 Bun（ui.css:127-129、tokens.css:170；审查 B5）：轨道 38×22、圆钮 18、离边 2，都只按字号缩放；
// 圆钮永远白色带一点投影，关着的轨道是 Bun 的淡填充（正文色 20%），开着是强调色。
// 点击区保持 32 高：SwitchBase 本身透明，轨道和圆钮画在它上面（保留切换、键盘和读屏语义）。
func (a *app) themeSwitch(c *ui.Context) { a.switchControl(c, "dark", "深色外观", &a.Dark) }

// switchControl 是外观页的开关（深色、壁纸、玻璃共用一套）。
func (a *app) switchControl(c *ui.Context, key, label string, v *bool) {
	k := a.T.S.Text / chatui.TextDefault
	trackW, trackH, thumb, gap := 38*k, 22*k, 18*k, 2*k
	e := ui.SwitchBase(c.Key(key), v).Label(label).Size(trackW, hitSmall).Radius(min(trackW, hitSmall) / 2) // 命中区比轨道高，圆角按短的那边
	target := float32(0)
	if *v {
		target = 1
	}
	pos := e.AnimateTo("thumb", target, controlMotion) // 颜色跟这个走：缓动，不会冲过头
	x := e.Spring("thumb-x", target, a.T.Bouncy)       // 位移用回弹（ADR-285 第 6 条）；减少动态效果时 mygo 直接给终点（ui/motion.go:300）
	if a.SwitchX == nil {
		a.SwitchX = map[string]float32{}
	}
	travel := trackW - thumb - 2*gap
	a.SwitchX[key] = gap + travel*x
	off, white := a.P.Ink.Alpha(.2), ui.RGB(252, 252, 252)
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		ty := r.Y + (r.H-trackH)/2
		p.Fill(ui.Rect{X: r.X, Y: ty, W: trackW, H: trackH}, off.Mix(a.P.Accent, pos), trackH/2)
		tb := ui.Rect{X: r.X + gap + travel*x, Y: ty + gap, W: thumb, H: thumb}
		p.Shadow(tb, thumb/2, 0, 1, 2, 0, a.T.C.ShadowA)
		p.Fill(tb, white, thumb/2)
	})
	a.surface(c, e, key, "switch", hitSmall/2, 0)
}

// 内置轮廓共用24网格、2线宽；只解析一次，避免每帧生成矢量资源。
var (
	iconHome     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 10 12 3 21 10V21H15V14H9V21H3Z"/></svg>`))
	iconChat     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 11A9 9 0 0 1 8 20L3 21 4 16A9 9 0 1 1 21 11Z"/><path d="M8 10H16M8 14H13"/></svg>`))
	iconSettings = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 6H20M4 12H20M4 18H20"/><circle cx="8" cy="6" r="2"/><circle cx="16" cy="12" r="2"/><circle cx="10" cy="18" r="2"/></svg>`))
	iconSide     = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M15 4V20"/></svg>`))
	iconScene    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="3"/><path d="m10 8 6 4-6 4Z"/></svg>`))
	iconRetry    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 7V12M12 16V16.1"/></svg>`))
	iconClose    = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 6 18 18M18 6 6 18"/></svg>`))
)

func (a *app) avatar(c *ui.Context, key, name, actor string) {
	a.avatarSized(c, key, name, actor, avatarSize)
}

// avatarSized 画指定尺寸的头像；首字字号照 Telegram 取 尺寸 × chatui.AvatarInitial（13/33），不小于 labelSize。
func (a *app) avatarSized(c *ui.Context, key, name, actor string, size float32) {
	index := 0
	switch actor {
	case "r2", "r7":
		index = 1
	case "r3", "r8":
		index = 2
	case "r4", "r9":
		index = 3
	case "r5", "r10":
		index = 4
	}
	_, n := utf8.DecodeRuneInString(name)
	initial := name[:n]
	e := ui.Box(c.Key("avatar-"+key)).Label("头像："+key).Size(size, size).Shrink(0).Radius(size / 2).Center().Background(a.P.Avatar[index]).TextColor(a.P.AvatarInk[index])
	font := max(labelSize, size*chatui.AvatarInitial)
	e.Children(func() { ui.Text(c, initial).FontSize(font).FixedLineHeight(font * labelLine / labelSize) })
	a.surface(c, e, "avatar-"+key, "avatar", size/2, 0)
}

func (a *app) sideRow(c *ui.Context, key string, children func()) {
	e := ui.Row(c.Key(key)).FillWidth().MinHeight(avatarSize).Gap(spaceSmall).Children(children)
	a.surface(c, e, key, "side-row", 0, 0)
}

func (a *app) iconButton(c *ui.Context, key, label string, icon *ui.SVG, action func()) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(label).Size(hitSmall, hitSmall).Center().Radius(hitSmall / 2).TextColor(a.P.Ink).OnClick(action)
	bg := ui.Transparent
	if b.Pressed() {
		bg = a.P.Press
	} else if b.Hovered() {
		bg = a.P.Hover
	}
	b.Background(b.AnimateColor("fill", bg, controlMotion)).Children(func() {
		size := iconSize
		if icon == iconRetry {
			size = retryInk
		}
		ui.Icon(c, icon).Size(size, size)
	})
	return b
}
func (a *app) sceneEntry(c *ui.Context) ui.Element {
	return a.iconButton(c, "scene-open", "场景", iconScene, func() { a.Scenes = !a.Scenes })
}
func (a *app) closeControl(c *ui.Context) {
	if a.Close != nil {
		a.iconButton(c, "window-close", "关闭窗口", iconClose, a.Close)
	}
}
