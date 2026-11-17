package dialogs

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func text(c *ui.Context, value string, style theme.TextStyle, color ui.Color) ui.Element {
	return ui.Text(c, value).Font(theme.T(c).Typography().Font()).FontSize(style.Size() * c.Preferences().TextScale).FontWeight(style.Weight()).TextColor(color)
}
func icon(c *ui.Context, src *ui.SVG, size float32, color ui.Color) ui.Element {
	return draw.Resolve(c, src, size, size).Element(c).TextColor(color).Shrink(0)
}
func measure(c *ui.Context, value string, style theme.TextStyle) (float32, float32) {
	return c.MeasureText(0, ui.Span{Text: value, Font: theme.T(c).Typography().Font(), Size: style.Size() * c.Preferences().TextScale, Weight: style.Weight()})
}
func animate(e ui.Element, key string, target float32, transition theme.Transition) float32 {
	if transition.Duration() == 0 {
		return e.Jump(key, target)
	}
	return e.AnimateTo(key, target, ui.Animation{Duration: transition.Duration(), Ease: transition.Easing().Curve()})
}
func unit(on bool) float32 {
	if on {
		return 1
	}
	return 0
}

type rippleState struct {
	held            bool
	start, released time.Time
	x, y            float32
}
type rippleKey struct{}

// The effect keeps only transient pointer state. Its drawing requests frames only
// while visible and moving; release fades the current circle without restarting it.
func ripple(c *ui.Context, e ui.Element, color ui.Color) {
	s := ui.Local(e, rippleKey{}, func() rippleState { return rippleState{} })
	held := e.Pressed()
	if held && !s.held {
		s.start = c.Now()
		s.released = time.Time{}
		s.x, s.y, _ = e.PointerPosition()
	}
	if !held && s.held {
		s.released = c.Now()
	}
	s.held = held
	if s.start.IsZero() {
		return
	}
	start, released, x, y := s.start, s.released, s.x, s.y
	motion := theme.T(c).Motion()
	show, hide := motion.RippleShow(), motion.RippleHide()
	reduced := c.Preferences().ReduceMotion
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		progress := float32(1)
		if show.Duration() > 0 && !reduced {
			progress = min(1, float32(p.Now().Sub(start))/float32(show.Duration()))
		}
		alpha := float32(1)
		if !released.IsZero() {
			if hide.Duration() == 0 || reduced {
				return
			}
			alpha = 1 - min(1, float32(p.Now().Sub(released))/float32(hide.Duration()))
			if alpha <= 0 {
				return
			}
		}
		radius := float32(math.Hypot(float64(max(x, r.W-x)), float64(max(y, r.H-y)))) * show.Easing().Curve()(progress)
		p.Clip(r, 0, func() {
			p.Fill(ui.Rect{X: r.X + x - radius, Y: r.Y + y - radius, W: radius * 2, H: radius * 2}, color.Alpha(alpha), radius)
		})
		if progress < 1 || !released.IsZero() {
			p.AnimationFrame()
		}
	})
}
