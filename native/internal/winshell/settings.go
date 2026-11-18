package winshell

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// Category 是设置页左栏里的一个分类。
type Category struct {
	ID, Label string
	Icon      *ui.SVG
}

// SettingsSpec 是设置页要的东西。布局照 Codex、Claude 桌面版（主人 2026-11-05 定）并复用聊天页的结构：
// 左栏是分类（顶上一行搜索设置，下面一组“通用”分类行），右边是内容（最宽 756 居中）；窄窗口分两层，
// 先看到分类，点进去看内容，有返回钮。内容由宿主画。
type SettingsSpec struct {
	Group      string // 分类的组名，灰色小字
	Categories []Category
	Selected   string
	OnSelect   func(id string)
	Search     *string // 搜索设置：按分类名过滤
	Title      string  // 内容顶上的标题（当前分类名）
	Content    func()
}

// settings 是设置页的整体：导航 | 分类 | 内容（宽窗口并排，窄窗口两层）。
func (k *Kit) settings(c *ui.Context, s *State, sp *Spec, w float32) {
	g := k.G
	st := sp.Settings
	if k.Mode(w) != Narrow {
		k.Plan(s, w, true)
		ui.Row(c.Key("columns")).Fill().AlignItems(ui.Stretch).Children(func() {
			k.nav(c, s, sp)
			ui.Box(c.Key("settings-categories")).Width(s.ListWidth).Shrink(0).FillHeight().Children(func() { k.categories(c, s, st, false) })
			ui.Box(c.Key("settings-main")).Grow(1).MinWidth(0).FillHeight().Children(func() { k.settingsMain(c, s, st, false) })
		})
		return
	}
	if !s.layerSeen {
		s.layerShown, s.layerSeen = s.Layer, true
	}
	if s.Layer != s.layerShown {
		s.layerDir = 1
		if s.Layer < s.layerShown {
			s.layerDir = -1
		}
		s.layerShown = s.Layer
	}
	layer := ui.Box(c.Key("settings-layer-" + fmt.Sprint(int(min(s.Layer, LayerChat))))).Fill()
	if s.layerDir != 0 {
		layer.Transition(ui.ElementTransition{Animation: &RouteMotion, Enter: &ui.Motion{X: s.layerDir * g.SpaceLarge, Opacity: 0}})
	}
	layer.Children(func() {
		if s.Layer == LayerList {
			ui.Row(c.Key("list-layer")).Fill().AlignItems(ui.Stretch).Children(func() {
				k.nav(c, s, sp)
				ui.Box(c).Grow(1).MinWidth(0).FillHeight().Children(func() { k.categories(c, s, st, true) })
			})
			return
		}
		k.settingsMain(c, s, st, true)
	})
}

// categories 是左栏：搜索设置、组名、分类行（图标 + 文字，左对齐，选中是浅灰底圆角）。
func (k *Kit) categories(c *ui.Context, s *State, st *SettingsSpec, narrow bool) {
	g, p := k.G, k.P
	k.surface(c, ui.Column(c.Key("sidebar")).Fill().Background(p.Pane), "sidebar", "pane", 0, g.SpacePanel).Children(func() {
		ui.Row(c.Key("search-row")).Height(g.BarHeight).Shrink(0).Margin(2*g.SpaceSmall, g.ListInset, g.SpaceSmall, g.ListInset).Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
			if narrow { // 窄窗口这一层是最右一栏：窗口按钮排在搜索框后面，不压着它
				defer k.WindowButtons(c)
			}
			if st.Search != nil {
				k.searchShaped(c, "search", st.Search, "搜索设置", &s.SearchField, g.SearchHeight, g.SearchInset).Grow(1)
			}
		})
		ui.Column(c.Key("sidebar-tools")).FillWidth().Padding(g.ListInset).Gap(g.SpaceTight).Children(func() {
			if st.Group != "" {
				ui.Text(c, st.Group).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).Padding(0, g.SpaceMedium)
			}
			query := ""
			if st.Search != nil {
				query = strings.TrimSpace(*st.Search)
			}
			k.categoryRows(c, "settings", st.Categories, st.Selected, query, func(id string) {
				s.OpenChat()
				st.OnSelect(id)
			})
		})
	})
}

// settingsMain 是右边内容：56 高的标题行 + 滚动区，内容最宽 756 居中。back 为真（窄窗口）时标题行左边有“返回分类”。
func (k *Kit) settingsMain(c *ui.Context, s *State, st *SettingsSpec, back bool) {
	g := k.G
	ui.Column(c.Key("settings")).Fill().Children(func() {
		// 顶行和聊天标题条同高同位，右边让给窗口按钮胶囊，换页时三个键不动
		inset, right := k.windowInset(back), g.PaneInset
		if k.Win != nil {
			right = k.windowInset(back) + k.windowRoom()
		}
		ui.Row(c.Key("settings-header")).FillWidth().Height(inset+g.BarHeight).Shrink(0).Padding(inset, right, 0, g.PaneInset).Gap(g.SpaceSmall).AlignItems(ui.Center).DragWindow().Children(func() {
			if back {
				k.Button(c, "settings-back", "返回分类", false, false, s.ShowList)
			}
			ui.Text(c.Key("settings-title"), st.Title).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).FontWeight(g.StrongWeight).Grow(1)
		})
		// 换分类：内容 200 毫秒淡入，标题行不动；滚动区按分类起 key，新分类从顶上看起
		jump := s.fade.catSeen && s.fade.cat != st.Selected || s.fade.jump
		s.fade.cat, s.fade.catSeen = st.Selected, true
		ui.Scroll(c.Key("settings-content-" + st.Selected)).Grow(1).MinHeight(0).Padding(g.PaneInset).Children(func() {
			page := ui.Column(c.Key("settings-page")).FillWidth().MaxWidth(SettingsWidth).Margin(0, ui.Auto).Gap(g.PaneInset)
			k.fadeIn(page, jump)
			page.Children(func() {
				if st.Content != nil {
					st.Content()
				}
			})
		})
		k.windowCapsule(c, inset)
	})
}

// dropdownRows 是下拉面板不滚动时最多放几行。
const dropdownRows = 10

