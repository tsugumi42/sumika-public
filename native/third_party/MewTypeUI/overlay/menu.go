package overlay

import (
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// MenuItem IDs must be unique among siblings, including separators. Children
// describe a submenu; the host owns checked/radio state and accepts leaf IDs.
type MenuItem struct {
	ID, Label, Shortcut                                    string
	Icon                                                   *ui.SVG
	Disabled, Danger, Separator, Checked, Checkable, Radio bool
	Children                                               []MenuItem
}
type MenuOptions struct {
	Label                     string
	Icons, ExpandedSeparators bool
	OnSelect                  func(id string)
	OnClose                   func()
}
type MenuState struct {
	Open     bool
	Point    ui.Point
	keyboard bool
	serial   uint64
	opener   ui.Handle
	restore  bool
}

func (s *MenuState) ShowAt(point ui.Point) {
	s.Point, s.Open, s.keyboard = point, true, false
	s.serial++
}
func (s *MenuState) Close() { s.Open = false }

// ContextMenu opens a native flyout at a secondary click, Menu or Shift+F10.
// Build after target with a stable key inside Native.
func ContextMenu(c *ui.Context, target ui.Element, items []MenuItem, state *MenuState, o MenuOptions) ui.Element {
	if target.RightClicked() {
		x, y, _ := target.PointerPosition()
		state.ShowAt(ui.Point{X: x, Y: y})
	}
	if target.Shortcut(ui.Shift, ui.KeyF10) || target.Shortcut(0, ui.KeyContextMenu) {
		state.ShowAt(ui.Point{Y: target.Bounds().H})
		state.keyboard = true
	}
	return menu(c, target, items, state, o, false)
}

// PopupMenu toggles a native flyout below target on click, Enter, Space or Down.
func PopupMenu(c *ui.Context, target ui.Element, items []MenuItem, state *MenuState, o MenuOptions) ui.Element {
	target.Role(ui.RoleMenuButton).Expanded(state.Open)
	if target.Clicked() {
		if state.Open {
			state.Close()
		} else {
			state.ShowAt(ui.Point{})
			state.keyboard = target.FocusVisible()
		}
	}
	if target.Shortcut(0, ui.KeyDown) {
		state.ShowAt(ui.Point{})
		state.keyboard = true
	}
	return menu(c, target, items, state, o, true)
}

func menu(c *ui.Context, target ui.Element, items []MenuItem, state *MenuState, o MenuOptions, below bool) ui.Element {
	root := ui.Box(c).Size(0, 0)
	target.Bind(&state.opener)
	if state.restore {
		state.opener.Focus(c)
		state.restore = false
	}
	at := target.Bounds()
	if !below {
		at = ui.Rect{X: at.X + state.Point.X, Y: at.Y + state.Point.Y}
	}
	if !state.Open {
		// Closed components do not require a native binding (e.g. static previews).
		slot := ui.Local(c.Root(), nativeKey{}, func() nativeSlot { return nativeSlot{} })
		if slot.host != nil && slot.host.menu != nil && slot.host.menu.state == state {
			slot.host.menu.close(true)
			slot.host.menuSeen = true
		}
		return root
	}
	h := native(c)
	h.menuSeen = true
	if h.menu != nil && h.menu.state == state && h.menu.serial == state.serial && !h.menu.closing {
		h.menu.items, h.menu.options = items, o
		if len(h.menu.levels) > 0 {
			level := h.menu.levels[0]
			if anchor := rectangle(at); anchor != level.anchor {
				level.anchor = anchor
				level.window.SetAnchor(anchor)
			}
		}
		return root
	}
	if h.menu != nil {
		h.menu.close(false)
	}
	if h.tip != nil {
		h.tip.close()
	}
	placement := mygo.PlacementBottomStart
	if c.Direction() == ui.RightToLeft {
		placement = mygo.PlacementBottomEnd
	}
	m := &nativeMenu{host: h, state: state, serial: state.serial, items: items, options: o, direction: c.Direction()}
	h.menu = m
	width, height, _ := menuSize(c, items, o)
	// Build callbacks may run repeatedly. Ownership above guards this queued job.
	h.window.Update(func() {
		if h.menu != m || !state.Open {
			return
		}
		m.open(h.window, rectangle(at), placement, width, height, "", state.keyboard)
	})
	return root
}

type nativeMenu struct {
	host      *nativeHost
	state     *MenuState
	serial    uint64
	items     []MenuItem
	options   MenuOptions
	direction ui.Direction
	levels    []*nativeMenuLevel
	closing   bool
}
type nativeMenuLevel struct {
	window        *mygo.Window
	parent        string
	panel         menuPanel
	width, height int
	anchor        mygo.Rectangle
}

func (m *nativeMenu) open(parent *mygo.Window, at mygo.Rectangle, placement mygo.Placement, width, height float32, id string, keyboard bool) {
	level := &nativeMenuLevel{parent: id, panel: menuPanel{open: true, focus: keyboard, keyboard: keyboard}, width: extent(width), height: extent(height), anchor: at}
	depth := len(m.levels)
	m.levels = append(m.levels, level)
	level.window = mygo.NewFlyout(mygo.FlyoutOptions{
		Parent: parent, Anchor: at, Placement: placement, Width: extent(width), Height: extent(height),
		Focusable: true, Hidden: true, Shadow: true,
		CornerRadius: int(m.host.theme.Metrics().Menu().Radius()),
		Content: ui.View(func(c *ui.Context) {
			theme.Apply(c, m.host.theme)
			c.Root().Direction(m.direction)
			c.Root().Background(ui.Transparent)
			items := m.levelItems(depth)
			if items == nil {
				m.host.window.Update(func() { m.trim(depth) })
				return
			}
			level.panel.render(c, items, m.options, menuActions{
				selectID: func(id string) {
					if m.options.OnSelect != nil {
						m.options.OnSelect(id)
					}
					m.close(true)
				},
				close: func() {
					if depth == 0 {
						m.close(true)
					} else {
						m.host.window.Update(func() { m.trim(depth) })
					}
				},
				child: func(id string, row ui.Rect, keyboard bool) {
					if depth+1 < len(m.levels) && m.levels[depth+1].parent == id {
						return
					}
					item := findItem(items, id)
					if item == nil || len(item.Children) == 0 {
						if depth+1 < len(m.levels) {
							m.host.window.Update(func() { m.trim(depth + 1) })
						}
						return
					}
					w, h, _ := menuSize(c, item.Children, m.options)
					pad := m.host.theme.Metrics().Menu().ScrollPadding()
					if m.options.Icons {
						pad = m.host.theme.Metrics().Menu().IconScrollPadding()
					}
					row.Y -= pad
					place := mygo.PlacementRightStart
					if m.direction == ui.RightToLeft {
						place = mygo.PlacementLeftStart
					}
					m.host.window.Update(func() {
						if m.closing || depth >= len(m.levels) || m.levels[depth] != level {
							return
						}
						m.trim(depth + 1)
						m.open(level.window, rectangle(row), place, w, h, id, keyboard)
					})
				},
				childBounds: func() ui.Rect {
					if depth+1 == len(m.levels) {
						return ui.Rect{}
					}
					r, p := m.levels[depth+1].window.Bounds(), level.window.ContentBounds()
					return ui.Rect{X: float32(r.X - p.X), Y: float32(r.Y - p.Y), W: float32(r.Width), H: float32(r.Height)}
				},
				done: func() { m.host.window.Update(m.destroy) },
			})
			if width, height := extent(level.panel.width), extent(level.panel.height); !m.closing && (width != level.width || height != level.height) {
				level.width, level.height = width, height
				level.window.Update(func() { level.window.SetContentSize(width, height) })
			}
		}),
	})
	level.window.SetTitle("MewTypeUI menu")
	placed, owner := level.window.Bounds(), parent.ContentBounds()
	level.panel.flipX = placed.X+placed.Width <= owner.X+at.X+at.Width/2
	level.panel.flipY = placed.Y+placed.Height <= owner.Y+at.Y+at.Height/2
	level.window.OnReadyToShow(func() {
		if !m.closing {
			level.window.Show()
		}
	})
	level.window.OnClose(func(e *mygo.CloseEvent) {
		e.PreventDefault()
		focused := mygo.FocusedWindow()
		for i, other := range m.levels {
			if other.window == focused && i != depth {
				if i < depth {
					m.host.window.Update(func() { m.trim(depth) })
				}
				return
			}
		}
		m.close(true)
	})
	level.window.OnClosed(func() {
		if depth == 0 && !m.closing {
			m.close(false)
		}
	})
}
func (m *nativeMenu) levelItems(depth int) []MenuItem {
	items := m.items
	for i := 1; i <= depth; i++ {
		it := findItem(items, m.levels[i].parent)
		if it == nil || it.Disabled || len(it.Children) == 0 {
			return nil
		}
		items = it.Children
	}
	return items
}
func (m *nativeMenu) invalidate() {
	for _, level := range m.levels {
		level.window.Invalidate()
	}
}
func (m *nativeMenu) trim(depth int) {
	if depth >= len(m.levels) {
		return
	}
	removed := m.levels[depth:]
	m.levels = m.levels[:depth]
	for i := len(removed) - 1; i >= 0; i-- {
		removed[i].window.Destroy()
	}
	if depth > 0 {
		parent := m.levels[depth-1]
		parent.panel.focus = true
		parent.window.Focus()
		parent.window.Invalidate()
	}
}
func (m *nativeMenu) close(animate bool) {
	if m.closing {
		if !animate {
			m.host.window.Update(m.destroy)
		}
		return
	}
	m.closing = true
	focused := mygo.FocusedWindow()
	for _, level := range m.levels {
		if focused == level.window {
			m.state.restore = true
			break
		}
	}
	m.state.Close()
	if m.options.OnClose != nil {
		m.options.OnClose()
	}
	m.host.window.Invalidate()
	if !animate || len(m.levels) == 0 {
		m.host.window.Update(m.destroy)
		return
	}
	for _, level := range m.levels {
		level.panel.open = false
		level.window.SetIgnoreMouseEvents(true)
		level.window.Invalidate()
	}
}
func (m *nativeMenu) destroy() {
	if m.host.menu == m {
		m.host.menu = nil
	}
	levels := m.levels
	m.levels = nil
	for i := len(levels) - 1; i >= 0; i-- {
		levels[i].window.Destroy()
	}
}

type menuActions struct {
	selectID    func(string)
	close       func()
	child       func(string, ui.Rect, bool)
	childBounds func() ui.Rect
	done        func()
}
type menuPanel struct {
	open, focus, initialized bool
	active, prefix, pending  string
	childRequest             string
	typed, shown, aimStarted time.Time
	pointer, aimFrom         ui.Point
	pointerKnown             bool
	mouse                    ui.Point
	mouseKnown, keyboard     bool
	flipX, flipY             bool
	visible                  visibility
	handle                   ui.Handle
	width, height            float32
}

// render is the same content in native flyouts and headless behavior tests.
func (s *menuPanel) render(c *ui.Context, items []MenuItem, o MenuOptions, actions menuActions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Menu()
	if !s.initialized {
		s.initialized = true
		s.shown = c.Now()
		s.handle.Focus(c)
	}
	alpha := s.visible.update(c, s.open, t.Motion().MenuShow(), t.Motion().Menu())
	if !s.open && alpha == 0 {
		actions.done()
		return ui.Box(c).Size(0, 0)
	}
	if it := findItem(items, s.active); it == nil || it.Disabled || it.Separator {
		s.active = ""
	}
	if s.focus && s.active == "" {
		s.active = nextItem(items, "", 1)
	}
	width, height, rowHeight := menuSize(c, items, o)
	s.width, s.height = width, height
	vw, vh := c.Size()
	width, height = min(width, vw), min(height, vh)
	pad := m.ScrollPadding()
	if o.Icons {
		pad = m.IconScrollPadding()
	}
	outer := ui.Box(c).Size(width, height).Role(ui.RoleMenu).Label(o.Label).Bind(&s.handle).Focusable().FocusRing(false).IgnoreInput(!s.open)
	// Rows move during scroll/reveal; only motion in the fixed window box
	// hands a keyboard selection back to hover.
	if x, y, over := outer.PointerPosition(); over {
		point := ui.Point{X: x, Y: y}
		if s.mouseKnown && point != s.mouse {
			s.keyboard = false
		}
		s.mouse, s.mouseKnown = point, true
	}
	back, forth := ui.KeyLeft, ui.KeyRight
	if c.Direction() == ui.RightToLeft {
		back, forth = forth, back
	}
	typeAhead := func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown && ev.Mods == 0 && s.open {
			switch ev.Key {
			case ui.KeyEscape, back:
				actions.close()
			case ui.KeyEnter:
				if active := findItem(items, s.active); active != nil {
					if len(active.Children) > 0 {
						s.childRequest = active.ID
					} else {
						actions.selectID(active.ID)
					}
				}
			case ui.KeyDown:
				s.active, s.focus = nextItem(items, s.active, 1), true
			case ui.KeyUp:
				s.active, s.focus = nextItem(items, s.active, -1), true
			case ui.KeyHome:
				s.active, s.focus = nextItem(items, "", 1), true
			case ui.KeyEnd:
				s.active, s.focus = nextItem(items, "", -1), true
			case forth:
				if active := findItem(items, s.active); active != nil && len(active.Children) > 0 {
					s.childRequest = active.ID
				}
			default:
				return false
			}
			c.Invalidate()
			s.keyboard = true
			return true
		}
		if ev.Kind != ui.InputText || ev.Text == "" {
			return false
		}
		if ev.Time.Sub(s.typed) > t.Motion().TooltipDelay().Duration() {
			s.prefix = ""
		}
		s.typed, s.prefix = ev.Time, s.prefix+strings.ToLower(ev.Text)
		s.keyboard = true
		for _, candidate := range items {
			if !candidate.Disabled && !candidate.Separator && strings.HasPrefix(strings.ToLower(candidate.Label), s.prefix) {
				s.active, s.focus = candidate.ID, true
				break
			}
		}
		return true
	}
	outer.HandleInput(typeAhead)
	p := progress(c, s.shown, t.Motion().MenuShow())
	w, h, frameAlpha := width, height, alpha
	if s.open {
		frameAlpha = 1
		if p < 1 {
			elapsed := float32(c.Now().Sub(s.shown)) / float32(t.Motion().MenuShow().Duration())
			curve := t.Motion().MenuShow().Easing().Curve()
			w *= m.StartWidth() + (1-m.StartWidth())*curve(portion(elapsed, m.WidthFraction()))
			h *= m.StartHeight() + (1-m.StartHeight())*curve(portion(elapsed, m.HeightFraction()))
			frameAlpha = curve(portion(elapsed, m.OpacityFraction()))
		}
	}
	origin := ui.AnchorTopLeft
	if s.flipX {
		origin = ui.AnchorTopRight
	}
	if s.flipY {
		origin = ui.AnchorBottomLeft
		if s.flipX {
			origin = ui.AnchorBottomRight
		}
	}
	outer.Children(func() {
		visible := ui.Column(c).Size(w, h).Attach(origin, origin).Radius(m.Radius()).Opacity(frameAlpha).Clip()
		material(visible, t.Materials().Menu())
		visible.Children(func() {
			scroll := ui.Scroll(c).Width(width).Height(height).Absolute().Padding(pad, 0).AlignItems(ui.Stretch)
			if s.flipX {
				scroll.Right(0)
			} else {
				scroll.Left(0)
			}
			if s.flipY {
				scroll.Bottom(0)
			} else {
				scroll.Top(0)
			}
			scroll.Children(func() {
				for _, item := range items {
					if item.Separator {
						padding, thickness := m.SeparatorPadding(), m.SeparatorWidth()
						if o.ExpandedSeparators {
							padding, thickness = m.ExpandedSeparatorPadding(), m.ExpandedSeparatorWidth()
						}
						ui.Box(c.Key(item.ID)).FillWidth().Height(thickness).Margin(padding, 0).Background(t.Colors().Menu().SeparatorForeground()).Shrink(0)
						continue
					}
					row := ui.ButtonBase(c.Key(item.ID)).Label(item.Label).Role(ui.RoleMenuItem).Width(width).Height(rowHeight).Disabled(item.Disabled).FocusRing(false).Justify(ui.Start)
					if item.Checkable || item.Radio {
						role := ui.RoleMenuItemCheckBox
						if item.Radio {
							role = ui.RoleMenuItemRadio
						}
						row.Role(role).Checked(item.Checked)
					}
					row.HandleInput(typeAhead)
					if row.Hovered() && !item.Disabled && s.open && !s.keyboard {
						x, y, _ := row.PointerPosition()
						point := ui.Point{X: x + row.Bounds().X, Y: y + row.Bounds().Y}
						if !s.pointerKnown || s.pointer != point || s.pending == item.ID {
							wait := false
							child := actions.childBounds()
							if child.W > 0 && s.active != item.ID && s.pointerKnown {
								if s.pending != item.ID {
									s.pending, s.aimStarted, s.aimFrom = item.ID, c.Now(), s.pointer
								}
								remaining := t.Motion().SubmenuAim().Duration() - c.Now().Sub(s.aimStarted)
								wait = remaining > 0 && aimingAt(s.aimFrom, point, child)
								if wait {
									c.After(remaining)
								}
							}
							s.pointer, s.pointerKnown = point, true
							if !wait {
								s.pending = ""
								if s.active != item.ID {
									s.active = item.ID
									c.Invalidate()
								}
								actions.child(item.ID, row.Bounds(), false)
							}
						}
					}
					selected := s.active == item.ID
					bg, fg, shortcut := t.Colors().Window().Background(), t.Colors().Window().Text(), t.Colors().Window().MutedText()
					if selected {
						bg, fg, shortcut = t.Colors().Window().BackgroundHover(), t.Colors().Window().ForegroundHover(), t.Colors().Window().SubTextForegroundHover()
					}
					if item.Danger {
						fg = t.Colors().Control().DangerForeground()
						if selected {
							fg = t.Colors().Control().DangerForegroundHover()
						}
					}
					if item.Disabled {
						fg = t.Colors().Menu().ForegroundDisabled()
						shortcut = fg
					}
					left, bottom := m.PaddingLeft(), m.PaddingBottom()
					if o.Icons {
						left, bottom = m.IconPaddingLeft(), m.IconPaddingBottom()
					}
					row.Background(bg).TextColor(fg).Padding(m.PaddingTop(), 0, bottom, 0).PaddingStart(left).PaddingEnd(m.PaddingRight())
					row.Children(func() {
						label := text(c, item.Label, t.Typography().Menu(), fg).Grow(1).SingleLine()
						if len(item.Children) > 0 {
							label.MarginEnd(m.IconSize())
						}
						if item.Shortcut != "" {
							text(c, item.Shortcut, t.Typography().Menu(), shortcut).SingleLine().Margin(0, 0, 0, m.RightSkip())
						}
						if item.Radio {
							// paint/dropdown.md#popup几何与画法; paint/radio-check.md#radio圆环与内点.
							cm := t.Metrics().Choice()
							mark := ui.Box(c).Size(cm.RadioDiameter(), cm.RadioDiameter()).Shrink(0).IgnoreInput(true)
							a := float32(0)
							if item.Checked {
								a = 1
							}
							a = mark.AnimateTo("checked", a, ui.Animation{Duration: t.Motion().Choice().Duration(), Ease: t.Motion().Choice().Easing().Curve()})
							ink := t.Colors().Control().CheckboxForeground().Mix(t.Colors().Window().BackgroundActive(), a)
							path := ui.Local(mark, "radio-ring", func() ui.Path { return ui.Path{} })
							path.Reset().Circle(0, 0, cm.RadioDiameter()/2-cm.RadioOuterInset()-cm.RadioStroke()/2)
							mark.Draw(func(p *ui.Painter, r ui.Rect) {
								p.Transform(ui.Translation(r.X+r.W/2, r.Y+r.H/2), 0, 0, func() {
									p.StrokePath(path, cm.RadioStroke(), ink)
									d := (cm.RadioDiameter() - 2*cm.RadioDotInset()) * a
									p.Fill(ui.Rect{X: -d / 2, Y: -d / 2, W: d, H: d}, ink, d/2)
								})
							})
						} else if item.Checked {
							draw.Element(c, checkIcon, m.IconSize(), m.IconSize()).TextColor(fg)
						}
						if len(item.Children) > 0 {
							icon := rightIcon
							if c.Direction() == ui.RightToLeft {
								icon = leftIcon
							}
							arrow := draw.Element(c, icon, m.IconSize(), m.IconSize()).Absolute().Top((rowHeight - m.IconSize()) / 2).TextColor(t.Colors().Window().BoldForeground())
							if c.Direction() == ui.RightToLeft {
								arrow.Left(m.RightSkip())
							} else {
								arrow.Right(m.RightSkip())
							}
						}
						if item.Icon != nil && o.Icons {
							icon := draw.Element(c, item.Icon, m.IconSize(), m.IconSize()).Absolute().Top(m.IconTop()).TextColor(fg)
							if c.Direction() == ui.RightToLeft {
								icon.Right(m.IconLeft())
							} else {
								icon.Left(m.IconLeft())
							}
						}
					})
					ripple(c, row, t.Colors().Window().BackgroundPressed())
					if selected && s.focus {
						handle := ui.Local(row, "focus", func() ui.Handle { return ui.Handle{} })
						row.Bind(handle)
						handle.Focus(c)
						row.ScrollIntoView()
						s.focus = false
					}
					if s.childRequest == item.ID {
						s.childRequest = ""
						actions.child(item.ID, row.Bounds(), true)
					}
					if row.Clicked() {
						if len(item.Children) > 0 {
							actions.child(item.ID, row.Bounds(), true)
						} else {
							actions.selectID(item.ID)
						}
					}
				}
			})
		})
		if s.open && p < 1 {
			visible.DrawOver(func(painter *ui.Painter, r ui.Rect) {
				band := height * m.FadeHeight()
				if band == 0 {
					return
				}
				start := min(height*p, r.H)
				bg := t.Materials().Menu().Background()
				gradient := ui.Rect{X: r.X, Y: r.Y + start, W: r.W, H: band}
				cover := ui.Rect{X: r.X, Y: r.Y + start + band, W: r.W, H: max(0, r.H-start-band)}
				from, to := bg.Alpha(0), bg
				if s.flipY {
					gradient.Y = r.Y + r.H - start - band
					cover.Y = r.Y
					from, to = to, from
				}
				painter.Clip(r, m.Radius(), func() {
					painter.FillGradient(gradient, ui.LinearGradient{From: from, To: to, Angle: 180}, 0)
					painter.Fill(cover, bg, 0)
				})
			})
		}
	})
	return outer
}

