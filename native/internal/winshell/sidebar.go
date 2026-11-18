package winshell

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// Conversation 是会话列表里的一行。预览是这个会话的最后一条消息（主人 2026-11-08 定）。
type Conversation struct {
	ID, Name, Preview string
	Unread            int
	When              string     // 名字右边的时间（最后一条消息），空就不画
	AvatarIndex       int        // 调色板里的头像色序号
	Avatar            *ui.Bitmap // 有真实头像就给，没有画首字
	Square            bool       // 角色的圆角方头像；QQ 上的人是圆的
}

// MenuItem 是手画菜单（“+”、药丸、下拉、会话行右键）里的一项。
// Icon、Shortcut、Danger、Trailing、Disabled 和 mygo 自带的 ui.MenuItem 同名同义，右键菜单两套画法看起来一样（审查第四节）。
type MenuItem struct {
	Label   string
	Icon    *ui.SVG
	Action  func()
	Group   string // 分组名：和上一项不同时画一行小标题（模型按供应商分）
	Checked bool   // 当前选中的那一项，右边画对勾
	// Shortcut 是右边的灰字快捷键提示，如 "Ctrl+C"。只是提示：键本身由界面自己处理，写之前确认代码里真的绑了。
	Shortcut string
	// Danger 是会删掉或丢掉东西的项：红字，自动排到菜单最下面，前面一条分隔线。字也要写明白做什么（“删除…”）。
	Danger bool
	// Disabled 是现在做不了的项：整体变灰、不响应；用 Trailing 在右边写一句原因，而不是把项藏起来。
	Disabled bool
	// Trailing 是右边的灰字，在快捷键之前：一个值，或不可用的原因。
	Trailing string
	// Separator 在这一项上面画一条分隔线（第一项、紧跟在线后面的不画）。
	Separator bool
}

// SidebarSpec 是会话列表栏要的数据与动作。列表可以有上千行，所以按下标取，只构建看得见的行。
type SidebarSpec struct {
	Segments  []string // 分段名，私聊｜群聊
	Segment   *int     // 当前分段；分段控件直接改它
	OnSegment func()   // 分段变了
	Count     int
	At        func(i int) Conversation
	Selected  string
	OnSelect  func(id string)
	Search    *string
	Hint      string // 搜索框的提示
	Empty     string // 没有会话时说什么；空串不写
	// Placeholder 不为空时，没有会话那一行由它画（第一份概览还没到时什么都不写、读得慢了才写“取数据中…”，不闪），代替 Empty。
	Placeholder func(c *ui.Context)
	Menu        []MenuItem
	// RowMenu 是会话行的右键菜单：按会话 ID 给项，返回空就这一行没有右键菜单。
	// 只给后台已经有的动作（审查第四节）；删除类的项标 Danger，自动排到最下面。每帧对看得见的行各问一次，别在里面做重活。
	RowMenu   func(id string) []MenuItem
	MenuLabel string                // “+”钮的读屏名和提示
	OnVisible func(first, last int) // 列表实际构建范围，用来按需取头像
}

// Sidebar 画会话列表栏：搜索行（搜索框 + “+”菜单钮）、私聊｜群聊分段、会话行铺满整宽。
func (k *Kit) Sidebar(c *ui.Context, s *State, sp *SidebarSpec, narrow bool) {
	if s.compactView && !narrow {
		k.compactSidebar(c, s, sp)
		return
	}
	g, p := k.G, k.P
	k.surface(c, ui.Column(c.Key("sidebar")).Fill().Background(p.Pane), "sidebar", "pane", 0, g.SpacePanel).Children(func() {
		// 搜索框和聊天标题条同高、同一条线
		ui.Row(c.Key("search-row")).Height(g.BarHeight).Shrink(0).Margin(2*g.SpaceSmall, g.ListInset, g.SpaceSmall, g.ListInset).Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
			if narrow { // 窄窗口这一层是最右一栏：窗口按钮排在搜索框后面，不压着它
				defer k.WindowButtons(c)
			}
			// 搜索框最左边一个放大镜（主人 2026-11-09）
			k.searchShaped(c, "search", sp.Search, sp.Hint, &s.SearchField, g.SearchHeight, g.SearchInset).Grow(1)
			if len(sp.Menu) > 0 {
				k.plusMenu(c, s, sp)
			}
		})
		// 会话行铺满列表整宽（ADR-284），内容栏自己不留内距，分段控件那一排单独留
		ui.Column(c.Key("sidebar-content")).Grow(1).MinHeight(0).Children(func() {
			if len(sp.Segments) > 0 {
				ui.Column(c.Key("sidebar-tools")).FillWidth().Padding(g.ListInset).Children(func() {
					if k.Segment(c, "venues", sp.Segment, sp.Segments) && sp.OnSegment != nil {
						sp.OnSegment()
					}
				})
			}
			s.Convs.Key = func(i int) any { return sp.At(i).ID }
			st := k.slides("conversations")
			own := true // 选中行要不要自己画底：滑动期间由列表画那一块，滑完交还给行（行跟着滚动走）
			list := ui.List(c.Key("conversation-list"), &s.Convs, sp.Count, func(i int) { k.conversationRow(c, s, sp, i, st, &own) }).Grow(1).MinHeight(0).ScrollbarInsets(0, ListGrab/2, 0, 0).Label("会话条目")
			own = k.SlideIndicator(c, list, st, sp.Selected, SlideOptions{Radius: g.RadiusRow, OwnWhenSettled: true})
			if sp.Count == 0 {
				if sp.Placeholder != nil {
					sp.Placeholder(c)
				} else if sp.Empty != "" {
					ui.Text(c.Key("conversation-empty"), sp.Empty).TextColor(p.Muted).Padding(g.ListInset)
				}
			}
		})
	})
	k.visible(s, sp)
}