// dropdownTrigger 画下拉的入口，返回它的句柄，面板用它量宽度。
// veil 小于 1 时入口整个淡掉（展开的面板正在接手它的位置），turn 是箭头转过的角度。
func (k *Kit) dropdownTrigger(c *ui.Context, key, label, value string, m ui.MenuParts, veil, turn float32) *ui.Handle {
	g := k.G
	if k.anchors == nil {
		k.anchors = map[string]*ui.Handle{}
	}
	anchor := k.anchors[key]
	if anchor == nil {
		anchor = &ui.Handle{}
		k.anchors[key] = anchor
	}
	// 入口是单行：胶囊，“凹下去”；面板的边线和它同色同粗（FloatPanel 用 Palette.Line 1）
	trigger := m.Trigger.Label(label).Height(g.RowHeight).Gap(g.SpaceSmall).Radius(g.Pill(g.RowHeight)).AlignItems(ui.Center).Shrink(1).MinWidth(0).Bind(anchor)
	k.pillPadding(trigger, g.FieldInset)
	k.Sunken(trigger)
	if veil < 1 {
		trigger.Opacity(max(veil, 0))
	}
	trigger.Children(func() {
		ui.Text(c, value).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine().Grow(1).MinWidth(0)
		ui.Icon(c, chatui.Icon("down", false)).Size(g.IconSmall, g.IconSmall).Rotate(turn)
	})
	k.surface(c, trigger, key, "choice", g.Pill(g.RowHeight), g.FieldInset)
	return anchor
}

// dropdownPanel 是下拉面板的外壳：和入口同宽（主人 2026-11-09）；会滚动时右边留出滚动条的位置，不压着选项。
func (k *Kit) dropdownPanel(c *ui.Context, panel ui.Element, anchor *ui.Handle, scrolls bool) {
	g := k.G
	right := g.SpaceTight
	if scrolls {
		right += g.SpaceSmall
	}
	panel.Padding(g.SpaceTight, right, g.SpaceTight, g.SpaceTight).Radius(g.RadiusControl)
	k.FloatMenu(c, panel)
	if w := anchor.Bounds(c).W; w > 0 {
		panel.Width(w)
	}
}

// Picker 是带搜索的分组下拉（选模型这种选项多的）：入口和普通下拉一样，点开顶上是搜索框，下面按组列出来。
func (k *Kit) Picker(c *ui.Context, key, label, value string, open *bool, query *string, hint string, items []MenuItem) {
	g, p := k.G, k.P
	m := ui.MenuBase(c.Key(key), open)
	anchor := k.dropdownTrigger(c, key, label, value, m, 1, 0)
	m.Trigger.Width(g.ControlWide) // 选项的名字长，入口和面板都定宽
	if k.searches == nil {
		k.searches = map[string]*ui.Handle{}
	}
	search := k.searches[key]
	if search == nil {
		search = &ui.Handle{}
		k.searches[key] = search
	}
	if !*open {
		*query = ""
		return
	}
	search.Focus(c)
	m.Popup(func(panel ui.Element) {
		shown := filterMenuItems(items, *query)
		k.dropdownPanel(c, panel, anchor, false)
		panel.Gap(g.SpaceTight)
		k.surface(c, panel, key+"-popup", "choice-popup", g.RadiusControl, g.SpaceTight)
		k.searchShaped(c, key+"-search", query, hint, search, g.RowHeight, g.FieldInset).FillWidth().Shrink(0)
		list := ui.Scroll(c.Key(key + "-list")).FillWidth().MaxHeight(g.HitSmall * dropdownRows).Gap(g.SpaceTight)
		list.Children(func() {
			if len(shown) == 0 {
				ui.Text(c.Key(key+"-empty"), "没有匹配的选项").FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Padding(g.ControlInset)
			}
			k.menuItems(c, m, key, shown)
		})
	})
}

// filterMenuItems 按名字和分组筛，不分大小写。
func filterMenuItems(items []MenuItem, query string) []MenuItem {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	var out []MenuItem
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Label), q) || strings.Contains(strings.ToLower(it.Group), q) {
			out = append(out, it)
		}
	}
	return out
}

// Row 是设置页的一行：左边名字（可带一行说明），右边是控件。
// 行高至少 RowHeight，和 ValueRow 一样：开关、下拉、分段、值行放在同一页，行与行之间的留白只有一种；
// 页面不再自己在外面包一层上下内距（主人 2026-11-11 审查第 8 条）。
func (k *Kit) Row(c *ui.Context, key, label, hint string, control func()) {
	g, p := k.G, k.P
	ui.Row(c.Key(key)).FillWidth().MinHeight(g.RowHeight).Gap(g.SpacePanel).AlignItems(ui.Center).Children(func() {
		ui.Column(c.Key("label")).Grow(1).MinWidth(0).Gap(g.SpaceMicro).Children(func() {
			ui.Text(c, label).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine)
			if hint != "" {
				ui.Text(c, hint).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
			}
		})
		// 控件包一层、按自己的宽度排、不抢左边的字：勾选框（checkLook）是给对话框竖排用的 FillWidth，
		// 直接放进这一行会把剩下的宽度全占掉，左边说明被挤成一字一行（2026-11-11 主人截图：放行规则“作用范围”那一行）
		ui.Row(c.Key("ctl")).Shrink(0).Gap(g.SpaceSmall).AlignItems(ui.Center).Children(control)
	})
}

// ValueRow 是设置页“名字 + 当前值 + 箭头”的一行：点开编辑。没有动作或 disabled 时只显示。
// 字和 Row 的字左边对齐；悬停的底比字宽出一圈。
func (k *Kit) ValueRow(c *ui.Context, title, value string, arrow, disabled bool, act func()) {
	g, p := k.G, k.P
	active := act != nil && !disabled
	e := ui.ButtonBase(c).Label(title).MinHeight(g.RowHeight).Margin(0, -g.FieldInset).Padding(0, g.FieldInset).Gap(g.SpacePanel).AlignItems(ui.Center).Radius(g.RadiusSegment)
	ink := p.Ink
	if active {
		e.OnClick(act)
		bg := ui.Transparent
		if e.Pressed() {
			bg = p.Press
		} else if e.Hovered() {
			bg = p.Hover
		}
		e.Background(e.AnimateColor("fill", bg, ControlFade)) // 悬停、按下 140 毫秒淡变，和外壳按钮同一族
	} else {
		e.Disabled(true)
		ink = p.Muted
	}
	e.Children(func() {
		ui.Text(c.Key("title"), title).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(ink).Grow(1).MinWidth(0)
		if value != "" {
			ui.Text(c.Key("value"), value).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).SingleLine().Shrink(1).MinWidth(0)
		}
		if arrow && active {
			ui.Icon(c.Key("arrow"), chatui.Icon("right", false)).Size(g.IconSmall, g.IconSmall).Shrink(0).TextColor(p.Meta)
		}
	})
}

