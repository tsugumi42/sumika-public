// Package settings implements the settings paint specifications. Hosts own values,
// prepared icons, selection and validation; local state contains only UI feedback.
package settings

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func text(c *ui.Context, value string, style theme.TextStyle, ink ui.Color) ui.Element {
	return ui.Text(c, value).Font(theme.T(c).Typography().Font()).FontSize(style.Size() * c.Preferences().TextScale).FontWeight(style.Weight()).TextColor(ink)
}
func measure(c *ui.Context, value string, style theme.TextStyle, width float32) (float32, float32) {
	return c.MeasureText(width, ui.Span{Text: value, Font: theme.T(c).Typography().Font(), Size: style.Size() * c.Preferences().TextScale, Weight: style.Weight()})
}
func unit(v bool) float32 {
	if v {
		return 1
	}
	return 0
}
func round(v float32) float32 { return float32(math.Round(float64(v))) }
func mirror(c *ui.Context, e ui.Element, x, width float32) ui.Element {
	if c.Direction() == ui.RightToLeft {
		return e.Absolute().Right(x).Width(width)
	}
	return e.Absolute().Left(x).Width(width)
}

// timeline retains the sampled value when reversing; every new target gets the
// full source duration. It never starts an entrance animation for bound values.
// paint/settings-toggle.md#状态与动效; paint/radio-check.md#交互状态和动效.
type timeline struct {
	ready               bool
	target, from, value float32
	at                  time.Time
}

func (a *timeline) update(c *ui.Context, target float32, tr theme.Transition, instant bool) float32 {
	if !a.ready || instant || c.Preferences().ReduceMotion || tr.Duration() == 0 {
		a.ready = true
		a.target = target
		a.value = target
		return target
	}
	if target != a.target {
		a.from = a.value
		a.target = target
		a.at = c.Now()
	}
	if a.value == a.target {
		return a.value
	}
	p := min(float32(c.Now().Sub(a.at))/float32(tr.Duration()), 1)
	a.value = a.from + (a.target-a.from)*tr.Easing().Curve()(p)
	if p < 1 {
		c.AnimationFrame()
	}
	return a.value
}

// paint/settings-button.md#悬停按下禁用与动效. Pointer presses alone start
// feedback; keyboard activation must not invent a pointer origin.
type pulse struct {
	down                bool
	at, released        time.Time
	x, y, growth, alpha float32
}

func (s *pulse) update(c *ui.Context, e ui.Element, enabled bool, hit func(float32, float32) bool) {
	down := enabled && e.Pressed()
	if down && !s.down {
		x, y, ok := e.PointerPosition()
		if ok && (hit == nil || hit(x, y)) {
			s.at = c.Now()
			s.released = time.Time{}
			s.x = x
			s.y = y
		}
	}
	if !down && s.down {
		s.released = c.Now()
	}
	s.down = down
	if !enabled || s.at.IsZero() {
		s.alpha = 0
		return
	}
	tr := theme.T(c).Motion()
	progress := func(at time.Time, t theme.Transition) float32 {
		if c.Preferences().ReduceMotion || t.Duration() == 0 {
			return 1
		}
		p := min(float32(c.Now().Sub(at))/float32(t.Duration()), 1)
		if p < 1 {
			c.AnimationFrame()
		}
		return t.Easing().Curve()(p)
	}
	s.growth = progress(s.at, tr.RippleShow())
	s.alpha = 1
	if !s.released.IsZero() {
		s.alpha = 1 - progress(s.released, tr.RippleHide())
	}
	if s.alpha == 0 {
		s.at = time.Time{}
	}
}
func (s *pulse) paint(p *ui.Painter, r ui.Rect, ink ui.Color, circle bool) {
	if s.alpha == 0 {
		return
	}
	radius := round(float32(math.Hypot(float64(max(s.x, r.W-s.x)), float64(max(s.y, r.H-s.y)))) * s.growth)
	clip := float32(0)
	if circle {
		clip = r.W / 2
	}
	p.Clip(r, clip, func() {
		p.Fill(ui.Rect{X: r.X + s.x - radius, Y: r.Y + s.y - radius, W: radius * 2, H: radius * 2}, ink.Alpha(s.alpha), radius)
	})
}

// Lucide 1.58.0 geometry (ISC/MIT), see THIRD_PARTY_NOTICES.md.
func glyph(shape string, x, y, w, h float32) *ui.SVG {
	return draw.Lucide(shape, ui.Rect{X: x, Y: y, W: w, H: h})
}

var chevronRight = glyph(`<path d="m9 18 6-6-6-6"/>`, 9, 6, 6, 12)
var chevronLeft = glyph(`<path d="m15 18-6-6 6-6"/>`, 9, 6, 6, 12)
var checkIcon = glyph(`<path d="M20 6 9 17l-5-5"/>`, 4, 6, 16, 11)
var searchIcon = glyph(`<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>`, 3, 3, 18, 18)

func init() {
	draw.Configure(chevronRight, draw.Submenu, 24, 24)
	draw.Configure(chevronLeft, draw.Submenu, 24, 24)
	draw.Configure(checkIcon, draw.Check, 14, 10)
	draw.Configure(searchIcon, draw.Search, 16, 16)
}
