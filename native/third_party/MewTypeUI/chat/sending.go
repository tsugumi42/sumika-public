package chat

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// DraftCapture owns the last painted editor layer. Take rotates its identity
// after an accepted submission, so clearing the draft cannot repaint the flight.
// Host-owned text and a bound field handle survive the new editor instance.
type DraftCapture struct {
	generation uint64
	layer      sendingImage
	refocus    *ui.Handle
}

type SendingSource struct {
	image    *ui.Bitmap
	pixels   ui.Rect
	bounds   ui.Rect
	captured time.Time
}

type sendingImage struct {
	image  *ui.Bitmap
	bounds ui.Rect
	scale  float32
	hide   bool
	flight *SendingState
	handle ui.Handle
}

// Parent caches shift painter coordinates; committed handle bounds stay in
// window coordinates. Like Draw, the effect callback belongs to this frame's
// node. Persistent state contains no Context or other build value.
type sendingImageView func(*ui.Painter, ui.Rect, *ui.Bitmap, ui.Point)

func (sendingImageView) EffectMargin() ui.Point { return ui.Point{} }
func (v sendingImageView) PaintEffect(p *ui.Painter, r ui.Rect, image *ui.Bitmap, origin ui.Point) {
	v(p, r, image, origin)
}

func captureSendingImage(c *ui.Context, s *sendingImage) sendingImageView {
	return func(p *ui.Painter, r ui.Rect, image *ui.Bitmap, _ ui.Point) {
		// An already-built effect may paint after Finish; it must not retain
		// the destination bitmap again after the flight released it.
		if s.flight == nil || s.flight.active {
			s.image, s.bounds, s.scale = image, s.handle.Bounds(c), p.Scale()
			if s.flight != nil && s.flight.started.IsZero() {
				s.flight.started = p.Now()
			}
		}
		if !s.hide {
			p.ImageRegion(image, ui.Rect{}, r, ui.ImageOptions{})
		}
	}
}

// CaptureDraft wraps an editor or a field; pass its bound field to Take to crop
// away buttons, reference bars and other editor chrome.
func CaptureDraft(c *ui.Context, s *DraftCapture, content func()) ui.Element {
	root := ui.Column(c).FillWidth().MinWidth(0)
	root.Children(func() {
		ui.Column(c.Key(s.generation)).FillWidth().MinWidth(0).Bind(&s.layer.handle).Cache().Effect(captureSendingImage(c, &s.layer)).Children(content)
	})
	if s.refocus != nil {
		s.refocus.Focus(c)
		s.refocus = nil
	}
	return root
}

// Take runs before the host clears the draft, from the accepted-send callback.
func (s *DraftCapture) Take(c *ui.Context, field *ui.Handle) SendingSource {
	r := field.Bounds(c)
	source := SendingSource{image: s.layer.image, bounds: r, pixels: ui.Rect{X: (r.X - s.layer.bounds.X) * s.layer.scale, Y: (r.Y - s.layer.bounds.Y) * s.layer.scale, W: r.W * s.layer.scale, H: r.H * s.layer.scale}}
	if field.Focused(c) {
		s.refocus = field
	}
	s.generation++
	s.layer = sendingImage{}
	return source
}

// SendingState follows one outgoing message ID, independently of mounting.
type SendingState struct {
	source  SendingSource
	target  sendingImage
	started time.Time
	active  bool
}

func (s *SendingState) Start(source SendingSource) {
	s.source = source
	s.target = sendingImage{}
	s.started = time.Time{}
	s.active = source.image != nil
}
func (s *SendingState) Finish() {
	s.active = false
	s.source = SendingSource{}
	s.target.image = nil
	s.target.handle = ui.Handle{}
	s.target.hide = false
}

// SendingTarget hides the destination while its captured bubble flies. The
// message's regular layout remains present and continues to track scrolling.
func SendingTarget(c *ui.Context, s *SendingState, content func()) ui.Element {
	root := ui.Column(c).FillWidth().MinWidth(0)
	if s.active {
		s.target.hide = !c.Instant() && !c.Preferences().ReduceMotion
		s.target.flight = s
		root.Bind(&s.target.handle).Effect(captureSendingImage(c, &s.target)).Cache()
	}
	root.Children(content)
	return root
}

// SendingFlight paints above the history and editor; call once after both have
// been built. Source and destination geometry are window-relative DIPs.
func SendingFlight(c *ui.Context, s *SendingState) ui.Element {
	root := ui.Box(c).Size(0, 0)
	t := theme.T(c)
	if !s.active {
		return root
	}
	if s.source.captured.IsZero() {
		s.source.captured = c.Now()
	}
	if c.Instant() || c.Preferences().ReduceMotion || c.Now().Sub(s.source.captured) >= t.Motion().SendingTimeout().Duration() {
		s.Finish()
		c.Invalidate()
		return root
	}
	if s.target.image != nil && s.started.IsZero() {
		s.started = c.Now()
	}
	if !s.started.IsZero() && c.Now().Sub(s.started) >= t.Motion().SendingFlight().Duration() {
		s.Finish()
		c.Invalidate()
		return root
	}
	c.AnimationFrame()
	services := c.Services()
	ui.Overlay(c, func() {
		ui.Box(c).Fill().PassThrough().Draw(func(p *ui.Painter, _ ui.Rect) {
			if s.target.image == nil || s.started.IsZero() {
				p.ImageRegion(s.source.image, s.source.pixels, s.source.bounds, ui.ImageOptions{})
				return
			}
			elapsed := p.Now().Sub(s.started)
			progress := min(1, max(0, float32(elapsed)/float32(t.Motion().SendingFlight().Duration())))
			slide := min(1, max(0, float32(elapsed)/float32(t.Motion().SendingSlide().Duration())))
			mix := min(1, max(0, float32(elapsed)/float32(t.Motion().SendingMix().Duration())))
			x := theme.EaseOutQuint.Curve()(slide)
			y := sendingRise(progress)
			from, to := s.source.bounds, s.target.bounds
			r := ui.Rect{X: from.X + (to.X-from.X)*x, Y: from.Y + (to.Y-from.Y)*y, W: from.W + (to.W-from.W)*mix, H: from.H + (to.H-from.H)*mix}
			p.ImageRegion(s.source.image, s.source.pixels, r, ui.ImageOptions{Tint: ui.RGBA(255, 255, 255, 1-mix)})
			p.ImageRegion(s.target.image, ui.Rect{}, r, ui.ImageOptions{Tint: ui.RGBA(255, 255, 255, mix)})
			if progress < 1 {
				p.AnimationFrame()
			} else {
				s.Finish()
				services.Invalidate()
			}
		})
	})
	return root
}

// Cubic Bezier (0.25,0.1,0.25,1), message_sending_text_flight.cpp:159-160.
func sendingRise(x float32) float32 {
	lo, hi := float32(0), float32(1)
	for range 16 {
		u := (lo + hi) / 2
		v := 1 - u
		bx := 3*v*v*u*.25 + 3*v*u*u*.25 + u*u*u
		if bx < x {
			lo = u
		} else {
			hi = u
		}
	}
	u := (lo + hi) / 2
	v := 1 - u
	return 3*v*v*u*.1 + 3*v*u*u + u*u*u
}