// DisclosureRow 是可以展开的一行：左边整块是按钮（箭头、名字、一句摘要），点一下展开或收起；右边 control 放开关、状态徽标。
// 箭头转 90° 表示展开。indent 把整行缩进一级，画嵌套的下一层；onToggle 为 nil 时不能点、不画箭头（只列出来写明）。
func (k *Kit) DisclosureRow(c *ui.Context, key, title, summary string, open, indent bool, onToggle func(), control func()) {
	g, p := k.G, k.P
	inset := float32(0)
	if indent {
		inset = g.IconSmall + g.SpaceSmall
	}
	ui.Row(c.Key(key)).FillWidth().Gap(g.SpacePanel).AlignItems(ui.Center).Padding(0, 0, 0, inset).Children(func() {
		head := ui.ButtonBase(c.Key("head")).Label(title).Grow(1).MinWidth(0).MinHeight(g.RowHeight).Margin(0, 0, 0, -g.FieldInset).Padding(0, g.FieldInset).Gap(g.SpaceSmall).AlignItems(ui.Center).Justify(ui.Start).Radius(g.RadiusSegment)
		ink := p.Ink
		if onToggle != nil {
			head.OnClick(onToggle)
			bg := ui.Transparent
			if head.Pressed() {
				bg = p.Press
			} else if head.Hovered() {
				bg = p.Hover
			}
			head.Background(head.AnimateColor("fill", bg, ControlFade))
		} else {
			head.Disabled(true)
			ink = p.Muted
		}
		head.Children(func() {
			if onToggle != nil {
				deg := float32(0)
				if open {
					deg = 90
				}
				ui.Icon(c.Key("chevron"), chatui.Icon("right", false)).Size(g.IconSmall, g.IconSmall).Shrink(0).TextColor(p.Meta).Rotate(head.AnimateTo("chevron", deg, ControlMotion))
			} else {
				ui.Box(c.Key("chevron-gap")).Size(g.IconSmall, g.IconSmall).Shrink(0)
			}
			ui.Text(c.Key("title"), title).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(ink).Shrink(0)
			if summary != "" {
				ui.Text(c.Key("summary"), summary).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).SingleLine().Shrink(1).MinWidth(0).Grow(1)
			}
		})
		if control != nil {
			control()
		}
	})
}

// Disclose 画一块能展开收起的内容（DisclosureRow、SettingCollapsed 下面那一块；主人 2026-11-15“设置页折叠”）：
// 展开时高度用“利落”弹簧从 0 长到内容本来的高度、内容淡入；收起反过来，收完才不画。里面的内容一直按全高排好，只是外框露出来的高度在变，字不挤。
// 第一帧还量不到内容多高：那一帧高 0、再要一帧。减少动态效果时高度直接到位，淡变照常。
// key 在同一个父元素里要唯一；open 是现在该不该展开。gap 是外面那一列的行间距（里面的行也按它排）：
// 高度还接近 0 时上外边距是负的 gap，抵掉父列表多出来的那道间距，展开开始、收起结束都不跳；父列表没有间距就传 0。
func (k *Kit) Disclose(c *ui.Context, key string, open bool, gap float32, body func()) {
	if k.discs == nil {
		k.discs = map[string]*discState{}
	}
	st := k.discs[key]
	if st == nil {
		st = &discState{}
		k.discs[key] = st
	}
	if !open && !st.shown {
		delete(k.discs, key)
		return
	}
	outer := ui.Column(c.Key(key)).FillWidth().Shrink(0)
	if !st.shown { // 刚打开：两根通道都从 0 起（mygo 的通道第一次就是目标值）
		st.shown = true
		outer.Jump("disc-h", 0)
		outer.Jump("disc-o", 0)
	}
	natural := st.inner.Bounds(c).H
	target, o := float32(0), float32(0)
	if open {
		target, o = natural, 1
	}
	h := max(0, outer.AnimateTo("disc-h", target, ui.Animation{Spring: k.firm()}))
	op := max(0, min(1, outer.AnimateTo("disc-o", o, ControlFade)))
	if open && natural == 0 {
		c.AnimationFrame() // 还没量到内容的高：下一帧再定目标
	}
	if !open && h <= .5 {
		st.shown = false // 收完了：这一帧还画一个 0 高的空框，下一帧整块拿掉
	}
	if h-natural > .5 || natural-h > .5 || !open {
		outer.Height(h).ClipY()
		if gap > 0 {
			frac := float32(0)
			if natural > 0 {
				frac = max(0, min(1, h/natural))
			}
			outer.Margin(-gap*(1-frac), 0, 0, 0)
		}
	}
	if op < 1 {
		outer.Opacity(op)
	}
	outer.Children(func() {
		ui.Column(c.Key("disc-body")).FillWidth().Shrink(0).Gap(gap).Bind(&st.inner).Children(body)
	})
}

// discState 是一块 Disclose 的记忆：里面那一列（量它本来多高），和它现在是不是还画着。
type discState struct {
	inner ui.Handle
	shown bool
}

// SectionHeading 是设置页一节的小标题。
func (k *Kit) SectionHeading(c *ui.Context, title string) {
	g := k.G
	ui.Text(c, title).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).FontWeight(g.StrongWeight).Padding(g.SpaceSmall, 0, g.SpaceTight, 0)
}

// Switch 是开关（原型 switchControl，照 Bun ui.css:127-129）：轨道 38×22、圆钮 18、离边 2，都只按字号缩放；
// 圆钮永远白色带一点投影，关着的轨道是正文色 20%，开着是强调色。点击区保持 32 高。
func (k *Kit) Switch(c *ui.Context, key, label string, v *bool) {
	k.switchLook(c, key, label, v)
}

// SwitchValue 是值由外面管的开关（要提交到后台的设置）：点了调 onChange，值等后台确认后再变。disabled 时点不了。
func (k *Kit) SwitchValue(c *ui.Context, key, label string, value, disabled bool, onChange func(bool)) {
	v := value
	e := k.switchLook(c, key, label, &v)
	if disabled {
		e.Disabled(true).Opacity(.5)
		return
	}
	e.OnClick(func() { onChange(v) })
}

