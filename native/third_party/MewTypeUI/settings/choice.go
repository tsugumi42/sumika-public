package settings

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type ChoiceOptions struct {
	Disabled, Error, Multiline bool
	OnChange                   func(bool)
}

// Radio reports only selection, never an unchecked intent for an already selected row.
func Radio(c *ui.Context, label string, checked bool, o ChoiceOptions) ui.Element {
	return choice(c, label, checked, true, o)
}
func Check(c *ui.Context, label string, checked bool, o ChoiceOptions) ui.Element {
	return choice(c, label, checked, false, o)
}

func choice(c *ui.Context, label string, checked, radio bool, o ChoiceOptions) ui.Element {
	t := theme.T(c)
	m, s := t.Metrics().Choice(), t.Metrics().Settings()
	d := m.CheckDiameter()
	if radio {
		d = m.RadioDiameter()
	}
	root := ui.ButtonBase(c).FillWidth().Shrink(0).Label(label).Checked(checked).Disabled(o.Disabled)
	if radio {
		root.Role(ui.RoleRadio)
	} else {
		root.Role(ui.RoleCheckBox)
	}
	a := ui.Local(root, "choice-progress", func() timeline { return timeline{} }).update(c, unit(checked), t.Motion().Choice(), o.Disabled)
	errorProgress := ui.Local(root, "choice-error", func() timeline { return timeline{} }).update(c, unit(o.Error && !checked && !radio), t.Motion().Choice(), o.Disabled || checked)
	width := root.LayoutBounds().W
	textWidth := max(0, width-s.CheckPaddingLeft()-d-s.CheckTextGap()-s.CheckPaddingRight())
	services := c.Services()
	root.DrawOver(func(_ *ui.Painter, r ui.Rect) {
		if r.W != width {
			services.Invalidate()
		}
	})
	_, line := measure(c, label, t.Typography().DialogBody(), 0)
	if o.Multiline && textWidth > 0 {
		_, line = measure(c, label, t.Typography().DialogBody(), textWidth)
	}
	height := s.CheckPaddingTop() + max(d, line+s.CheckTextTop()) + s.CheckPaddingBottom()
	root.Height(height)
	// paint/radio-check.md#在行中的位置: margins belong to the native widget,
	// not this outer settings layout. Applying them again would make 42 become 58.
	root.Children(func() {
		e := text(c, label, t.Typography().DialogBody(), t.Colors().Window().Text())
		if !o.Multiline {
			e.SingleLine()
		}
		mirror(c, e, s.CheckPaddingLeft()+d+s.CheckTextGap(), textWidth).Top(s.CheckPaddingTop() + s.CheckTextTop())
		mirror(c, mark(c, checked, radio, a, errorProgress, false), s.CheckPaddingLeft(), d).Top(s.CheckPaddingTop()).Label(label + " mark")
	})
	var rippleRect ui.Rect
	rippleRect = ui.Rect{X: s.CheckPaddingLeft() - m.RipplePadding(), Y: s.CheckPaddingTop() - m.RipplePadding(), W: d + 2*m.RipplePadding(), H: d + 2*m.RipplePadding()}
	if c.Direction() == ui.RightToLeft {
		rippleRect.X = width - rippleRect.X - rippleRect.W
	}
	wave := ui.Local(root, "choice-ripple", func() pulse { return pulse{} })
	wave.update(c, root, !o.Disabled, func(x, y float32) bool {
		dx, dy := x-rippleRect.X-rippleRect.W/2, y-rippleRect.Y-rippleRect.H/2
		return dx*dx+dy*dy <= rippleRect.W*rippleRect.W/4
	})
	feedback := *wave
	feedback.x -= rippleRect.X
	feedback.y -= rippleRect.Y
	root.Draw(func(p *ui.Painter, r ui.Rect) {
		box := rippleRect
		box.X += r.X
		box.Y += r.Y
		feedback.paint(p, box, t.Colors().Window().BackgroundHover().Mix(t.Colors().Control().SurfaceBackgroundHover(), a), true)
	})
	return root.OnClick(func() {
		if o.OnChange != nil && (!radio || !checked) {
			o.OnChange(!checked)
		}
	})
}

// RadioMark and CheckMark are noninteractive views for host-owned custom rows.
func RadioMark(c *ui.Context, checked bool) ui.Element {
	e := ui.Box(c)
	a := ui.Local(e, "mark-progress", func() timeline { return timeline{} }).update(c, unit(checked), theme.T(c).Motion().Choice(), false)
	return markIn(c, e, true, a, 0, false)
}
func CheckMark(c *ui.Context, checked bool) ui.Element {
	e := ui.Box(c)
	a := ui.Local(e, "mark-progress", func() timeline { return timeline{} }).update(c, unit(checked), theme.T(c).Motion().Choice(), false)
	return markIn(c, e, false, a, 0, false)
}
func mark(c *ui.Context, checked, radio bool, a, errorProgress float32, box bool) ui.Element {
	return markIn(c, ui.Box(c), radio, a, errorProgress, box)
}
func markIn(c *ui.Context, e ui.Element, radio bool, a, errorProgress float32, box bool) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Choice()
	d, stroke := m.CheckDiameter(), m.CheckStroke()
	if radio {
		d, stroke = m.RadioDiameter(), m.RadioStroke()
	}
	e.Size(d, d).Shrink(0)
	fg := t.Colors().Control().CheckboxForeground().Mix(t.Colors().Dialog().TextForegroundError(), errorProgress).Mix(t.Colors().Window().BackgroundActive(), a)
	path := ui.Local(e, "radio-path", func() ui.Path { return ui.Path{} })
	inset := m.RadioOuterInset() + stroke/2
	if radio {
		path.Reset().Circle(0, 0, (d-2*inset)/2)
	}
	check := draw.Resolve(c, checkIcon, m.CheckMarkWidth(), m.CheckMarkHeight())
	// paint/radio-check.md#radio圆环与内点; #check方框与勾.
	return e.Draw(func(p *ui.Painter, r ui.Rect) {
		if radio {
			draw.Center(p, r, func() {
				if box {
					p.FillPath(path, t.Colors().Dialog().Background())
				}
				p.StrokePath(path, stroke, fg)
			})
			dot := (d - 2*m.RadioDotInset()) * a
			if dot > 0 {
				draw.Circle(p, ui.Rect{X: r.X + (d-dot)/2, Y: r.Y + (d-dot)/2, W: dot, H: dot}, fg)
			}
		} else {
			radius := t.Metrics().Control().RadiusSmall()
			p.Fill(r, fg.Alpha(a), radius)
			p.Stroke(r, fg, radius, stroke)
			if a > 0 {
				check.Paint(p, ui.Rect{X: r.X + m.CheckMarkLeft(), Y: r.Y + m.CheckMarkTop(), W: m.CheckMarkWidth(), H: m.CheckMarkHeight()}, t.Colors().Control().OverviewCheckForegroundActive())
			}
		}
	})
}
