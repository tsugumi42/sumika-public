// Package content renders prepared message content without owning transport,
// decoding, playback, or a message bubble. Give repeated roots stable c.Key IDs.
package content

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// Appearance is the containing message's direction and selection state.
type Appearance struct{ Outgoing, Selected, Disabled bool }

type ink struct{ text, link, code, service, bar, background, muted ui.Color }

func colors(t *theme.Theme, a Appearance) ink {
	m := t.Colors().Message()
	q := t.Colors().Quote()
	if a.Outgoing {
		p, b := m.Out(), q.Out()
		if a.Selected {
			return ink{p.TextSelected(), p.LinkSelected(), p.CodeForegroundSelected(), p.ServiceForegroundSelected(), b.BarSelected(), p.BackgroundSelected(), p.TimestampForegroundSelected()}
		}
		return ink{p.Text(), p.Link(), p.CodeForeground(), p.ServiceForeground(), b.Bar(), p.Background(), p.TimestampForeground()}
	}
	p, b := m.In(), q.In()
	if a.Selected {
		return ink{p.TextSelected(), p.LinkSelected(), p.CodeForegroundSelected(), p.ServiceForegroundSelected(), b.BarSelected(), p.BackgroundSelected(), p.TimestampForegroundSelected()}
	}
	return ink{p.Text(), p.Link(), p.CodeForeground(), p.ServiceForeground(), b.Bar(), p.Background(), p.TimestampForeground()}
}
func text(c *ui.Context, value string, s theme.TextStyle, color ui.Color) ui.Element {
	return ui.Text(c, value).Font(theme.T(c).Typography().Font()).FontSize(s.Size() * c.Preferences().TextScale).FontWeight(s.Weight()).TextColor(color)
}
func action(c *ui.Context, label string, disabled bool, fn func()) ui.Element {
	if fn == nil {
		return ui.Box(c).Label(label).Disabled(disabled)
	}
	return ui.ButtonBase(c).Label(label).Disabled(disabled).OnClick(fn).Cursor(ui.CursorPointer)
}
func transition(s theme.Transition) ui.Animation {
	return ui.Animation{Duration: s.Duration(), Ease: s.Easing().Curve()}
}
func mix(a, b ui.Color, f float32) ui.Color {
	return ui.Color{R: uint8(float32(a.R) + (float32(b.R)-float32(a.R))*f), G: uint8(float32(a.G) + (float32(b.G)-float32(a.G))*f), B: uint8(float32(a.B) + (float32(b.B)-float32(a.B))*f), A: uint8(float32(a.A) + (float32(b.A)-float32(a.A))*f)}
}

func quoteBar(e ui.Element, width, radius float32, color ui.Color) {
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Clip(r, radius, func() { p.Fill(ui.Rect{X: r.X, Y: r.Y, W: width, H: r.H}, color, 0) })
	})
}

type rippleKey struct{}
type rippleState struct {
	start, release time.Time
	x, y           float32
	active, down   bool
}

// Paint-only clocks stop requesting frames when the ripple finishes.
func ripple(c *ui.Context, e ui.Element, color ui.Color, clipRadius, alpha float32) {
	s := ui.Local(e, rippleKey{}, func() rippleState { return rippleState{} })
	t := theme.T(c)
	e.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
			s.start = ev.Time
			s.release = time.Time{}
			s.x, s.y = ev.X, ev.Y
			s.active, s.down = true, true
		}
		if ev.Kind == ui.InputPointerUp && ev.Button == 0 && s.down {
			s.release = ev.Time
			s.down = false
		}
		return false
	})
	show, hide := t.Motion().RippleShow().Duration(), t.Motion().RippleHide().Duration()
	showCurve, hideCurve := t.Motion().RippleShow().Easing().Curve(), t.Motion().RippleHide().Easing().Curve()
	if c.Preferences().ReduceMotion {
		show, hide = 0, 0
	}
	e.DrawOver(func(p *ui.Painter, r ui.Rect) {
		if !s.active {
			return
		}
		age := p.Now().Sub(s.start)
		progress := float32(1)
		if show > 0 {
			progress = min(1, float32(age)/float32(show))
		}
		progress = showCurve(progress)
		opacity := float32(1)
		if !s.down {
			if hide == 0 {
				opacity = 0
			} else {
				opacity = 1 - hideCurve(min(1, float32(p.Now().Sub(s.release))/float32(hide)))
			}
		}
		if opacity <= 0 {
			return
		}
		radius := float32(math.Hypot(float64(max(s.x, r.W-s.x)), float64(max(s.y, r.H-s.y)))) * progress
		p.Clip(r, clipRadius, func() {
			p.Fill(ui.Rect{X: r.X + s.x - radius, Y: r.Y + s.y - radius, W: radius * 2, H: radius * 2}, color.Alpha(alpha*opacity), radius)
		})
		if progress < 1 || !s.down {
			p.AnimationFrame()
		}
	})
}

// Corners holds the host bubble's four outer radii. Nil uses the theme's large radius.
type Corners struct{ TopLeft, TopRight, BottomRight, BottomLeft float32 }

func radii(t *theme.Theme, v *Corners) [4]float32 {
	if v != nil {
		return [4]float32{v.TopLeft, v.TopRight, v.BottomRight, v.BottomLeft}
	}
	r := t.Metrics().Message().RadiusLarge()
	return [4]float32{r, r, r, r}
}