func (k *Kit) switchLook(c *ui.Context, key, label string, v *bool) ui.Element {
	g, p := k.G, k.P
	scale := k.T.S.Text / chatui.TextDefault
	trackW, trackH, thumb, gap := 38*scale, 22*scale, 18*scale, 2*scale
	e := ui.SwitchBase(c.Key(key), v).Label(label).Size(trackW, g.HitSmall).Shrink(0).Radius(g.HitSmall / 2)
	target := float32(0)
	if *v {
		target = 1
	}
	pos := e.AnimateTo("thumb", target, ControlFade) // 轨道颜色的混合量，不是位移：带 NonMotion，减少动态效果时也淡变（位移是下面的 thumb-x 弹簧）
	x := e.Spring("thumb-x", target, k.snappy())     // 位移回弹，“灵敏”预设（ADR-285 第 6 条）；减少动态效果时直接给终点
	travel := trackW - thumb - 2*gap
	// 滑钮按速度挤压（Fluid SpringSwitch）：通道速度是“每秒走全程的几分之几”，乘全程换成像素每秒；滑钮以自己的中心横向拉长、纵向压扁。
	// 减少动态效果时弹簧直接到终点、速度为 0，这里再明说一遍不挤。
	stretch := float32(0)
	if !c.Preferences().ReduceMotion {
		stretch = thumbStretch(e.Velocity("thumb-x") * travel * float32(motionScale)) // 慢放时速度被拉慢了，乘回去：慢放只是把时间拉长，挤多少不变
	}
	// 关着的轨道用控件边界色（Palette.Edge）：原来是正文色 20%，对底只有约 1.2:1，看不出是个开关（WCAG 1.4.11，2026-11-17 体检）。
	// 打开的仍是角色色实底（设计语言第十三节：主人 10-14 定的有意不加深）。
	off, white := p.Edge, ui.RGB(252, 252, 252)
	e.Draw(func(pt *ui.Painter, r ui.Rect) {
		ty := r.Y + (r.H-trackH)/2
		pt.Fill(ui.Rect{X: r.X, Y: ty, W: trackW, H: trackH}, off.Mix(p.Accent, pos), trackH/2)
		tb := ui.Rect{X: r.X + gap + travel*x, Y: ty + gap, W: thumb, H: thumb}
		if stretch != 0 {
			cx, cy := tb.X+tb.W/2, tb.Y+tb.H/2
			tb.W, tb.H = thumb*(1+stretch), thumb*(1-stretch*thumbSquash)
			tb.X, tb.Y = cx-tb.W/2, cy-tb.H/2
		}
		rad := min(tb.W, tb.H) / 2
		k.RaisedPaint(pt, tb, rad) // 圆钮是“浮起来”（内容档）
		pt.Fill(tb, white, rad)
	})
	k.surface(c, e, key, "switch", g.HitSmall/2, 0)
	return e
}

// 滑钮挤压的三个数（Fluid SpringSwitch）：像素每秒 × 0.0016 = 横向拉长的比例，封顶 0.45；纵向压扁它的 0.4 倍。
const (
	thumbStretchGain float32 = .0016
	thumbStretchMax  float32 = .45
	thumbSquash      float32 = .4
)

// thumbStretch 是滑钮速度（像素每秒，带不带正负都行）换成的横向拉长比例：停下时是 0，滑钮是正圆。
func thumbStretch(v float32) float32 {
	if v < 0 {
		v = -v
	}
	return min(thumbStretchMax, v*thumbStretchGain)
}

// Slider 是自己画的滑块（原型 slider）：高 = 小控件高，圆钮直径 = 高 − 2×嵌套内边距，四边内边距都是半个圆钮宽；
// 轨道左段强调色、右段凹块色，圆钮白色加轻阴影。
func (k *Kit) Slider(c *ui.Context, key string, val *float64, lo, hi, step float32, label string, ticks bool) ui.Element {
	g, t := k.G, k.T
	h := t.HSm
	kw := h - 2*t.InsetCtl
	width := g.ControlWide
	if ticks {
		// 带刻度的滑块（字号）自己的尺寸不跟字号走：拖它的时候界面实时变字号，它自己不能跟着变宽变高、从手底下跑掉
		h, kw, width = 32, 32-2*8, 280
	}
	s := ui.SliderBase(c.Key(key), val, float64(lo), float64(hi)).Step(float64(step)).Label(label).Width(width).Shrink(1).MinWidth(g.HitRegular * 2).Height(h).Padding(kw / 2).Radius(h / 2)
	acc := t.AccentOf(chatui.DefaultChar)
	s.Draw(func(pt *ui.Painter, r ui.Rect) {
		frac := float32((*val - float64(lo)) / float64(hi-lo))
		track := 6 * t.S.Text / chatui.TextDefault
		x0, x1 := r.X+kw/2, r.X+r.W-kw/2
		cy := r.Y + r.H/2
		x := x0 + (x1-x0)*frac
		// 没填满的那段和滑钮的描边用控件边界色（Palette.Edge）：原来借凹块底，对底只有约 1.2:1，看不出还能拖多远（WCAG 1.4.11，2026-11-17 体检）
		pt.Fill(ui.Rect{X: x0 - track/2, Y: cy - track/2, W: x1 - x0 + track, H: track}, k.P.Edge, track/2)
		pt.Fill(ui.Rect{X: x0 - track/2, Y: cy - track/2, W: x - x0 + track, H: track}, acc.Base, track/2)
		if ticks && step > 0 { // 刻度：每一档一个小点，在轨道上；圆钮盖住当前那一档
			n := int(math.Round(float64((hi - lo) / step)))
			for i := 0; i <= n; i++ {
				tx := x0 + (x1-x0)*float32(i)/float32(n)
				pt.Fill(ui.Rect{X: tx - track/4, Y: cy + track, W: track / 2, H: track}, t.C.Text3, track/4)
			}
		}
		k.RaisedPaint(pt, ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, kw/2)
		pt.Fill(ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, ui.RGB(252, 252, 252), kw/2)
		pt.Stroke(ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, k.P.Edge, kw/2, 1)
	})
	k.surface(c, s, key, "slider", h/2, kw/2)
	return s
}

// SliderRow 是一行滑块：名字、当前值、滑块；值是 float32，滑块要 float64，来回转。返回是否改了。
func (k *Kit) SliderRow(c *ui.Context, key, label string, v *float32, lo, hi, step float32) (changed bool) {
	changed, _ = k.SliderRowTicks(c, key, label, v, lo, hi, step, false)
	return changed
}

