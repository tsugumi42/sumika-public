package settings

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type ButtonTone uint8

const (
	Normal ButtonTone = iota
	Light
	Attention
)

type ButtonOptions struct {
	Icon                        *ui.SVG
	Value                       string
	Arrow, MutedValue, Disabled bool
	// Selected is the explicit CTL-CATEGORIES persistent navigation appearance.
	Selected   bool
	Tone       ButtonTone
	OnActivate func()
}
type buttonLocal struct {
	ripple pulse
	width  float32
}

// Button has one full-width hit target, including its icon and value.
func Button(c *ui.Context, title string, o ButtonOptions) ui.Element { return button(c, title, o, nil) }
func button(c *ui.Context, title string, o ButtonOptions, toggle func(ui.Element, float32)) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Settings()
	colors := t.Colors().Window()
	left, top, right, bottom := m.ButtonNoIconPaddingLeft(), m.ButtonNoIconPaddingTop(), m.ButtonNoIconPaddingRight(), m.ButtonNoIconPaddingBottom()
	if o.Icon != nil {
		left, top, right, bottom = m.ButtonPaddingLeft(), m.ButtonPaddingTop(), m.ButtonPaddingRight(), m.ButtonPaddingBottom()
	}
	_, line := measure(c, title, t.Typography().DialogBody(), 0)
	height := top + max(m.ButtonContentHeight(), line) + bottom
	b := ui.ButtonBase(c).FillWidth().Height(height).Shrink(0).Label(title).Value(o.Value).Disabled(o.Disabled)
	s := ui.Local(b, "settings-button", func() buttonLocal { return buttonLocal{} })
	over := !o.Disabled && (b.Hovered() || b.Pressed())
	bg, fg := colors.Background(), colors.BoldForeground()
	if over {
		bg, fg = colors.BackgroundHover(), colors.BoldForegroundHover()
	}
	if o.Tone == Light {
		fg = t.Colors().Control().SurfaceForeground()
		if over {
			fg = t.Colors().Control().SurfaceForegroundHover()
		}
	}
	if o.Tone == Attention {
		fg = t.Colors().Control().DangerForeground()
		if over {
			fg = t.Colors().Control().DangerForegroundHover()
		}
	}
	if o.Disabled {
		fg = colors.MutedText()
	}
	// paint/side-categories.md#常驻分类组合我定的: keep the selected
	// background through hover; the row still owns the only ripple/action.
	if o.Selected {
		bg, fg = colors.BackgroundHover(), colors.ActiveTextForeground()
	}
	s.ripple.update(c, b, !o.Disabled, nil)
	b.Background(bg).Draw(func(p *ui.Painter, r ui.Rect) { s.ripple.paint(p, r, colors.BackgroundPressed(), false) })
	// paint/settings-button.md#右侧当前值与截断优先级. Measure the natural title
	// first; only the value gives way until no positive value width remains.
	width := b.LayoutBounds().W
	if width != s.width {
		s.width = width
		c.AnimationFrame()
	}
	services := c.Services()
	b.DrawOver(func(_ *ui.Painter, r ui.Rect) {
		if r.W != width {
			services.Invalidate()
		}
	})
	natural, _ := measure(c, title, t.Typography().DialogBody(), 0)
	titleWidth := max(0, width-left-right)
	tail := m.ButtonValueRight()
	if o.Arrow {
		tail = right + t.Metrics().Header().IconSize() + t.Metrics().Menu().RightSkip()
		titleWidth = max(0, titleWidth-t.Metrics().Header().IconSize()-t.Metrics().Menu().RightSkip())
	}
	if toggle != nil {
		titleWidth = max(0, titleWidth-m.ToggleRight()-2*m.ToggleBorder()-m.ToggleDiameter()-m.ToggleTravel())
	}
	valueWidth := max(0, width-left-right-natural-tail)
	b.Children(func() {
		// paint/settings-button.md#行图标标题.
		mirror(c, text(c, title, t.Typography().DialogBody(), fg).SingleLine(), left, titleWidth).Top(top)
		if o.Icon != nil {
			size := t.Metrics().Header().IconSize()
			mirror(c, draw.Element(c, o.Icon, size, size).TextColor(t.Colors().Menu().IconForeground()), m.ButtonIconLeft(), size).Top(float32(int((height - size) / 2))).Height(size)
		}
		if o.Value != "" && valueWidth > 0 && toggle == nil {
			valueInk := colors.ActiveTextForeground()
			if o.MutedValue {
				valueInk = colors.MutedText()
			}
			v := text(c, o.Value, t.Typography().DialogBody(), valueInk).SingleLine().Absolute().Top(top).MaxHeight(max(m.ButtonValueMaxHeight(), line)).MaxWidth(valueWidth)
			if c.Direction() == ui.RightToLeft {
				v.Left(tail)
			} else {
				v.Right(tail)
			}
		}
		// paint/settings-button.md#可选右箭头 (SET-ROW-ARROW).
		if o.Arrow && toggle == nil {
			size := t.Metrics().Header().IconSize()
			icon := chevronRight
			ink := t.Colors().Menu().IconForeground()
			if c.Direction() == ui.RightToLeft {
				icon = chevronLeft
			}
			if over {
				ink = t.Colors().Menu().IconForegroundHover()
			}
			e := draw.Element(c, icon, size, size).Absolute().Top((height - size) / 2).TextColor(ink)
			if c.Direction() == ui.RightToLeft {
				e.Left(right)
			} else {
				e.Right(right)
			}
		}
		if toggle != nil {
			toggle(b, height)
		}
	})
	return b.OnClick(o.OnActivate)
}

