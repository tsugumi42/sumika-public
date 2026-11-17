package chat

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type rippleState struct {
	pressed        bool
	start, release time.Time
	x, y           float32
}

type ripplePaint struct {
	start, release time.Time
	x, y           float32
	show, hide     theme.Transition
	color          ui.Color
}

func buttonRipple(c *ui.Context, b ui.Element, color ui.Color) ripplePaint {
	state := ui.Local(b, "chat.ripple", func() rippleState { return rippleState{} })
	down := b.Pressed()
	if down && !state.pressed {
		state.start, state.release = c.Now(), time.Time{}
		state.x, state.y, _ = b.PointerPosition()
	}
	if !down && state.pressed {
		state.release = c.Now()
	}
	state.pressed = down
	motion := theme.T(c).Motion()
	return ripplePaint{state.start, state.release, state.x, state.y, motion.RippleShow(), motion.RippleHide(), color}
}

func (v ripplePaint) draw(p *ui.Painter, bounds ui.Rect) {
	if v.start.IsZero() {
		return
	}
	opacity := float32(1)
	if !v.release.IsZero() {
		if p.Now().Sub(v.release) >= v.hide.Duration() {
			return
		}
		opacity = 1 - v.hide.Easing().Curve()(float32(p.Now().Sub(v.release))/float32(v.hide.Duration()))
	}
	progress := float32(1)
	if p.Now().Sub(v.start) < v.show.Duration() {
		progress = v.show.Easing().Curve()(float32(p.Now().Sub(v.start)) / float32(v.show.Duration()))
	}
	if progress < 1 || !v.release.IsZero() {
		p.AnimationFrame()
	}
	radius := bounds.W * progress
	p.Clip(bounds, bounds.W/2, func() {
		p.Fill(ui.Rect{X: bounds.X + v.x - radius, Y: bounds.Y + v.y - radius, W: 2 * radius, H: 2 * radius}, v.color.Alpha(opacity), radius)
	})
}