// SliderRowTicks 同 SliderRow，可以画刻度，并说现在是不是正按着滑块拖（调用方据此决定松手才写盘）。
func (k *Kit) SliderRowTicks(c *ui.Context, key, label string, v *float32, lo, hi, step float32, ticks bool) (changed, dragging bool) {
	g, p := k.G, k.P
	val := float64(*v)
	ui.Row(c.Key(key)).FillWidth().Gap(g.SpacePanel).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Grow(1).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine)
		ui.Text(c, fmt.Sprintf("%g", *v)).FontSize(g.LabelSize).FontFeatures(chatui.Tabular).TextColor(p.Muted).MinWidth(g.ValueWidth)
		_, dragging = k.Slider(c, key+"-slider", &val, lo, hi, step, label, ticks).PressedButton() // 按着期间一直算，指针拖出滑块也算
	})
	if float32(val) != *v {
		*v = float32(val)
		c.AnimationFrame()
		return true, dragging
	}
	return false, dragging
}

// Preview 是外观页的气泡预览：她的两条（合组）、你的一条带表情回应、你的一条发送失败，跟着设置实时变。
// 预览框的内距等于圆角（都取浮层面板那一档）：气泡离卡片边至少一个圆角半径，永远进不了圆角区。
func (k *Kit) Preview(c *ui.Context) {
	g := k.G
	a1 := chatui.CharColors[0]
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	msgs := []*chatui.Msg{
		{ID: "pv1", Sender: "r1", Name: "晴枝", Char: a1, At: at, Text: "窗外的风停了，纸灯还亮着。"},
		{ID: "pv2", Sender: "r1", Name: "晴枝", Char: a1, At: at.Add(10 * time.Second), Text: "要不要一起把今天的事慢慢说一遍？说到哪里都可以。"},
		{ID: "pv3", Sender: "self", Self: true, At: at.Add(30 * time.Second), Text: "好呀。", Reactions: []chatui.Reaction{{Emoji: "❤", Count: 1, By: []chatui.Reactor{{Name: "晴枝", Char: a1}}}}},
		{ID: "pv4", Sender: "self", Self: true, At: at.Add(40 * time.Second), Text: "这是一条发送失败的消息。", State: chatui.Failed},
	}
	w, _ := c.Size()
	inset := g.RadiusPanel
	column := min(SettingsWidth, w-2*g.PaneInset) - 2*inset
	host := chatui.Host{T: k.T, Font: k.Font, SelfChar: a1, Column: column, NoEnter: true}
	box := ui.Column(c.Key("look-preview")).FillWidth().Padding(inset).Radius(inset).Clip().Background(k.T.C.ChatBg)
	if k.S.Wallpaper {
		box.Draw(func(pt *ui.Painter, r ui.Rect) {
			pt.Clip(r, inset, func() { k.PreviewWall.Paint(pt, r, k.T, a1) })
		})
	}
	box.Children(func() {
		for i, m := range msgs {
			var prev, next *chatui.Msg
			if i > 0 {
				prev = msgs[i-1]
			}
			if i+1 < len(msgs) {
				next = msgs[i+1]
			}
			host.Message(c, m, prev, next)
		}
	})
}

// DropdownHeight 是 n 个选项的下拉展开后的高度（入口那一行加选项区，最多放 dropdownRows 行，再多在里面滚动）。
// 页面要给展开的下拉留位置、测试量终点，都用它。
func (k *Kit) DropdownHeight(n int) float32 {
	g := k.G
	rows := min(n, dropdownRows)
	if rows <= 0 {
		return g.RowHeight
	}
	return g.RowHeight + g.SpaceTight + float32(rows)*g.HitSmall + float32(rows-1)*g.SpaceTight + g.SpaceTight
}

