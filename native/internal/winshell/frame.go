package winshell

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// NavItem 是导航里的一项。Bottom 为真的项贴在栏底（设置）。
type NavItem struct {
	ID, Label string
	Icon      *ui.SVG
	IconOn    *ui.SVG // 选中时的实心版；空就用 Icon
	Bottom    bool
}

// ChatSpec 是聊天页中间那一栏：标题栏加消息列加输入区。
// 消息列和输入区归宿主画，外壳只给它们几何（ChatLayout）和输入区的位置（ComposerSlot）。
type ChatSpec struct {
	Key    string // 当前会话；空表示还没选，画 Empty
	Empty  string
	Header HeaderSpec
	OnBack func() // 窄窗口返回列表
	// OnArea 在聊天区元素建好、还没放子元素时调一次：宿主用它读拖进来的文件、量缩放。
	OnArea func(root ui.Element)
	Body   func(l ChatLayout)
	// WidthPct 是聊天区（消息列和输入框，永远同宽）占聊天栏宽度的百分比，40～100；100 是拉满，只留内边距；0 是没设，按原型居中、最宽 752。
	// 群聊拉满时对方贴左、自己贴右。聊天栏窄于 520 时不再缩，直接铺满。
	WidthPct float32
}

// Spec 是整个窗口要的东西。Chat 为真时是聊天类页面（本地、QQ）：导航 + 会话列表 + 聊天 + 右栏；
// 否则是整页内容（记忆、运行、设置）：导航 + 内容区，内容由宿主画。
type Spec struct {
	Nav    []NavItem
	Page   string
	OnPage func(id string)

	Chat    bool
	Sidebar *SidebarSpec
	Conv    ChatSpec
	Side    SideSpec
	// OnToggleList、OnToggleSide 额外通知宿主（宿主存设置）；外壳自己已经改了 State。可为空。
	OnPanes func()
	// NoShortcuts 为真时不注册 Ctrl+B、Ctrl+\（别的浮层占着键盘时）
	NoShortcuts bool

	// Settings 不为空时是设置页：导航 | 分类 | 内容，布局复用聊天页的结构（Chat 为假）
	Settings *SettingsSpec
	Content  func() // 其余非聊天页（记忆、运行）的内容
	Overlays func() // 弹窗等盖在整个窗口上的东西
}

// Frame 画整个窗口。顺序和原型 view 一致：先算分栏，再按宽度分三档摆。
func (k *Kit) Frame(c *ui.Context, s *State, sp *Spec) {
	g, p := k.G, k.P
	c.SetTheme(&k.Theme)
	w, _ := c.Size()
	k.Reduce = c.Preferences().ReduceTransparency // 系统开了降低透明度就退回实色
	c.Root().Background(p.Canvas)
	if sp.Chat && !sp.NoShortcuts {
		k.shortcuts(c, s, sp, w)
	}
	k.Plan(s, w, false)
	s.fade.jump = s.fade.pageSeen && s.fade.page != sp.Page // 换了页：页里的内容淡入
	s.fade.page, s.fade.pageSeen = sp.Page, true
	floating := sp.Chat && sp.Settings == nil && k.SideFloating(s, w)
	switch {
	case floating:
		s.float.live, s.float.spec = true, sp.Side
	case k.SideMode(w) != PanelFloat || !sp.Chat || sp.Settings != nil:
		s.float.live = false // 换了档位（拉宽拉窄）或换了页：不播退场，直接换成那一档的样子
	}
	if !s.float.live {
		s.float.entered = false
	}
	root := ui.Column(c.Key("workbench")).Fill().Background(p.Canvas)
	k.surface(c, root, "canvas", "canvas", 0, 0).Children(func() {
		ui.Box(c.Key("workspace")).Grow(1).MinHeight(0).Children(func() {
			switch {
			case sp.Settings != nil:
				k.settings(c, s, sp, w)
			case !sp.Chat:
				ui.Row(c.Key("columns")).Fill().AlignItems(ui.Stretch).Children(func() {
					k.nav(c, s, sp)
					ui.Column(c.Key("page")).Grow(1).MinWidth(0).FillHeight().Children(func() {
						if k.Win != nil {
							k.pageHeader(c, sp)
						}
						body := ui.Box(c.Key("page-body")).Grow(1).MinHeight(0).FillWidth()
						k.fadeIn(body, s.fade.jump)
						body.Children(func() {
							if sp.Content != nil {
								sp.Content()
							}
						})
						k.windowCapsule(c, k.windowInset(false))
					})
				})
			case k.Mode(w) == Narrow:
				k.narrowLayers(c, s, sp)
			default:
				k.columns(c, s, sp, w)
			}
		})
	})
	if s.float.live {
		k.floatPanel(c, s, sp, floating)
	}
	if sp.Overlays != nil {
		sp.Overlays()
	}
	_ = g
}

