// Package control implements the clean-room general control specifications.
// Hosts own values, stable IDs and actions; no control performs external I/O.
package control

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
	"math"
	"time"
)

func text(c *ui.Context, value string, style theme.TextStyle, ink ui.Color) ui.Element {
	return ui.Text(c, value).Font(theme.T(c).Typography().Font()).FontSize(style.Size() * c.Preferences().TextScale).FontWeight(style.Weight()).TextColor(ink)
}
func measure(c *ui.Context, value string, style theme.TextStyle, width float32) (float32, float32) {
	return c.MeasureText(width, ui.Span{Text: value, Font: theme.T(c).Typography().Font(), Size: style.Size() * c.Preferences().TextScale, Weight: style.Weight()})
}
func round(v float32) float32 { return float32(math.Round(float64(v))) }
func floor(v float32) float32 { return float32(math.Floor(float64(v))) }
func unit(v bool) float32 {
	if v {
		return 1
	}
	return 0
}
func logical(c *ui.Context, e ui.Element, x, w float32) ui.Element {
	e.Absolute().Width(max(0, w))
	if c.Direction() == ui.RightToLeft {
		return e.Right(x)
	}
	return e.Left(x)
}
func mirrorRect(r ui.Rect, w float32, rtl bool) ui.Rect {
	if rtl {
		r.X = w - r.X - r.W
	}
	return r
}

type timeline struct {
	ready               bool
	from, target, value float32
	at                  time.Time
}

func (a *timeline) sample(now time.Time, tr theme.Transition) float32 {
	if a.value == a.target {
		return a.value
	}
	q := min(float32(now.Sub(a.at))/float32(tr.Duration()), 1)
	a.value = a.from + (a.target-a.from)*tr.Easing().Curve()(q)
	return a.value
}

// paint/collapse.md#几何与动画: reverse from the currently sampled value.
func (a *timeline) update(c *ui.Context, target float32, tr theme.Transition, instant bool) float32 {
	if !a.ready || instant || c.Preferences().ReduceMotion || tr.Duration() == 0 {
		a.ready = true
		a.target = target
		a.value = target
		return target
	}
	a.sample(c.Now(), tr)
	if a.target != target {
		a.from = a.value
		a.target = target
		a.at = c.Now()
	}
	if a.value != a.target {
		c.AnimationFrame()
	}
	return a.value
}

// paint/round-button.md#状态与交互; paint/icon-button.md#状态与动画.
// Pointer-only feedback, with clipping owned by the component's exact outline.
type pulse struct {
	down                bool
	at, released        time.Time
	x, y, growth, alpha float32
}

func (s *pulse) update(c *ui.Context, e ui.Element, enabled bool, hit func(float32, float32) bool) {
	down := enabled && e.Pressed()
	if down && !s.down {
		if x, y, ok := e.PointerPosition(); ok && (hit == nil || hit(x, y)) {
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
	progress := func(at time.Time, tr theme.Transition) float32 {
		if c.Preferences().ReduceMotion || tr.Duration() == 0 {
			return 1
		}
		q := min(float32(c.Now().Sub(at))/float32(tr.Duration()), 1)
		if q < 1 {
			c.AnimationFrame()
		}
		return tr.Easing().Curve()(q)
	}
	s.growth = progress(s.at, theme.T(c).Motion().RippleShow())
	s.alpha = 1
	if !s.released.IsZero() {
		s.alpha = 1 - progress(s.released, theme.T(c).Motion().RippleHide())
	}
	if s.alpha == 0 {
		s.at = time.Time{}
	}
}
func (s *pulse) paint(p *ui.Painter, r ui.Rect, ink ui.Color) {
	if s.alpha == 0 {
		return
	}
	radius := round(float32(math.Hypot(float64(max(s.x, r.W-s.x)), float64(max(s.y, r.H-s.y)))) * s.growth)
	p.Fill(ui.Rect{X: r.X + s.x - radius, Y: r.Y + s.y - radius, W: 2 * radius, H: 2 * radius}, ink.Alpha(s.alpha), radius)
}

// Lucide 1.58.0, ISC/MIT, see THIRD_PARTY_NOTICES.md.
func glyph(shape string, x, y, w, h float32) *ui.SVG {
	return draw.Lucide(shape, ui.Rect{X: x, Y: y, W: w, H: h})
}

var backIcon = glyph(`<path d="m12 19-7-7 7-7"/><path d="M5 12h14"/>`, 5, 5, 14, 14)
var closeIcon = glyph(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`, 6, 6, 12, 12)
var downIcon = glyph(`<path d="m6 9 6 6 6-6"/>`, 6, 9, 12, 6)

func init() {
	draw.Configure(backIcon, draw.ControlBack, 24, 24)
	draw.Configure(closeIcon, draw.Close, 24, 24)
	draw.Configure(downIcon, draw.Dropdown, 24, 24)
}