func findItem(items []MenuItem, id string) *MenuItem {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}
func nextItem(items []MenuItem, active string, direction int) string {
	start := -1
	for i := range items {
		if items[i].ID == active {
			start = i
			break
		}
	}
	if start < 0 && direction < 0 {
		start = 0
	}
	for n := 1; n <= len(items); n++ {
		i := (start + direction*n + len(items)) % len(items)
		if !items[i].Separator && !items[i].Disabled {
			return items[i].ID
		}
	}
	return ""
}
func menuSize(c *ui.Context, items []MenuItem, o MenuOptions) (width, height, rowHeight float32) {
	t := theme.T(c)
	m := t.Metrics().Menu()
	_, line := measure(c, "Mg", t.Typography().Menu(), 0)
	left, bottom, pad := m.PaddingLeft(), m.PaddingBottom(), m.ScrollPadding()
	if o.Icons {
		left, bottom, pad = m.IconPaddingLeft(), m.IconPaddingBottom(), m.IconScrollPadding()
	}
	rowHeight, height, width = line+m.PaddingTop()+bottom, pad*2, m.MinWidth()
	for _, item := range items {
		if item.Separator {
			if o.ExpandedSeparators {
				height += m.ExpandedSeparatorPadding()*2 + m.ExpandedSeparatorWidth()
			} else {
				height += m.SeparatorPadding()*2 + m.SeparatorWidth()
			}
			continue
		}
		w, _ := measure(c, item.Label, t.Typography().Menu(), 0)
		if item.Shortcut != "" {
			extra, _ := measure(c, item.Shortcut, t.Typography().Menu(), 0)
			w += m.RightSkip() + extra
		}
		if len(item.Children) > 0 || item.Checked && !item.Radio {
			w += m.IconSize()
		}
		if item.Radio {
			w += t.Metrics().Choice().RadioDiameter()
		}
		width = max(width, left+w+m.PaddingRight())
		height += rowHeight
	}
	return min(width, m.MaxWidth()), height, rowHeight
}
func aimingAt(from, point ui.Point, child ui.Rect) bool {
	edge := child.X
	if from.X > child.X+child.W/2 {
		edge += child.W
	}
	a, b := ui.Point{X: edge, Y: child.Y}, ui.Point{X: edge, Y: child.Y + child.H}
	cross := func(a, b, p ui.Point) float32 { return (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X) }
	x, y, z := cross(from, a, point), cross(a, b, point), cross(b, from, point)
	return (x >= 0 && y >= 0 && z >= 0) || (x <= 0 && y <= 0 && z <= 0)
}
func portion(elapsed, span float32) float32 {
	if span == 0 {
		return 1
	}
	return min(elapsed/span, 1)
}