// pageHeader 是记忆、运行这些页的顶行：页名和窗口按钮同一行，页面内容从它下面开始，不被挡。
func (k *Kit) pageHeader(c *ui.Context, sp *Spec) {
	g := k.G
	title := ""
	for _, it := range sp.Nav {
		if it.ID == sp.Page {
			title = it.Label
		}
	}
	inset := k.windowInset(false)
	ui.Row(c.Key("page-header")).FillWidth().Height(inset+g.BarHeight).Shrink(0).Padding(inset, inset+k.windowRoom(), 0, g.PaneInset).AlignItems(ui.Center).DragWindow().Children(func() {
		ui.Text(c.Key("page-title"), title).FontSize(g.LabelSize).FixedLineHeight(g.MetaLine).FontWeight(g.StrongWeight).Grow(1).MinWidth(0).SingleLine()
	})
}

// shortcuts 是聊天页的两个快捷键：Ctrl+B 收起或展开会话列表，Ctrl+\ 开关右侧栏。
func (k *Kit) shortcuts(c *ui.Context, s *State, sp *Spec, w float32) {
	c.OnShortcut(ui.Cmd, ui.KeyB, func() {
		s.ToggleList()
		if sp.OnPanes != nil {
			sp.OnPanes()
		}
	})
	c.OnShortcut(ui.Cmd, ui.KeyBackslash, func() {
		k.ToggleSide(s, w)
		if sp.OnPanes != nil {
			sp.OnPanes()
		}
	})
}