type ToggleOptions struct {
	Icon, LockIcon *ui.SVG
	Disabled       bool
	// Locked is artwork only. The host decides whether activation changes value.
	Locked   bool
	OnChange func(bool)
}

// ToggleRow reports an intent; its checked value always comes from the host.
func ToggleRow(c *ui.Context, title string, checked bool, o ToggleOptions) ui.Element {
	return button(c, title, ButtonOptions{Icon: o.Icon, Disabled: o.Disabled, OnActivate: func() {
		if o.OnChange != nil {
			o.OnChange(!checked)
		}
	}}, func(row ui.Element, height float32) {
		t := theme.T(c)
		m := t.Metrics().Settings()
		a := ui.Local(row, "settings-toggle", func() timeline { return timeline{} }).update(c, unit(checked), t.Motion().SettingsToggle(), o.Disabled)
		off := t.Colors().Menu().IconForeground()
		if !o.Disabled && (row.Hovered() || row.Pressed()) {
			off = t.Colors().Menu().IconForegroundHover()
		}
		ink := off.Mix(t.Colors().Window().BackgroundActive(), a)
		w, h := 2*m.ToggleBorder()+m.ToggleDiameter()+m.ToggleTravel(), 2*m.ToggleBorder()+m.ToggleDiameter()
		view := ui.Box(c).Absolute().Top(float32(int((height-h)/2))).Size(w, h).Label(title + " switch")
		if c.Direction() == ui.RightToLeft {
			view.Left(m.ToggleRight())
		} else {
			view.Right(m.ToggleRight())
		}
		rtl := c.Direction() == ui.RightToLeft
		// paint/settings-toggle.md#轨道和圆钮. Circle path retains the centered
		// 2 DIP stroke around the nominal 16 DIP path diameter.
		path := ui.Local(view, "knob-path", func() ui.Path { return ui.Path{} })
		path.Reset().Circle(0, 0, m.ToggleDiameter()/2)
		view.Draw(func(p *ui.Painter, r ui.Rect) {
			b, d, s := m.ToggleBorder(), m.ToggleDiameter(), m.ToggleTrackInset()
			track := ui.Rect{X: r.X + b + s, Y: r.Y + b + s, W: d + m.ToggleTravel() - 2*s, H: d - 2*s}
			p.Fill(track, ink, track.H/2)
			x := b + round(m.ToggleTravel()*a)
			if rtl {
				x = w - x - d
			}
			knob := ui.Rect{X: r.X + x, Y: r.Y + b, W: d, H: d}
			draw.Center(p, knob, func() { p.FillPath(path, t.Colors().Window().Background()); p.StrokePath(path, b, ink) })
			if o.Locked && o.LockIcon != nil {
				p.Icon(o.LockIcon, knob, ink)
			}
		})
	}).Role(ui.RoleSwitch).Checked(checked)
}
