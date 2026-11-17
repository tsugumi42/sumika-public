package chat

import (
	"math"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type serviceContour struct {
	value     string
	font      ui.Font
	width     float32
	direction ui.Direction
	padding   [4]float32
	path      ui.Path
	widths    []float32
	levels    []float32
	points    []ui.Point
}

// Each text line contributes its padded rectangle. Their union produces the
// stepped outline; rounding convex and concave vertices gives the connected
// service surface without layering translucent paint over itself.
func serviceBackground(c *ui.Context, e ui.Element, label string, color ui.Color) {
	t := theme.T(c)
	m := t.Metrics().Service()
	style := t.Typography().Service()
	font := ui.Font{Family: t.Typography().Font(), Size: style.Size() * c.Preferences().TextScale, Weight: style.Weight(), Lang: c.Locale()}
	direction := c.Direction()
	padding := [4]float32{m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()}
	cache := ui.Local(e, "chat.service.contour", func() serviceContour { return serviceContour{} })
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		width := r.W - m.PaddingLeft() - m.PaddingRight()
		if cache.value != label || cache.font != font || cache.width != width || cache.direction != direction || cache.padding != padding || len(cache.widths) == 0 {
			cache.value, cache.font, cache.width, cache.direction, cache.padding = label, font, width, direction, padding
			cache.build(m)
		}
		p.Transform(ui.Translation(r.X, r.Y), 0, 0, func() { p.FillPath(&cache.path, color) })
	})
}

func (s *serviceContour) build(m theme.MetricsService) {
	layout := ui.ShapeRichTextWith([]ui.Span{{Text: s.value}}, s.font, ui.TextShape{Width: s.width, Align: ui.Center, Direction: s.direction})
	lines := layout.Lines()
	_, lineHeight := ui.ShapeText("M", s.font, 0).Size()
	large := float32(int(lineHeight+m.PaddingTop()+m.PaddingBottom()) / 2)
	inner := max(0, lineHeight-large)
	shrink := float32(int(inner*2) / 3)
	minStep := 2 * (large + inner - shrink)
	s.widths = s.widths[:0]
	for _, line := range lines {
		s.widths = append(s.widths, line.Rect.W)
	}
	// Width steps too small for both arcs merge into the wider neighboring band.
	for i := range s.widths {
		for j := i; j > 0 && float32(math.Abs(float64(s.widths[j]-s.widths[j-1]))) < minStep; j-- {
			w := max(s.widths[j], s.widths[j-1])
			s.widths[j], s.widths[j-1] = w, w
		}
	}
	var previousWidth float32
	previousInset := false
	for i, w := range s.widths {
		inset := previousInset && w == previousWidth || i > 0 && previousWidth > w || i+1 < len(s.widths) && s.widths[i+1] > w
		if inset {
			s.widths[i] -= 2 * shrink
		}
		previousWidth, previousInset = w, inset
	}
	s.levels = s.levels[:0]
	for _, line := range lines {
		s.levels = append(s.levels, line.Rect.Y, line.Rect.Y+line.Rect.H+m.PaddingTop()+m.PaddingBottom())
	}
	slices.Sort(s.levels)
	s.levels = slices.Compact(s.levels)
	s.points = s.points[:0]
	center := (s.width + m.PaddingLeft() + m.PaddingRight()) / 2
	var previous float32
	for i := 0; i+1 < len(s.levels); i++ {
		top, bottom := s.levels[i], s.levels[i+1]
		if top == bottom {
			continue
		}
		var width float32
		middle := (top + bottom) / 2
		for j, line := range lines {
			if middle >= line.Rect.Y && middle < line.Rect.Y+line.Rect.H+m.PaddingTop()+m.PaddingBottom() {
				width = max(width, s.widths[j]+m.PaddingLeft()+m.PaddingRight())
			}
		}
		right := center + width/2
		if len(s.points) == 0 {
			s.points = append(s.points, ui.Point{X: right, Y: top})
		} else if width != previous {
			s.points = append(s.points, ui.Point{X: s.points[len(s.points)-1].X, Y: top}, ui.Point{X: right, Y: top})
		}
		previous = width
		if i+2 == len(s.levels) {
			s.points = append(s.points, ui.Point{X: right, Y: bottom})
		}
	}
	rightCount := len(s.points)
	for i := rightCount - 1; i >= 0; i-- {
		s.points = append(s.points, ui.Point{X: 2*center - s.points[i].X, Y: s.points[i].Y})
	}
	roundedContour(&s.path, s.points, large, max(0, inner))
}

func roundedContour(path *ui.Path, points []ui.Point, outer, inner float32) {
	path.Reset()
	for i, current := range points {
		previous, next := points[(i+len(points)-1)%len(points)], points[(i+1)%len(points)]
		ax, ay, bx, by := current.X-previous.X, current.Y-previous.Y, next.X-current.X, next.Y-current.Y
		al, bl := float32(math.Hypot(float64(ax), float64(ay))), float32(math.Hypot(float64(bx), float64(by)))
		if al == 0 || bl == 0 {
			continue
		}
		ax, ay, bx, by = ax/al, ay/al, bx/bl, by/bl
		radius := outer
		if ax*by-ay*bx < 0 {
			radius = inner
		}
		radius = min(radius, al/2, bl/2)
		inX, inY, outX, outY := current.X-ax*radius, current.Y-ay*radius, current.X+bx*radius, current.Y+by*radius
		if i == 0 {
			path.MoveTo(inX, inY)
		} else {
			path.LineTo(inX, inY)
		}
		// Cubic approximation of a quarter circle; a mathematical constant.
		const k = float32(.5522847498)
		path.CubeTo(inX+ax*radius*k, inY+ay*radius*k, outX-bx*radius*k, outY-by*radius*k, outX, outY)
	}
	path.Close()
}