// Dropdown 是下拉选择：收起时是一个凹下去的胶囊入口，展开时是这个入口自己往下长（下面放不下就往上长）。
// 长出来的面板顶上的一行就是入口（当前选项加箭头，箭头转 180°），上、左、右三条边和入口重合；
// 圆角一直是入口的那个数（高度的一半），选项行圆角是它减内距（同心）；面板是浮层档的玻璃和阴影，收回去后又变回凹下去的入口。
// items 是选项文字，current 是当前选中的下标，选了别的调 onChoose。open 由调用方持有（每个下拉一份）。
func (k *Kit) Dropdown(c *ui.Context, key, label string, open *bool, current int, items []string, onChoose func(i int), itemFont func(i int) string) ui.Element {
	g, p := k.G, k.P
	m := ui.MenuBase(c.Key(key), open)
	cur := ""
	if current >= 0 && current < len(items) {
		cur = items[current]
	}
	// 入口收起时的高、面板长到位的高：选项最多放 dropdownRows 行，再多就在面板里滚动
	rowH, radius := g.RowHeight, g.Pill(g.RowHeight)
	rows := min(len(items), dropdownRows)
	listH := float32(rows)*g.HitSmall + float32(max(rows-1, 0))*g.SpaceTight
	scrolls := len(items) > dropdownRows
	fullH := k.DropdownHeight(len(items))
	// 高度和箭头都挂在入口上（它一直在），关上以后收回去的那一段也接得上
	target, anim, deg := rowH, DropdownShrink, float32(0)
	if *open {
		target, anim, deg = fullH, DropdownGrow, 180
	}
	h := m.Trigger.AnimateTo("grow", target, anim)
	turn := m.Trigger.AnimateTo("turn", deg, anim)
	grown := float32(1) // 长了几成
	if fullH-rowH > .5 {
		grown = max(0, min((h-rowH)/(fullH-rowH), 1))
	}
	showing := *open || h > rowH+.5
	// 面板的底（玻璃、阴影、边线）在最初的四分之一里淡入，入口同时淡出：收起的那一刻和普通入口一模一样
	veil := 1 - min(grown*4, 1)
	if !showing {
		veil = 1
	}
	anchor := k.dropdownTrigger(c, key, label, cur, m, veil, turn)
	if k.ddUp == nil {
		k.ddUp = map[string]bool{}
	}
	if !showing { // 往哪边长在收起时定，长的过程中不翻面
		b := anchor.Bounds(c)
		_, vh := c.Size()
		below, above := vh-(b.Y+b.H), b.Y
		k.ddUp[key] = below < fullH-rowH && above > below
	}
	up := k.ddUp[key]
	// body 搭面板：顶上一行是入口的复刻，下面是选项；朝上长时倒过来，入口在最下面。item 造一个选项行（真菜单项或退场时的摆设）。
	body := func(panel ui.Element, interactive bool, item func(i int) ui.Element) {
		at, self := ui.AnchorTopStart, ui.AnchorTopStart
		if up {
			at, self = ui.AnchorBottomStart, ui.AnchorBottomStart
			panel.Justify(ui.End)
		}
		panel.AttachTo(m.Trigger, at, self).Height(h).Radius(radius).Clip().Scrollbars(false) // 滚动条只给里面的选项列表；长的过程中面板自己装不下，不显示
		if w := anchor.Bounds(c).W; w > 0 {
			panel.Width(w)
		}
		if a := 1 - veil; a < 1 {
			panel.Opacity(a)
		}
		// 材质照浮层档（玻璃或降级）和浮层阴影；边线和入口同一条里侧细线，不占布局，入口和第一行才对得齐
		k.Glass(panel, LevelFloat, k.T.C.Float)
		k.FloatShadow(panel)
		panel.InsetShadow(0, 0, 0, sunkenRing, p.Line)
		k.surface(c, panel, key+"-popup", "choice-popup", radius, g.SpaceTight)
		head := func() {
			row := ui.Row(c.Key(key + "-head")).Label(label).FillWidth().Height(rowH).Shrink(0).Gap(g.SpaceSmall).AlignItems(ui.Center)
			k.pillPadding(row, g.FieldInset)
			if interactive {
				row.OnClick(func() { *open = false })
			}
			row.Children(func() {
				ui.Text(c, cur).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine().Grow(1).MinWidth(0)
				ui.Icon(c, chatui.Icon("down", false)).Size(g.IconSmall, g.IconSmall).Rotate(turn)
			})
		}
		list := func() {
			if rows == 0 {
				return
			}
			right := g.SpaceTight
			if scrolls {
				right += g.SpaceSmall // 滚动条的位置，不压着选项
			}
			box := ui.Column(c.Key(key+"-opts")).FillWidth().Height(fullH-rowH).Shrink(0).Padding(g.SpaceTight, right, g.SpaceTight, g.SpaceTight)
			if grown < 1 {
				box.Opacity(grown) // 选项的字跟着长出来慢慢显
			}
			box.Children(func() {
				ui.Scroll(c.Key(key + "-list")).FillWidth().Height(listH).Shrink(0).Gap(g.SpaceTight).Children(func() {
					for i, it := range items {
						row := item(i)
						if interactive { // 展开时选项一个接一个淡入（退场摆设不动）
							k.StaggerIn(c, row, i, len(items), true)
						}
						// 选项的字和顶上一行入口的字左边对齐
						row.Height(g.HitSmall).MinWidth(g.HitSmall).Padding(g.ControlInset, g.ControlInset, g.ControlInset, max(g.FieldInset+g.SpaceSmall-g.SpaceTight, g.ControlInset)).Radius(g.Inner(radius, g.SpaceTight)).TextColor(p.Ink)
						if i == current {
							k.selectBg(row, selTag) // 当前项和筛选片的选中同一种强调
						} else if interactive && row.Highlighted() {
							row.Background(p.Hover)
						}
						row.Children(func() {
							t := ui.Text(c, it).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine()
							if itemFont != nil { // 每一项用它自己的字体画，选之前就看得到样子
								if f := itemFont(i); f != "" {
									t.Font(f)
								}
							}
						})
						if interactive && row.Clicked() && i != current {
							onChoose(i)
						}
						k.surface(c, row, key+"-"+strconv.Itoa(i), "choice-item", g.Inner(radius, g.SpaceTight), g.ControlInset)
					}
				})
			})
		}
		if up {
			list()
			head()
		} else {
			head()
			list()
		}
	}
	if *open {
		m.Popup(func(panel ui.Element) {
			body(panel, true, func(i int) ui.Element { return m.Item().Label(items[i]) })
		})
	} else if showing {
		// 收回去的那一段：真菜单已经关了，这里摆一个一样的面板接着缩，不收点击和焦点
		ui.Overlay(c, func() {
			body(ui.Column(c.Key(key+"-ghost")), false, func(i int) ui.Element { return ui.Row(c.Key(key + "-ghost-" + strconv.Itoa(i))) })
		})
	}
	return m.Trigger
}

// categoryRows 画分类行（图标 + 文字，左对齐；选中是“浮起来”的一块，会在行之间上下滑，没选中是平的、悬停深一层）。
// 设置页的左栏和 SettingsPane 共用这一份；key 区分用在哪（滑动的记忆按它分）；query 非空时按分类名过滤，一个都没有写一句话。
func (k *Kit) categoryRows(c *ui.Context, key string, cats []Category, selected, query string, onSelect func(id string)) {
	g, p := k.G, k.P
	st := k.slides("category/" + key)
	host := ui.Column(c.Key("category-rows")).FillWidth().Gap(g.SpaceTight)
	own := k.SlideIndicator(c, host, st, selected, SlideOptions{Radius: g.Pill(g.HitRegular), ClipRadius: 0})
	host.Children(func() {
		shown := 0
		for _, cat := range cats {
			if query != "" && !strings.Contains(cat.Label, query) {
				continue
			}
			shown++
			isSel := selected == cat.ID
			st.FillKey(cat.ID, isSel) // 选中的帧里记一笔，取消选中后换新的底色通道
			b := ui.ButtonBase(c.Key("category-"+cat.ID)).Label(cat.Label).FillWidth().Height(g.HitRegular).Padding(g.SpaceSmall, g.SpaceMedium).Gap(g.SpaceSmall).
				Justify(ui.Start).AlignItems(ui.Center).Radius(g.Pill(g.HitRegular)).Selected(isSel).TextColor(p.Ink).OnClick(func() { onSelect(cat.ID) })
			b.Bind(st.Item(cat.ID))
			if isSel {
				if own {
					k.selectBg(b, selMode) // 第一帧还没量到位置：先自己上和选中块同色同圆角的底，不带淡变
				} else {
					b.Background(ui.Transparent) // 块由上面的容器画；不写的话按钮基座会按“选中”自己上一层主题色
				}
			} else {
				// 没选中的悬停、按下的底淡变；不挂 Transition（它会把“刚被取消选中”的底也淡一遍，和滑走的块叠成两层）
				b.Background(b.AnimateColor(st.FillKey(cat.ID, false), k.flatFill(ui.Transparent, b.Pressed(), b.Hovered()), ControlFade))
			}
			b.Children(func() {
				if cat.Icon != nil {
					ui.Icon(c, cat.Icon).Size(g.IconSize, g.IconSize)
				}
				ui.Text(c, cat.Label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine)
			})
			k.surface(c, b, "category-"+cat.ID, "button", g.Pill(g.HitRegular), g.SpaceSmall)
		}
		if shown == 0 {
			ui.Text(c, "没有找到设置，换个词试试").FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Padding(0, g.SpaceMedium)
		}
	})
}