func (k *Kit) visible(s *State, sp *SidebarSpec) {
	if sp.OnVisible != nil && sp.Count > 0 {
		first, last := s.Convs.Visible()
		sp.OnVisible(max(0, first), min(last, sp.Count-1))
	}
}

// conversationRow 是一行：两边离栏边一个 ListInset，圆角，选中是浮起的一块、字色不变（Raised，照 Bun .su-conv，主人 2026-11-09 定，改 ADR-284）；
// 选中块画在行自己身上，滚动时不会和行脱开。悬停和按下各一层浅底。名字常规字重、只有选中的一行 700（2026-11-17 体检 C2：
// 一列全是粗体就分不出哪行要紧），预览用第三级灰。
func (k *Kit) conversationRow(c *ui.Context, s *State, sp *SidebarSpec, i int, st *SlideState, own *bool) {
	g, p := k.G, k.P
	cv := sp.At(i)
	id := cv.ID
	selected := sp.Selected == id
	st.FillKey(id, selected)
	b := ui.ButtonBase(c.Key("conversation-"+id)).Label(cv.Name).Margin(1, g.ListInset).MinHeight(g.ConversationAvatar + g.BarInset*2).Padding(g.BarInset).Gap(g.SpaceMedium).AlignItems(ui.Center).Radius(g.RadiusRow).Selected(selected).OnClick(func() { sp.OnSelect(id) })
	b.Bind(st.Item(id))
	weight := g.NormalWeight
	if selected {
		weight = g.BoldWeight
		if *own {
			k.selectBg(b, selMode) // 选中行自己画底：和滑动的块同色同圆角，不带淡变
		} else {
			b.Background(ui.Transparent) // 滑动期间块由列表画；不写的话按钮基座会按“选中”自己上一层主题色
		}
	} else {
		// 悬停、按下的底 140 毫秒淡变；不挂 Transition，选中行和刚取消选中的行的底都不淡，和滑动的那一块交接时不闪
		bg := ui.Transparent
		if b.Pressed() {
			bg = p.Press
		} else if b.Hovered() {
			bg = p.Hover
		}
		b.Background(b.AnimateColor(st.FillKey(id, false), bg, ControlFade))
	}
	// 时间和未读角标一上一下，中线对齐（主人 2026-11-09）：谁窄谁往里让半个差值
	measure := func(text string) float32 {
		w, _ := c.MeasureText(1e6, ui.Span{Text: text, Size: g.MetaSize, Font: c.Theme().Font, Features: chatui.Tabular})
		return w
	}
	var whenW, badgeW float32
	if cv.When != "" {
		whenW = measure(cv.When)
	}
	if cv.Unread > 0 {
		badgeW = max(g.ConversationLine, measure(unreadText(cv.Unread))+2*g.BarInset)
	}
	if cv.When == "" {
		whenW = badgeW
	}
	b.TextColor(p.Ink).Children(func() {
		k.conversationAvatar(c, cv, g.ConversationAvatar)
		ui.Column(c).Grow(1).MinWidth(0).Gap(0).Children(func() {
			ui.Row(c).FillWidth().Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
				ui.Text(c, cv.Name).FontSize(g.LabelSize).FontWeight(weight).FixedLineHeight(g.ConversationLine).TextColor(p.Ink).SingleLine().Grow(1).MinWidth(0)
				if cv.When != "" {
					w := ui.Text(c.Key("when-"+id), cv.When).FontSize(g.MetaSize).FontFeatures(chatui.Tabular).FixedLineHeight(g.ConversationLine).TextColor(p.Meta).SingleLine().Shrink(0)
					if shift := badgeW - whenW; shift > 0 {
						w.Margin(0, shift/2, 0, 0)
					}
				}
			})
			ui.Row(c).FillWidth().Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
				ui.Text(c, cv.Preview).FontSize(g.MetaSize).FixedLineHeight(g.ConversationLine).TextColor(p.Meta).SingleLine().Grow(1).MinWidth(0)
				if cv.Unread > 0 {
					ui.Text(c.Key("unread-"+id), unreadText(cv.Unread)).FontSize(g.MetaSize).FontFeatures(chatui.Tabular).FixedLineHeight(g.ConversationLine).Padding(0, g.BarInset).MinWidth(g.ConversationLine).Radius(g.ConversationLine/2).Background(p.Strong).TextColor(p.OnStrong).Shrink(0).Margin(0, max(0, whenW-badgeW)/2, 0, 0)
				}
			})
		})
	})
	k.surface(c, b, "conversation-"+id, "conversation", g.RadiusRow, g.BarInset)
	k.rowMenu(c, b, sp, id)
}

