package settings

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"math"
)

type barLocal struct {
	hover         timeline
	dragging      bool
	offset, start float32
}

// paint/choose-box.md#滚动区. Native Scroll keeps wheel/inertia; this indicator
// replaces its incompatible inset/hide geometry and supplies thumb dragging.
func chooseScrollbar(c *ui.Context, s *ChooseState, view float32) {
	t := theme.T(c)
	m := t.Metrics().Choose()
	sm := t.Metrics().Scrollbar()
	content := s.cache.total
	q := view - m.ScrollInsetTop() - m.ScrollInsetBottom()
	if content <= view || q < sm.MinHeight() {
		return
	}
	thumb := max(sm.MinHeight(), float32(math.Floor(float64(q*view/content))))
	travel := q - thumb
	top := m.ScrollInsetTop() + float32(math.Floor(float64(travel*s.scroll.Y/(content-view))))
	bar := ui.Box(c).Absolute().Top(0).Bottom(0).Width(m.ScrollTrackWidth()).Label("Choice scrollbar")
	if c.Direction() == ui.RightToLeft {
		bar.Left(0)
	} else {
		bar.Right(0)
	}
	local := ui.Local(bar, "solid-scrollbar", func() barLocal { return barLocal{} })
	hover := local.hover.update(c, unit(bar.Hovered() || local.dragging), t.Motion().Scrollbar(), false)
	bar.HandleInput(func(ev ui.InputEvent) bool {
		switch ev.Kind {
		case ui.InputPointerDown:
			if ev.Button != 0 {
				return false
			}
			local.dragging = true
			local.start = ev.Y
			local.offset = s.scroll.Y
			if ev.Y < top || ev.Y > top+thumb {
				s.scroll.Y = max(0, min(content-view, (ev.Y-m.ScrollInsetTop()-thumb/2)/travel*(content-view)))
				local.offset = s.scroll.Y
			}
			return true
		case ui.InputPointerMove:
			if !local.dragging {
				return false
			}
			s.scroll.Y = max(0, min(content-view, local.offset+(ev.Y-local.start)/travel*(content-view)))
			return true
		case ui.InputPointerUp:
			local.dragging = false
			return true
		}
		return false
	})
	bar.Draw(func(p *ui.Painter, r ui.Rect) {
		colors := t.Colors().Scrollbar()
		p.Fill(r, colors.TrackBackground().Mix(colors.TrackBackgroundHover(), hover), 0)
		p.Fill(ui.Rect{X: r.X + m.ScrollInsetX(), Y: r.Y + top, W: sm.Width(), H: thumb}, colors.ThumbBackground().Mix(colors.ThumbBackgroundHover(), hover), sm.Radius())
	})
}