// CategoryList 是一列分类行（图标 + 文字），选中的那块在行之间上下滑；设置页的左栏用的就是它，画廊里单独摆也用它。
func (k *Kit) CategoryList(c *ui.Context, key string, cats []Category, selected string, onSelect func(id string)) {
	k.categoryRows(c, key, cats, selected, "", onSelect)
}

// ── 设置页的分组样板（2026-11-15）─────────────────────────────────────────────
//
// 三档底色：分类栏（Palette.Pane）比内容区（Palette.Content）暗一档，内容区是最亮的一档；
// 分组的面（Palette.Group）又比内容区深一档，所以栏和栏、面和底都只靠底色分开，不画线。
// 间距：一项上下各 SpaceSmall，所以相邻两项的字之间是 2×SpaceSmall；名字和它的说明只隔 SpaceMicro；
// 组和组之间是 SpaceLarge 加上组名那一行，一定比项间距的两倍还大。

// SettingsPaneSpec 是 SettingsPane 要的东西：左边分类，右边内容。
type SettingsPaneSpec struct {
	Group      string // 分类栏顶上的灰色组名，可空
	Categories []Category
	Selected   string
	OnSelect   func(id string)
	Height     float32 // 整块的高；内容区自己滚
	Content    func()  // 当前分类的内容：一串 SettingGroup
	// Gen 是“换代号”：变了就当作换了一页，内容区重建、各组重新错开淡入（画廊的“重放”用）；平时不用管，留 0。
	Gen int
}

// SettingsPane 是可以嵌进别处的“分类栏 + 内容区”：不带导航栏、标题行和窗口按钮，宽的时候内容靠左、最宽 SettingsGroupWidth。
func (k *Kit) SettingsPane(c *ui.Context, key string, sp SettingsPaneSpec) {
	g, p := k.G, k.P
	// 换分类（或换代号）：右边的各组一个接一个淡入、略往上移；页面刚打开、没人换过东西时直接就位
	seen := sp.Selected + "#" + fmt.Sprint(sp.Gen)
	if k.paneSeen == nil {
		k.paneSeen = map[string]string{}
	}
	prev, had := k.paneSeen[key]
	k.paneSeen[key] = seen
	run := &staggerRun{play: had && prev != seen}
	ui.Row(c.Key(key)).FillWidth().Height(sp.Height).Shrink(0).AlignItems(ui.Stretch).Radius(g.RadiusPanel).Clip().Children(func() {
		ui.Column(c.Key("categories")).Width(SettingsListWidth).Shrink(0).FillHeight().Padding(g.ListInset).Gap(g.SpaceTight).Background(p.Pane).Children(func() {
			if sp.Group != "" {
				ui.Text(c, sp.Group).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).Padding(0, g.SpaceMedium)
			}
			k.categoryRows(c, "pane", sp.Categories, sp.Selected, "", sp.OnSelect)
		})
		ui.Box(c.Key("content")).Grow(1).MinWidth(0).FillHeight().Background(p.Content).Children(func() {
			ui.Scroll(c.Key("content-" + seen)).Fill().Padding(g.SpaceLarge).Children(func() {
				ui.Column(c.Key("page")).FillWidth().MaxWidth(g.SettingsGroupWidth).Gap(g.SpaceLarge).Children(func() {
					if sp.Content != nil {
						k.stag = run
						sp.Content()
						k.stag = nil
					}
				})
			})
		})
	})
}

// SettingGroup 是一组设置：组名在面外面的上方（小一号、次要色，字和面里的名字左对齐），面是一块圆角（RadiusControl，
// 多行容器那一档）、不描边，里面的 SettingItem 同心（行的悬停圆角 = 面圆角 − 面内距）。面最宽 SettingsGroupWidth。
func (k *Kit) SettingGroup(c *ui.Context, key, title string, items func()) {
	g, p := k.G, k.P
	group := ui.Column(c.Key(key)).FillWidth().MaxWidth(g.SettingsGroupWidth).Gap(g.SpaceSmall)
	if i, play := k.takeStagger(); i >= 0 {
		k.StaggerIn(c, group, i, 0, play)
	}
	group.Children(func() {
		if title != "" {
			ui.Text(c.Key("title"), title).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).FontWeight(g.MediumWeight).TextColor(p.Muted).
				Padding(0, g.SpaceTight+g.SettingsInsetX)
		}
		// 分组面是卡片，归“浮起来”那一档：底色之外加内容档的淡阴影（设计自查第 20 条；主人 2026-11-16 同意），换到别的底上也看得出是一块
		face := ui.Column(c.Key("face")).FillWidth().Padding(g.SpaceTight).Radius(g.RadiusControl).Background(p.Group)
		k.RaisedShadow(face)
		k.surface(c, face, key, "setting-group", g.RadiusControl, g.SpaceTight).Children(items)
	})
}

// SettingItemOptions 是 SettingItem 的可选项。
type SettingItemOptions struct {
	Changed bool   // 用户改过（和默认不一样）：名字旁边出现一个小圆点；没改过什么也不画，不写“默认值”
	Was     string // 默认是什么，写在小圆点的悬停提示里：“你改过，默认是 …”
	Below   bool   // 控件放在名字和说明下面、占满一行（选项多的筛选片）；否则紧挨着放在右边
}