// rowMenu 给会话行接上右键菜单（头像列的小行也用）：和别的手画菜单同一个面板、同一套项。
func (k *Kit) rowMenu(c *ui.Context, row ui.Element, sp *SidebarSpec, id string) {
	if sp.RowMenu == nil {
		return
	}
	k.ContextMenu(c, row, "row-menu", sp.RowMenu(id))
}

// ContextMenu 给 target 接上右键菜单（Shift+F10、菜单键也开）：和别的手画菜单同一个面板、同一套项、同一种展开收起。没有项就不接。
func (k *Kit) ContextMenu(c *ui.Context, target ui.Element, key string, items []MenuItem) {
	if len(items) == 0 {
		return
	}
	open := ui.Local(target, key+"-open", func() bool { return false })
	m := ui.ContextMenuBase(c, target, open)
	m.Popup(func(panel ui.Element) { k.menuPanel(c, m, panel, key, items) })
}

// conversationAvatar 按会话是角色还是人选头像形状。
func (k *Kit) conversationAvatar(c *ui.Context, cv Conversation, size float32) {
	if cv.Square { // 圆角和外面的行同心
		k.avatarShaped(c, "conversation-"+cv.ID, cv.Name, cv.AvatarIndex, size, k.G.RadiusRow-k.G.BarInset, cv.Avatar)
		return
	}
	k.Avatar(c, "conversation-"+cv.ID, cv.Name, cv.AvatarIndex, size, cv.Avatar)
}

func unreadText(n int) string {
	if n > 999 {
		return "999+"
	}
	return fmt.Sprint(n)
}

// compactSidebar 是会话列表的头像列（Telegram 拖到 130 以下）：顶上一格放大镜，点了展开列表、光标进搜索框；
// 每个会话一格，选中是浮起的圆角块（和展开时同一族）。和展开时是同一栏、同一个底色，只是变窄（设计语言第二节）。
func (k *Kit) compactSidebar(c *ui.Context, s *State, sp *SidebarSpec) {
	g, p := k.G, k.P
	k.surface(c, ui.Column(c.Key("sidebar")).Fill().Background(p.Pane), "sidebar", "pane", 0, 0).Children(func() {
		ui.Row(c.Key("search-row")).Height(g.BarHeight).Shrink(0).Margin(2*g.SpaceSmall, g.ListInset, g.SpaceSmall, g.ListInset).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			k.IconButton(c, "search-expand", "搜索会话", IconSearch, func() {
				s.Panes.Compact = false
				s.SearchField.Focus(c)
			})
		})
		s.Convs.Key = func(i int) any { return sp.At(i).ID }
		st := k.slides("conversations-compact")
		own := true
		list := ui.List(c.Key("conversation-list"), &s.Convs, sp.Count, func(i int) {
			cv := sp.At(i)
			id := cv.ID
			selected := sp.Selected == id
			st.FillKey(id, selected)
			b := ui.ButtonBase(c.Key("conversation-"+id)).Label(cv.Name).Size(g.ConversationAvatar+2*g.BarInset, g.ConversationAvatar+2*g.BarInset).AlignSelf(ui.Center).Center().Radius(g.RadiusRow).Selected(selected).OnClick(func() { sp.OnSelect(id) })
			b.Bind(st.Item(id))
			if selected {
				if own {
					k.selectBg(b, selMode)
				} else {
					b.Background(ui.Transparent)
				}
			} else {
				bg := ui.Transparent
				if b.Pressed() {
					bg = p.Press
				} else if b.Hovered() {
					bg = p.Hover
				}
				b.Background(b.AnimateColor(st.FillKey(id, false), bg, ControlFade))
			}
			b.Children(func() {
				k.conversationAvatar(c, cv, g.ConversationAvatar)
				if cv.Unread > 0 { // 照 Telegram 小列（dialogs_layout.cpp:304 PaintNarrowCounter）：未读数贴在头像右上
					badgeBg, badgeInk := p.Strong, p.OnStrong
					ui.Text(c.Key("unread-"+id), unreadText(cv.Unread)).FontSize(g.MetaSize).FontFeatures(chatui.Tabular).FixedLineHeight(g.CompactBadge).Padding(0, g.SpaceTight).
						MinWidth(g.CompactBadge).Radius(g.CompactBadge / 2).Background(badgeBg).TextColor(badgeInk).Absolute().Top(g.SpaceTight).Right(g.SpaceTight)
				}
			})
			k.surface(c, b, "conversation-"+id, "conversation", g.RadiusRow, g.BarInset)
			k.rowMenu(c, b, sp, id)
		}).Grow(1).MinHeight(0).ScrollbarInsets(0, ListGrab/2, 0, 0).Label("会话条目")
		own = k.SlideIndicator(c, list, st, sp.Selected, SlideOptions{Radius: g.RadiusRow, OwnWhenSettled: true})
	})
	k.visible(s, sp)
}