// columns 是两栏和三栏：导航 | 会话列表 | 聊天 [| 右栏]，列表和右栏的分隔条都能拖。
// 列表分隔条是自己的抓取条（listGrip）：比 mygo 自带的宽，拖动时列表宽直接跟着指针，松手才决定收成头像列还是展开。
func (k *Kit) columns(c *ui.Context, s *State, sp *Spec, w float32) {
	g := k.G
	line := k.Theme // 分隔条平时不画线（几栏靠留缝分开），悬停和拖动时才亮强调色
	line.Border = ui.Transparent
	// 右栏分隔条按指针的绝对位置算宽度：Split 只给增量，拖过下限再拖回来会漂移
	cols := ui.Row(c.Key("columns")).Fill().AlignItems(ui.Stretch)
	cols.Children(func() {
		k.nav(c, s, sp)
		ui.Row(c.Key("list-wrap")).Grow(1).MinWidth(0).FillHeight().AlignItems(ui.Stretch).Children(func() {
			shown, grip := k.listGrip(c, s, sp, w)
			c.SetTheme(&line)
			split := ui.Split(c.Key("list-split"), &s.ListWidth, func() {
				c.SetTheme(&k.Theme)
				// 窄于 ListMin 时内容保持 ListMin 宽、靠左露出一部分，不跟着重排（拖动时内容不跳）；头像列按显示宽
				inner := max(shown, ListMin)
				if s.compactView {
					inner = shown
				}
				ui.Box(c.Key("list-reveal")).Width(inner).FillHeight().Children(func() { k.Sidebar(c, s, sp.Sidebar, false) })
			}, func() {
				c.SetTheme(&k.Theme)
				if k.SideMode(w) == PanelPush && s.Panes.SideOpen {
					available := w - g.NavWidth - s.ListWidth - SplitSize
					s.ChatWidth = available - k.SideReserve(s, w)
					c.SetTheme(&line)
					side := ui.Split(c.Key("side-split"), &s.ChatWidth, func() { c.SetTheme(&k.Theme); k.chatCard(c, s, sp, w) }, func() {
						c.SetTheme(&k.Theme)
						k.side(c, s, &sp.Side, sideColumn, nil)
					}).Fill()
					c.SetTheme(&k.Theme)
					if side.Changed() {
						want := available - s.ChatWidth - SplitSize
						if x, _, over := cols.PointerPosition(); over {
							want = w - x - SplitSize
						}
						s.Panes.SideWidth = min(max(want, SideMin), SideMax, available-ChatMin-SplitSize)
						c.AnimationFrame()
						if sp.OnPanes != nil {
							sp.OnPanes()
						}
					}
					return
				}
				k.chatCard(c, s, sp, w)
			}).Grow(1).MinWidth(0).FillHeight()
			c.SetTheme(&k.Theme)
			if split.Changed() { // 只有键盘方向键会走到这里：鼠标拖动被抓取条先拿走了
				grip.Jump("list-w", s.ListWidth)
				k.DragList(s, w, false)
				c.AnimationFrame()
				if sp.OnPanes != nil {
					sp.OnPanes()
				}
			}
		})
	})
}

// listGrip 建列表分隔条的抓取条并处理拖动，返回这一帧列表的显示宽。
// 抓取条在分隔线两侧各 ListGrab/2，叠在两栏上面、不占布局宽度；先建它，才能在量列表宽之前读到这一帧指针的位移（跟手，不晚一帧）。
// 拖动时列表宽 = 指针位置（夹在头像列宽和上限之间），内容按显示宽换成头像列；松手才改偏好，再用 RouteMotion 滑到落点。
func (k *Kit) listGrip(c *ui.Context, s *State, sp *Spec, w float32) (float32, ui.Element) {
	g, p := k.G, k.P
	grip := ui.Box(c.Key("list-grip")).Absolute().Top(0).Bottom(0).Width(ListGrab).ZIndex(2).Cursor(ui.CursorResizeEW).Role(ui.RoleNone).Label("拖动调整会话列表宽度，双击收起或展开")
	dx, _, held := grip.Dragged()
	d := &s.listDrag
	body := w - g.NavWidth
	switch {
	case held && !d.active:
		d.active, d.moved, d.raw = true, false, s.listShown
		if d.raw <= 0 {
			d.raw = s.ListWidth
		}
		fallthrough
	case held:
		if dx != 0 {
			d.raw, d.moved = d.raw+dx, true
		}
	case d.active: // 松手
		d.active = false
		if d.moved && body > 0 {
			if d.raw < ListCompactDrag {
				s.Panes.Compact = true
			} else {
				s.Panes.Compact = false
				s.Panes.ListRatio = min(max(d.raw, ListMin), k.listUpper(s, w)) / body
			}
			s.listDragged = true
			k.Plan(s, w, false)
			c.AnimationFrame()
			if sp.OnPanes != nil {
				sp.OnPanes()
			}
		}
	}
	var shown float32
	if d.active {
		shown = grip.Jump("list-w", min(max(d.raw, g.ListCompactWidth), k.listUpper(s, w)))
	} else {
		// 松手后用弹簧落到位，接着拖动时最后的速度（Jump 记着），不是从静止起步
		shown = grip.AnimateTo("list-w", s.ListWidth, ui.Animation{Spring: k.T.Smooth})
	}
	if grip.DoubleClicked() {
		s.ToggleList()
		k.Plan(s, w, false)
		c.AnimationFrame()
		if sp.OnPanes != nil {
			sp.OnPanes()
		}
	}
	s.listShown, s.ListWidth = shown, shown
	s.compactView = shown < ListCompactDrag
	grip.Left(shown - ListGrab/2)
	hot := held || grip.Hovered()
	op := float32(0)
	if hot {
		op = 1
	}
	op = grip.AnimateTo("grip-o", op, ControlFade)
	grip.Draw(func(pt *ui.Painter, r ui.Rect) {
		if op <= 0 {
			return
		}
		pt.Fill(ui.Rect{X: r.X + (r.W-2)/2, Y: r.Y, W: 2, H: r.H}, p.Accent.Alpha(.7*op), 1)
	})
	return shown, grip
}

