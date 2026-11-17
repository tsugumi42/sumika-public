package layout

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// pageImage retains the effect layer of a departed, hidden pane. No readback or
// per-frame image copy is needed; the owning pane cannot repaint while hidden.
type pageImage struct {
	bitmap   *ui.Bitmap
	bounds   ui.Rect
	suppress bool
}

func (*pageImage) EffectMargin() ui.Point { return ui.Point{} }
func (s *pageImage) PaintEffect(p *ui.Painter, box ui.Rect, bitmap *ui.Bitmap, _ ui.Point) {
	s.bitmap, s.bounds = bitmap, box
	if !s.suppress {
		p.ImageRegion(bitmap, ui.Rect{}, box, ui.ImageOptions{})
	}
}

type pageSlide struct {
	from, to Region
	started  time.Time
	backward bool
	active   bool
	images   [regionCount]pageImage
	area     ui.Rect
}

func mainRegion(f frame) Region {
	if f.shown[Third] && (f.ThirdInMain || f.Mode == Single) {
		return Third
	}
	if f.Mode == Single && f.shown[Conversations] {
		return Conversations
	}
	return Chat
}

func (s *pageSlide) update(c *ui.Context, t *theme.Theme, previous, current frame) {
	target := mainRegion(current)
	old := mainRegion(previous)
	for i := range s.images {
		s.images[i].suppress = false
	}
	if current.rects[target] != s.area && s.active {
		s.active = false
	}
	if old != target && previous.rects[old] == current.rects[target] && s.images[old].bitmap != nil {
		s.from, s.to, s.started = old, target, c.Now()
		s.backward, s.active = target < old, true
		s.area = current.rects[target]
	}
	if c.Instant() || c.Preferences().ReduceMotion || c.Now().Sub(s.started) >= t.Motion().ThirdPanel().Duration() {
		s.active = false
	}
	if s.active {
		s.images[s.to].suppress = true
		c.AnimationFrame()
	}
}

func (s *pageSlide) paint(p *ui.Painter, t *theme.Theme) {
	if !s.active || s.images[s.to].bitmap == nil {
		return
	}
	age := p.Now().Sub(s.started)
	u := min(1, max(0, float32(age)/float32(t.Motion().ThirdPanel().Duration())))
	progress := t.Motion().ThirdPanel().Easing().Curve()(u)
	under, over := &s.images[s.from], &s.images[s.to]
	if s.backward {
		under, over = over, under
		progress = 1 - progress
	}
	r := s.images[s.to].bounds
	edge := r.X + r.W*(1-progress)
	p.Clip(r, 0, func() {
		p.Clip(ui.Rect{X: r.X, Y: r.Y, W: edge - r.X, H: r.H}, 0, func() {
			dst := r
			dst.X -= t.Metrics().Layout().PageShift() * progress
			p.ImageRegion(under.bitmap, ui.Rect{}, dst, ui.ImageOptions{})
			p.Fill(ui.Rect{X: r.X, Y: r.Y, W: edge - r.X, H: r.H}, t.Colors().Control().SlideFadeOutBackground().Alpha(progress), 0)
		})
		dst := r
		dst.X = edge
		p.ImageRegion(over.bitmap, ui.Rect{}, dst, ui.ImageOptions{})
		shadow := t.Metrics().Layout().PageShadowWidth()
		p.FillGradient(ui.Rect{X: edge - shadow, Y: r.Y, W: shadow, H: r.H}, ui.LinearGradient{From: t.Colors().Control().SlideFadeOutShadowForeground().Alpha(0), To: t.Colors().Control().SlideFadeOutShadowForeground().Alpha(t.Metrics().Layout().PageShadowOpacity() * progress), Angle: 90}, 0)
	})
	if u < 1 {
		p.AnimationFrame()
	}
}

// WindowHost exposes only the host operations required by third-column sizing.
// ExtendWidth returns the actual signed content-width change. A nil host keeps
// navigation inside the current container. Fixed covers maximized/fullscreen.
type WindowHost struct {
	Fixed                func() bool
	CanExtendWithoutMove func(delta float32) bool
	ExtendWidth          func(delta float32) float32
}

// SetThird belongs in an input callback. It preserves the list's absolute width
// while the host grows, and remembers only this opening's actual extension.
func (s *State) SetThird(c *ui.Context, open bool, host *WindowHost) {
	if s.ThirdOpen == open {
		return
	}
	if s.frame.BodyWidth == 0 {
		s.ThirdOpen = open
		return
	}
	t := s.frame.theme
	m := t.Metrics().Layout()
	body := s.frame.BodyWidth
	if host != nil && body > 0 && !host.Fixed() {
		if open && s.frame.Mode != Triple {
			deficit := float32(0)
			if s.frame.Mode == Single {
				deficit = m.ListMinWidth() + m.ChatMinWidth() + m.ThirdMinWidth() - body
			}
			preferred := clamp(s.ThirdWidth, m.ThirdMinWidth(), m.ThirdMaxWidth()) + deficit
			minimum := m.ThirdMinWidth()
			if deficit > 0 {
				minimum = deficit
			}
			delta := preferred
			if !host.CanExtendWithoutMove(preferred) {
				delta = minimum
				if !host.CanExtendWithoutMove(minimum) && body >= m.ListMinWidth()+m.ChatMinWidth()+m.ThirdMinWidth() {
					delta = 0
				}
			}
			s.extended = host.ExtendWidth(delta)
			if s.extended > 0 {
				ratio := s.ListRatio
				if ratio == 0 {
					ratio = m.ListRatio()
				}
				s.ListRatio = ratio * body / (body + s.extended)
				s.ThirdWidth = max(m.ThirdMinWidth(), delta-deficit)
			}
		} else if !open && s.extended > 0 {
			changed := host.ExtendWidth(-s.extended)
			s.ListRatio = s.ListRatio * body / (body + changed)
		}
	}
	if !open {
		s.extended = 0
	}
	s.ThirdOpen = open
}