// plusMenu 是搜索框右边的“+”钮，点开是菜单（主人 2026-11-08 定；原型没有这个动作，照原型 choice 的弹出面板推）：
// 面板 Composer 底、圆角 radiusControl、内距和项间距 spaceTight、项圆角 radiusSegment（外圆角 − 内距，同心），
// 项高 32，高亮 Selected 底；设计语言第四层浮层再加一圈 FloatRing 细边。
func (k *Kit) plusMenu(c *ui.Context, s *State, sp *SidebarSpec) {
	g, p := k.G, k.P
	label := sp.MenuLabel
	if label == "" {
		label = "更多"
	}
	m := ui.MenuBase(c.Key("conversation-menu"), &s.menuOpen)
	trigger := m.Trigger.Label(label).Size(g.HitSmall, g.HitSmall).Shrink(0).Center().Radius(g.HitSmall / 2).TextColor(p.Ink)
	k.Tip(c, trigger, label)
	bg := ui.Transparent
	if s.menuOpen || trigger.Pressed() {
		bg = p.Press
	} else if trigger.Hovered() {
		bg = p.Hover
	}
	trigger.Background(trigger.AnimateColor("fill", bg, ControlFade)).Children(func() { ui.Icon(c, IconPlus).Size(g.IconSize, g.IconSize) })
	k.surface(c, trigger, "conversation-menu", "icon-button", g.HitSmall/2, 0)
	m.Popup(func(panel ui.Element) { k.menuPanel(c, m, panel, "conversation-menu", sp.Menu) })
}

// menuPanel 是弹出菜单的面板和里面的项；“+”菜单和输入框的药丸菜单共用。
func (k *Kit) menuPanel(c *ui.Context, m ui.MenuParts, panel ui.Element, key string, items []MenuItem) {
	g := k.G
	panel.Padding(g.SpaceTight).Gap(g.SpaceTight).Radius(g.RadiusControl)
	k.FloatMenu(c, panel)
	k.surface(c, panel, key+"-popup", "menu-popup", g.RadiusControl, g.SpaceTight)
	k.menuItems(c, m, key, items)
}

// menuOrder 把危险项排到最后（保持各自原来的先后）：删除、丢弃类的项永远在菜单最下面。
func menuOrder(items []MenuItem) []MenuItem {
	out := make([]MenuItem, 0, len(items))
	for _, it := range items {
		if !it.Danger {
			out = append(out, it)
		}
	}
	for _, it := range items {
		if it.Danger {
			out = append(out, it)
		}
	}
	return out
}