// narrowLayers 是窄窗口三层（列表 → 聊天 → 资料）：换层时新的一层滑进来并淡入，往里走从右边、返回从左边。
// 每层按层号起 key，换层就是新元素，mygo 给它播进场；刚启动那一帧不播。
func (k *Kit) narrowLayers(c *ui.Context, s *State, sp *Spec) {
	g := k.G
	if !s.layerSeen { // 第一帧只记下当前层，不当成换层
		s.layerShown, s.layerSeen = s.Layer, true
	}
	if s.Layer != s.layerShown {
		s.layerDir = 1
		if s.Layer < s.layerShown {
			s.layerDir = -1
		}
		s.layerShown = s.Layer
	}
	layer := ui.Box(c.Key("layer-" + strconv.Itoa(int(s.Layer)))).Fill()
	if s.layerDir != 0 {
		layer.Transition(ui.ElementTransition{Animation: &RouteMotion, Enter: &ui.Motion{X: s.layerDir * g.SpaceLarge, Opacity: 0}})
	}
	layer.Children(func() {
		w, _ := c.Size()
		switch s.Layer {
		case LayerList:
			ui.Row(c.Key("list-layer")).Fill().AlignItems(ui.Stretch).Children(func() {
				k.nav(c, s, sp)
				ui.Box(c).Grow(1).MinWidth(0).FillHeight().Children(func() { k.Sidebar(c, s, sp.Sidebar, true) })
			})
		case LayerChat:
			k.chat(c, s, sp, w, true)
		case LayerSide:
			k.side(c, s, &sp.Side, sideLayer, s.CloseSide)
		}
	})
}

// floatGap 是浮动面板离窗口上、右、下边的缝（设计语言第二节第 3 层：侧边面板，8 到 16 之间取小的）。
func (k *Kit) floatGap() float32 { return k.G.SpaceSmall }