// SettingItem 是分组里的一项：左边名字，名字下面一行小字说明（没有就不占位），右边紧挨着是控件。
// 项自己上下各留 SpaceSmall，所以项与项之间是 2×SpaceSmall，名字和说明之间只有 SpaceMicro，一眼看出哪两行是一对。
func (k *Kit) SettingItem(c *ui.Context, key, name, hint string, o SettingItemOptions, control func()) {
	g, p := k.G, k.P
	item := ui.Column(c.Key(key)).FillWidth().Padding(g.SpaceSmall, g.SettingsInsetX).Gap(g.SpaceSmall)
	k.surface(c, item, key, "setting-item", 0, 0).Children(func() {
		ui.Row(c.Key("line")).FillWidth().MinHeight(g.RowHeight).Gap(g.SpacePanel).AlignItems(ui.Center).Children(func() {
			ui.Column(c.Key("label")).Grow(1).MinWidth(0).Gap(g.SpaceMicro).Children(func() {
				ui.Row(c.Key("name")).AlignItems(ui.Center).Gap(g.SpaceTight).Children(func() {
					ui.Text(c, name).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).Shrink(1).MinWidth(0)
					if o.Changed {
						k.ModifiedDot(c, key+"-dot", o.Was)
					}
				})
				if hint != "" {
					ui.Text(c, hint).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Wrap()
				}
			})
			if !o.Below && control != nil {
				ui.Row(c.Key("ctl")).Shrink(0).Gap(g.SpaceSmall).AlignItems(ui.Center).Children(control)
			}
		})
		if o.Below && control != nil {
			ui.Row(c.Key("ctl-below")).FillWidth().Children(control)
		}
	})
}

// ModifiedDot 是“被改过”的小圆点：强调色，直径 SettingsDot，命中区是 SpaceMedium 见方；悬停提示“你改过，默认是 …”。
func (k *Kit) ModifiedDot(c *ui.Context, key, was string) {
	g, p := k.G, k.P
	tip := "你改过"
	if was != "" {
		tip += "，默认是" + was
	}
	box := ui.Box(c.Key(key)).Label("改过的项").Size(g.SpaceMedium, g.SpaceMedium).Shrink(0)
	box.Draw(func(pt *ui.Painter, r ui.Rect) {
		d := g.SettingsDot
		// 点贴着命中区的左边画（命中区不变）：画在正中时离名字还隔着半个命中区，像飘着的一点（主人 2026-11-16“确实稍微有点远”）
		pt.Fill(ui.Rect{X: r.X, Y: r.Y + (r.H-d)/2, W: d, H: d}, p.Accent, d/2)
	})
	k.Tip(c, box, tip)
	k.surface(c, box, key, "modified-dot", g.SettingsDot/2, 0)
}

// SettingValue 是只读的值：右边一小段次要色文字，不是控件，不能点。
func (k *Kit) SettingValue(c *ui.Context, text string) {
	g, p := k.G, k.P
	ui.Text(c, text).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).SingleLine().Shrink(1).MinWidth(0)
}

// SettingLink 是“设置 ›”：点开同一项下面的细项。箭头向右，open 时转 90°；做成平的胶囊，悬停深一层。
func (k *Kit) SettingLink(c *ui.Context, key, text string, open bool, onClick func()) {
	g, p := k.G, k.P
	b := ui.ButtonBase(c.Key(key)).Label(text).Height(g.HitSmall).Gap(g.SpaceTight).AlignItems(ui.Center).Radius(g.Pill(g.HitSmall)).TextColor(p.Muted).OnClick(onClick)
	k.pillPadding(b, g.ControlInset)
	k.Flat(b, ui.Transparent, b.Pressed(), b.Hovered())
	deg := float32(0)
	if open {
		deg = 90
	}
	b.Children(func() {
		ui.Text(c, text).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine()
		ui.Icon(c.Key("arrow"), chatui.Icon("right", false)).Size(g.IconSmall, g.IconSmall).Shrink(0).TextColor(p.Meta).Rotate(b.AnimateTo("arrow", deg, ControlMotion))
	})
	k.surface(c, b, key, "button", g.Pill(g.HitSmall), g.ControlInset)
}

// SettingSelect 是设置行右边的下拉：入口定宽 SettingsSelectWidth，选了长字短字都不变宽。
func (k *Kit) SettingSelect(c *ui.Context, key, label string, open *bool, current int, items []string, onChoose func(i int)) {
	k.Dropdown(c, key, label, open, current, items, onChoose, nil).Width(k.G.SettingsSelectWidth)
}

// SettingCollapsed 是分组里“收起来的一长串”：一行“箭头 + 概述”，点一下展开成紧凑的 cols 列名字列表（只读，纯文字）。
// 概述写成“另有 17 个内置插件（只读）”；展开后每行 cols 个名字，列宽 = 面内宽 / cols。open 由调用方持有。
func (k *Kit) SettingCollapsed(c *ui.Context, key, summary string, names []string, open *bool, cols int) {
	g, p := k.G, k.P
	cols = max(cols, 1)
	inner := g.Inner(g.RadiusControl, g.SpaceTight)
	ui.Column(c.Key(key)).FillWidth().Children(func() {
		head := ui.ButtonBase(c.Key("head")).Label(summary).FillWidth().Height(g.RowHeight+2*g.SpaceSmall).Padding(0, g.SettingsInsetX).Gap(g.SpaceSmall).
			Justify(ui.Start).AlignItems(ui.Center).Radius(inner).TextColor(p.Muted).OnClick(func() { *open = !*open })
		k.Flat(head, ui.Transparent, head.Pressed(), head.Hovered())
		deg := float32(0)
		if *open {
			deg = 90
		}
		head.Children(func() {
			ui.Icon(c.Key("chevron"), chatui.Icon("right", false)).Size(g.IconSmall, g.IconSmall).Shrink(0).TextColor(p.Meta).Rotate(head.AnimateTo("chevron", deg, ControlMotion))
			ui.Text(c.Key("summary"), summary).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).SingleLine()
		})
		k.surface(c, head, key+"-head", "button", inner, 0)
		indent := g.SettingsInsetX + g.IconSmall + g.SpaceSmall // 名字和上面概述的字左边对齐
		cell := (g.SettingsGroupWidth - 2*g.SpaceTight - indent - g.SettingsInsetX) / float32(cols)
		k.Disclose(c, key+"-names", *open, 0, func() { // 展开收起：高度弹簧加淡变，收完才拿掉
			ui.Column(c.Key("names")).FillWidth().Padding(0, g.SettingsInsetX, g.SpaceSmall, indent).Gap(g.SpaceSmall).Children(func() {
				for r := 0; r*cols < len(names); r++ {
					row := ui.Row(c.Key(fmt.Sprint("row-", r))).FillWidth().Wrap()
					k.StaggerIn(c, row, r, (len(names)+cols-1)/cols, true) // 展开后一行一行淡入
					k.surface(c, row, fmt.Sprint(key, "-row-", r), "setting-list-row", 0, 0).Children(func() {
						for _, n := range names[r*cols : min((r+1)*cols, len(names))] {
							ui.Text(c, n).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).SingleLine().Width(cell)
						}
					})
				}
			})
		})
	})
}
