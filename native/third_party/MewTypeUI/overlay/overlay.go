// Package overlay provides themed menus, tooltips, dialogs, toasts and image viewers.
// Hosts retain explicit state and keep window-level overlays outside virtual rows.
package overlay

import (
	"math"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func text(c *ui.Context, value string, style theme.TextStyle, color ui.Color) ui.Element {
	return ui.Text(c, value).Font(theme.T(c).Typography().Font()).FontSize(style.Size() * c.Preferences().TextScale).FontWeight(style.Weight()).TextColor(color)
}
func measure(c *ui.Context, value string, style theme.TextStyle, width float32) (float32, float32) {
	return c.MeasureText(width, ui.Span{Text: value, Font: theme.T(c).Typography().Font(), Size: style.Size() * c.Preferences().TextScale, Weight: style.Weight()})
}
func unit(value bool) float32 {
	if value {
		return 1
	}
	return 0
}
func progress(c *ui.Context, start time.Time, transition theme.Transition) float32 {
	if c.Preferences().ReduceMotion || transition.Duration() == 0 {
		return 1
	}
	p := min(float32(c.Now().Sub(start))/float32(transition.Duration()), 1)
	if p < 1 {
		c.AnimationFrame()
	}
	return transition.Easing().Curve()(p)
}

// visibility keeps an exiting surface alive and permits reversal without an opacity jump.
type visibility struct {
	initialized, target bool
	start               time.Time
	from, value         float32
}

func (v *visibility) update(c *ui.Context, open bool, enter, exit theme.Transition) float32 {
	if !v.initialized {
		v.initialized = true
		v.target = open
		v.start = c.Now()
	}
	if v.target != open {
		v.from = v.value
		v.target = open
		v.start = c.Now()
	}
	if v.value == unit(open) {
		return v.value
	}
	tr := exit
	if open {
		tr = enter
	}
	p := progress(c, v.start, tr)
	v.value = v.from + (unit(open)-v.from)*p
	return v.value
}

func menuShadow(e ui.Element, t *theme.Theme) {
	m := t.Metrics().Menu()
	e.Shadow(0, m.ShadowY(), m.ShadowBlur(), 0, t.Colors().Window().ShadowForeground().Alpha(m.ShadowOpacity()))
}

func material(e ui.Element, surface theme.Surface) { materialOpacity(e, surface, 1) }

func materialOpacity(e ui.Element, surface theme.Surface, opacity float32) {
	e.Background(surface.Background().Alpha(opacity))
	if surface.Glass() != theme.GlassNone {
		style := glass.Regular
		if surface.Glass() == theme.GlassClear {
			style = glass.Clear
		}
		e.Material(glass.Glass{Style: style, Tint: surface.Background().Alpha(opacity), Clarity: surface.Clarity()})
	} else if surface.BackdropBlur() > 0 {
		e.Material(glass.Blur{Radius: surface.BackdropBlur()})
	}
}

// ripple is local press feedback, not host-owned application state.
func ripple(c *ui.Context, e ui.Element, ink ui.Color) {
	type pulse struct {
		down         bool
		at, released time.Time
		x, y         float32
	}
	s := ui.Local(e, "overlay-ripple", func() pulse { return pulse{} })
	down := e.Pressed()
	if down && !s.down {
		s.at = c.Now()
		s.released = time.Time{}
		s.x, s.y, _ = e.PointerPosition()
	}
	if !down && s.down {
		s.released = c.Now()
	}
	s.down = down
	if s.at.IsZero() {
		return
	}
	t := theme.T(c)
	growth := progress(c, s.at, t.Motion().RippleShow())
	alpha := float32(1)
	if !s.released.IsZero() {
		alpha = 1 - progress(c, s.released, t.Motion().RippleHide())
	}
	if alpha == 0 {
		s.at = time.Time{}
		return
	}
	x, y := s.x, s.y
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		radius := float32(math.Hypot(float64(max(x, r.W-x)), float64(max(y, r.H-y)))) * growth
		p.Clip(r, 0, func() {
			p.Fill(ui.Rect{X: r.X + x - radius, Y: r.Y + y - radius, W: radius * 2, H: radius * 2}, ink.Alpha(alpha), radius)
		})
	})
}