// floatPanel 是放不下三栏时浮在聊天上的右栏（PanelFloat）：一张大圆角的玻璃面板，四周离窗口 floatGap，不压暗聊天、不拦聊天区的点击和滚动。
// 层级：聊天区 < 浮动面板 < 窗口按钮胶囊 < 弹窗；面板盖住聊天右边一段（含标题条和输入框的右端），要用被盖的部分先关面板（Esc、关闭钮、Ctrl+\）。
// 胶囊在同一个浮层里画在面板上面，位置和聊天区那只重合；面板顶行的选项卡胶囊让出它的宽。
// 进场、退场都滑动加淡；退场时 open 为假，内容照 s.float.spec（最后开着时的样子）画到动画结束才撤，不跟着宿主新状态变空。
// 浮层和里面的东西是同一帧新出现的，mygo 的 Transition 进场不播，所以自己做：第一帧在右边外面、全透明，之后滑回原位。
func (k *Kit) floatPanel(c *ui.Context, s *State, sp *Spec, open bool) {
	g, p := k.G, k.P
	f := &s.float
	first := !f.entered
	f.entered = true
	w, _ := c.Size()
	gap := k.floatGap()
	width := min(s.SideShown, w-2*gap)
	spec := &f.spec
	if open {
		spec = &sp.Side
	}
	ui.Overlay(c, func() {
		fill := ui.Box(c.Key("float-fill")).Absolute().Right(gap).Top(gap).Bottom(gap).Width(width).Radius(g.RadiusCard)
		k.Glass(fill, LevelInput, p.Composer)
		k.FloatShadow(fill)
		panel := ui.Column(c.Key("float-panel")).Absolute().Right(gap).Top(gap).Bottom(gap).Width(width).Clip().Radius(g.RadiusCard).Label("资料与活动")
		if panel.OverlayShortcut(0, ui.KeyEscape) && open && (spec.Escape == nil || !spec.Escape()) {
			s.CloseSide()
		}
		tx, op := width+gap, float32(0)
		if open && !first {
			tx, op = 0, 1
		}
		move, fade := RouteMotion, PanelFade
		if !open { // 退场比进场快：230 进 / 200 出（令牌 motion.panel、motion.panel-out）
			move, fade = PanelOutMotion, PanelFadeOut
		}
		x, o := panel.AnimateTo("float-x", tx, move), panel.AnimateTo("float-o", op, fade)
		fill.Translate(x, 0).Opacity(o)
		panel.Translate(x, 0).Opacity(o)
		k.surface(c, panel, "float-panel", "card", g.RadiusCard, 0).Children(func() { k.side(c, s, spec, sideFloat, s.CloseSide) })
		capsule := k.windowCapsule(c, k.windowInset(false))
		// 点外面收起，这一下照样落到下面的东西上（和弹出层一样）。三个键在同一层，不算外面
		if open && panel.PressedOutside() && !capsule.PressedInside() && (spec.KeepOpen == nil || !spec.KeepOpen()) {
			s.CloseSide()
		}
		if !open && panel.Settled("float-x") && panel.Settled("float-o") {
			f.live = false
		}
	})
}

// ChatOnly 只画聊天区（没有导航、列表、右栏），占满给它的空间；测试和只要消息列的场合用。
func (k *Kit) ChatOnly(c *ui.Context, s *State, cs ChatSpec) {
	k.Reduce = c.Preferences().ReduceTransparency
	w, _ := c.Size()
	k.chat(c, s, &Spec{Conv: cs}, w, false)
}

// chatCard 是分栏时的聊天窗：一张浮起的圆角卡，上下右留一个 SpaceSmall 的缝，左边贴着会话列表（照 Bun .su-app-main、.su-chat）。
// 窄窗口是整层铺满，不走这里。
func (k *Kit) chatCard(c *ui.Context, s *State, sp *Spec, w float32) {
	// 铺满，不留缝（ADR-326）：窗口的圆角由系统裁，卡片不再自己处理角
	ui.Box(c.Key("chat-slot")).Fill().Children(func() {
		card := ui.Box(c.Key("chat-card")).Fill()
		k.surface(c, card, "chat-card", "card", 0, 0).Children(func() { k.chat(c, s, sp, w, false) })
	})
}

