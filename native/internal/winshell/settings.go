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
				k.searchShaped(c, "search", st.Search, "搜索设置", &s.SearchField, g.SearchHeight, g.SearchInset, g.RadiusBar).Grow(1)
			}
		})
		ui.Column(c.Key("sidebar-tools")).FillWidth().Padding(g.ListInset).Gap(g.SpaceTight).Children(func() {
			if st.Group != "" {
				ui.Text(c, st.Group).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).Padding(0, g.SpaceSmall)
			}
			query := ""
			if st.Search != nil {
				query = strings.TrimSpace(*st.Search)
			}
			shown := 0
			for _, cat := range st.Categories {
				if query != "" && !strings.Contains(cat.Label, query) {
					continue
				}
				shown++
				selected := st.Selected == cat.ID
				b := ui.ButtonBase(c.Key("category-" + cat.ID)).Label(cat.Label).FillWidth().Height(g.HitRegular).Padding(g.SpaceSmall).Gap(g.SpaceSmall).
					Justify(ui.Start).AlignItems(ui.Center).Radius(g.RadiusControl).Selected(selected).TextColor(p.Ink).OnClick(func() {
					s.OpenChat()
					st.OnSelect(cat.ID)
				})
				if selected {
					k.Lift(b)
				} else {
					bg := ui.Transparent
					if b.Pressed() {
						bg = p.Press
					} else if b.Hovered() {
						bg = p.Hover
					}
					b.Background(bg)
				}
				b.Transition(SelectFade)
				b.Children(func() {
					if cat.Icon != nil {
						ui.Icon(c, cat.Icon).Size(g.IconSize, g.IconSize)
					}
					ui.Text(c, cat.Label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine)
				})
				k.surface(c, b, "category-"+cat.ID, "button", g.RadiusControl, g.SpaceSmall)
			}
			if shown == 0 {
				ui.Text(c, "没有找到设置，换个词试试").FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).TextColor(p.Muted).Padding(0, g.SpaceSmall)
			}
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
func (k *Kit) dropdownTrigger(c *ui.Context, key, label, value string, m ui.MenuParts) *ui.Handle {
	g, p := k.G, k.P
	if k.anchors == nil {
		k.anchors = map[string]*ui.Handle{}
	}
	anchor := k.anchors[key]
	if anchor == nil {
		anchor = &ui.Handle{}
		k.anchors[key] = anchor
	}
	trigger := m.Trigger.Label(label).Height(g.RowHeight).Padding(g.FieldInset).Gap(g.SpaceSmall).Radius(g.RadiusControl).Background(p.Bubble).AlignItems(ui.Center).Shrink(1).MinWidth(0).Bind(anchor)
	trigger.Children(func() {
		ui.Text(c, value).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine().Grow(1).MinWidth(0)
		ui.Icon(c, chatui.Icon("down", false)).Size(g.IconSmall, g.IconSmall)
	})
	k.surface(c, trigger, key, "choice", g.RadiusControl, g.FieldInset)
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
	k.FloatMenu(panel)
	if w := anchor.Bounds(c).W; w > 0 {
		panel.Width(w)
	}
}

// Picker 是带搜索的分组下拉（选模型这种选项多的）：入口和普通下拉一样，点开顶上是搜索框，下面按组列出来。
func (k *Kit) Picker(c *ui.Context, key, label, value string, open *bool, query *string, hint string, items []MenuItem) {
	g, p := k.G, k.P
	m := ui.MenuBase(c.Key(key), open)
	anchor := k.dropdownTrigger(c, key, label, value, m)
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
		k.searchShaped(c, key+"-search", query, hint, search, g.RowHeight, g.FieldInset, g.RadiusSegment).FillWidth().Shrink(0)
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
	x := e.Spring("thumb-x", target, k.T.Bouncy)     // 位移回弹（ADR-285 第 6 条）；减少动态效果时直接给终点
	travel := trackW - thumb - 2*gap
	off, white := p.Ink.Alpha(.2), ui.RGB(252, 252, 252)
	e.Draw(func(pt *ui.Painter, r ui.Rect) {
		ty := r.Y + (r.H-trackH)/2
		pt.Fill(ui.Rect{X: r.X, Y: ty, W: trackW, H: trackH}, off.Mix(p.Accent, pos), trackH/2)
		tb := ui.Rect{X: r.X + gap + travel*x, Y: ty + gap, W: thumb, H: thumb}
		pt.Shadow(tb, thumb/2, 0, 1, 2, 0, k.T.C.ShadowA)
		pt.Fill(tb, white, thumb/2)
	})
	k.surface(c, e, key, "switch", g.HitSmall/2, 0)
	return e
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
		pt.Fill(ui.Rect{X: x0 - track/2, Y: cy - track/2, W: x1 - x0 + track, H: track}, t.C.Sunken, track/2)
		pt.Fill(ui.Rect{X: x0 - track/2, Y: cy - track/2, W: x - x0 + track, H: track}, acc.Base, track/2)
		if ticks && step > 0 { // 刻度：每一档一个小点，在轨道上；圆钮盖住当前那一档
			n := int(math.Round(float64((hi - lo) / step)))
			for i := 0; i <= n; i++ {
				tx := x0 + (x1-x0)*float32(i)/float32(n)
				pt.Fill(ui.Rect{X: tx - track/4, Y: cy + track, W: track / 2, H: track}, t.C.Text3, track/4)
			}
		}
		pt.Shadow(ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, kw/2, 0, 1, 3, 0, t.C.ShadowA)
		pt.Fill(ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, ui.RGB(252, 252, 252), kw/2)
		pt.Stroke(ui.Rect{X: x - kw/2, Y: cy - kw/2, W: kw, H: kw}, t.C.LineStrong, kw/2, 1)
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

// Dropdown 是下拉选择（照原型 choice）：触发钮高 36、四边内距相等、凹块底；面板和“+”菜单同一套（浮层第四层）。
// items 是选项文字，current 是当前选中的下标，选了别的调 onChoose。open 由调用方持有（每个下拉一份）。
func (k *Kit) Dropdown(c *ui.Context, key, label string, open *bool, current int, items []string, onChoose func(i int), itemFont func(i int) string) ui.Element {
	g, p := k.G, k.P
	m := ui.MenuBase(c.Key(key), open)
	cur := ""
	if current >= 0 && current < len(items) {
		cur = items[current]
	}
	anchor := k.dropdownTrigger(c, key, label, cur, m)
	m.Popup(func(panel ui.Element) {
		k.dropdownPanel(c, panel, anchor, len(items) > dropdownRows)
		panel.Gap(g.SpaceTight).MaxHeight(g.HitSmall*dropdownRows + g.SpaceTight*2)
		k.surface(c, panel, key+"-popup", "choice-popup", g.RadiusControl, g.SpaceTight)
		for i, it := range items {
			item := m.Item().Label(it).Height(g.HitSmall).MinWidth(g.HitSmall).Padding(g.ControlInset).Radius(g.RadiusSegment).TextColor(p.Ink)
			if item.Highlighted() || i == current {
				item.Background(p.Selected)
			}
			item.Children(func() {
				t := ui.Text(c, it).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine()
				if itemFont != nil { // 每一项用它自己的字体画，选之前就看得到样子
					if f := itemFont(i); f != "" {
						t.Font(f)
					}
				}
			})
			if item.Clicked() && i != current {
				onChoose(i)
			}
			k.surface(c, item, key+"-"+strconv.Itoa(i), "choice-item", g.RadiusSegment, g.ControlInset)
		}
	})
	return m.Trigger
}