// menuItems 画一串菜单项：分组名换了就画一行小标题；危险项在最下面、前面一条分隔线、红字；
// 不可用的项变灰不响应、右边写原因；快捷键和值是右边的灰字。
// 同一个菜单里图标要么都有要么都没有：有一项带图标，没图标的项就留出同宽的空位，文字左边对齐。
func (k *Kit) menuItems(c *ui.Context, m ui.MenuParts, key string, items []MenuItem) {
	g, p := k.G, k.P
	items = menuOrder(items)
	anyIcon := false
	for _, it := range items {
		anyIcon = anyIcon || it.Icon != nil
	}
	group := ""
	for i, it := range items {
		if i > 0 && (it.Separator || it.Danger && !items[i-1].Danger) {
			ui.Box(c.Key(fmt.Sprint(key, "-separator-", i))).FillWidth().Height(1).Shrink(0).Background(p.Line)
		}
		if it.Group != group {
			group = it.Group
			if group != "" {
				ui.Text(c.Key(fmt.Sprint(key, "-group-", i)), group).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).Padding(g.SpaceTight, g.ControlInset, 0, g.ControlInset)
			}
		}
		ink := p.Ink
		if it.Danger {
			ink = k.T.C.DangerText
		}
		item := m.Item().Label(it.Label).Height(g.HitSmall).MinWidth(g.HitSmall).Padding(g.ControlInset).Gap(g.SpaceSmall).AlignItems(ui.Center).Radius(g.RadiusSegment).TextColor(ink)
		if it.Disabled {
			item.Disabled(true).Opacity(DisabledAlpha)
		} else if item.Highlighted() {
			item.Background(p.Selected)
		}
		item.Children(func() {
			if it.Icon != nil {
				ui.Icon(c, it.Icon).Size(g.IconSmall, g.IconSmall).Shrink(0)
			} else if anyIcon {
				ui.Box(c).Size(g.IconSmall, g.IconSmall).Shrink(0)
			}
			ui.Text(c, it.Label).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).SingleLine().Grow(1).MinWidth(0)
			if it.Trailing != "" {
				ui.Text(c, it.Trailing).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).SingleLine().Shrink(0)
			}
			if it.Shortcut != "" {
				ui.Text(c, it.Shortcut).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).TextColor(p.Meta).SingleLine().Shrink(0)
			}
			if it.Checked {
				ui.Icon(c, IconCheck).Size(g.IconSmall, g.IconSmall).TextColor(p.Accent).Shrink(0)
			}
		})
		if it.Action != nil && !it.Disabled {
			item.OnClick(it.Action)
		}
		k.surface(c, item, fmt.Sprint(key, "-item-", i), "menu-item", g.RadiusSegment, g.ControlInset)
	}
}

// PillMenu 是输入框底行的小药丸（照 Bun .su-composer-bar 的小按钮）：图标加当前值，点开是菜单；没有项时只显示、不能点，悬停看说明。
// 菜单太长时面板自己限高滚动。
func (k *Kit) PillMenu(c *ui.Context, key, label, hint string, icon *ui.SVG, open *bool, items []MenuItem) {
	g, p := k.G, k.P
	if len(items) == 0 { // 没有可选的：只显示，但悬停还能看说明（禁用的按钮没有悬停提示）
		pill := ui.Row(c.Key(key)).Label(hint).Height(g.HitSmall).Shrink(1).MinWidth(0).Padding(g.ControlInset).Gap(g.SpaceTight).AlignItems(ui.Center).TextColor(p.Muted)
		k.Tip(c, pill, hint)
		k.surface(c, pill, key, "pill", g.Pill(g.HitSmall), g.ControlInset).Children(func() {
			ui.Icon(c, icon).Size(g.IconSmall, g.IconSmall).Shrink(0)
			ui.Text(c, label).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).SingleLine().MinWidth(0)
		})
		return
	}
	m := ui.MenuBase(c.Key(key), open)
	trigger := m.Trigger.Label(hint).Height(g.HitSmall).Shrink(1).MinWidth(0).Padding(g.ControlInset).Gap(g.SpaceTight).AlignItems(ui.Center).Radius(g.Pill(g.HitSmall)).TextColor(p.Muted)
	k.Tip(c, trigger, hint)
	bg := ui.Transparent
	if *open || trigger.Pressed() {
		bg = p.Press
	} else if trigger.Hovered() {
		bg = p.Hover
	}
	trigger.Background(trigger.AnimateColor("fill", bg, ControlFade)).Children(func() {
		ui.Icon(c, icon).Size(g.IconSmall, g.IconSmall).Shrink(0)
		ui.Text(c, label).FontSize(g.MetaSize).FixedLineHeight(g.MetaLine).SingleLine().MinWidth(0)
	})
	k.surface(c, trigger, key, "pill", g.Pill(g.HitSmall), g.ControlInset)
	m.Popup(func(panel ui.Element) {
		panel.MinWidth(g.ControlWide).MaxHeight(g.ControlWide * 1.4)
		ui.Scroll(c.Key(key + "-scroll")).FillWidth().Children(func() {
			ui.Column(c.Key(key + "-items")).FillWidth().Gap(g.SpaceTight).Children(func() { k.menuPanel(c, m, panel, key, items) })
		})
	})
}