// chat 是聊天区：聊天底（加图案）上，消息列整块铺满，标题栏浮在上面，输入区浮在下面。
// 消息列版心居中、最宽 752；窄时两边只留小内距。
func (k *Kit) chat(c *ui.Context, s *State, sp *Spec, w float32, narrow bool) {
	g, p := k.G, k.P
	cs := &sp.Conv
	changed := s.fade.chatSeen && s.fade.chat != cs.Key // 换了会话：消息列和标题栏淡入（窗口按钮留在外面不动）
	s.fade.chat, s.fade.chatSeen = cs.Key, true
	if cs.Key == "" {
		empty := ui.Column(c.Key("chat-empty")).Fill().Clip().Background(k.T.C.ChatBg).Center()
		if k.S.Wallpaper { // 没选会话也是同一个聊天窗，图案不消失
			empty.Draw(func(pt *ui.Painter, rect ui.Rect) { k.Wall.Paint(pt, rect, k.T, chatui.DefaultChar) })
		}
		empty.Children(func() {
			ui.Box(c.Key("chat-empty-drag")).Absolute().Left(0).Right(0).Top(0).Height(g.HeaderHeight).DragWindow()
			if cs.Empty != "" {
				ui.Text(c, cs.Empty).TextColor(p.Muted)
			}
			k.windowCapsule(c, k.windowInset(narrow))
		})
		return
	}
	e := ui.Box(c.Key("chat-" + cs.Key)).Fill().Clip().Bind(&s.Area).Background(k.T.C.ChatBg)
	if k.S.Wallpaper {
		glow := cs.Header.Glow
		e.Draw(func(pt *ui.Painter, rect ui.Rect) { k.Wall.Paint(pt, rect, k.T, glow) })
	}
	if cs.OnArea != nil {
		cs.OnArea(e)
	}
	width := s.Area.Bounds(c).W
	if width <= 0 {
		width, _ = c.Size()
	}
	inset := k.windowInset(narrow) // 消息、标题栏浮条、输入框三者左右边线对齐；上下左右离聊天窗一样远，和窗口按钮胶囊同一条线
	extra := inset - g.SpaceSmall  // 比一个缝多出来的：标题栏和输入区的高度都按一个缝量的
	s.lastChatW = width
	if s.ComposerH <= 0 {
		s.ComposerH = g.ComposerMin + g.SpacePanel
	}
	column := width - inset*2
	switch pct := cs.WidthPct; {
	case pct == 0: // 没设：原型的居中版式，最宽 752
		column = min(ContentWidth, column)
	case pct >= 40 && pct < 100 && width >= NarrowContentWidth:
		column = column * pct / 100
	}
	horizontal := max(inset, (width-column)/2)
	s.overlayTop, s.overlayBottom = g.HeaderHeight+extra, s.Composer.Bounds(c).H+inset
	l := ChatLayout{Column: column, Horizontal: horizontal, Inset: inset, Top: g.HeaderHeight + extra + g.SpacePanel, Bottom: s.ComposerH + inset, Narrow: narrow}
	e.Children(func() {
		content := ui.Box(c.Key("chat-content")).Fill()
		k.fadeIn(content, changed || s.fade.jump)
		content.Children(func() {
			if cs.Body != nil {
				cs.Body(l)
			}
			k.header(c, s, &cs.Header, l, cs.OnBack, func() {
				k.ToggleSide(s, w)
				if sp.OnPanes != nil {
					sp.OnPanes()
				}
			})
		})
		if !s.sideOwnsButtons(k, w) { // 右栏并排时三个键在右栏的顶行里；浮动面板在时由浮层画
			k.windowCapsule(c, inset)
		}
	})
	vw, vh := c.Size()
	if s.lastW != width || s.lastH != vh || s.lastVW != vw {
		s.lastW, s.lastH, s.lastVW = width, vh, vw
		c.AnimationFrame()
	}
	if height := s.Composer.Bounds(c).H + g.SpacePanel; height > g.SpacePanel && height != s.ComposerH {
		s.ComposerH = height
		c.AnimationFrame()
	}
}

// ComposerSlot 是输入区的位置：浮在聊天区底部、版心居中，量它的高度让消息列底边跟着让位。
// 输入区本身归宿主画（internal/chatui 的 Composer）。
func (k *Kit) ComposerSlot(c *ui.Context, s *State, l ChatLayout, fill func()) {
	ui.Column(c.Key("composer-frame")).Absolute().Left(l.Horizontal).Right(l.Horizontal).Bottom(l.Inset).Bind(&s.Composer).Children(fill)
}

// MenuTheme 是消息右键菜单用的主题：高亮和别的菜单一样是中性浅底，不是强调色（主人 2026-11-09）。
func (k *Kit) MenuTheme() *ui.Theme {
	k.menuTheme = k.Theme
	k.menuTheme.Accent, k.menuTheme.AccentText = k.P.Selected, k.P.Ink
	return &k.menuTheme
}
