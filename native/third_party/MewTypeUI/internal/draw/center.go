// Package draw keeps nested circular artwork in one coordinate system.
package draw

import "github.com/egoist/mygo/ui"

// Center paints around the box's floating-point center. Rectangles and icon
// masks then round symmetrically about zero before the shared translation;
// independently snapping their window coordinates would separate odd/even sizes.
func Center(p *ui.Painter, r ui.Rect, paint func()) {
	p.Transform(ui.Translation(r.X+r.W/2, r.Y+r.H/2), 0, 0, paint)
}

func Circle(p *ui.Painter, r ui.Rect, color ui.Color) {
	Center(p, r, func() {
		p.Fill(ui.Rect{X: -r.W / 2, Y: -r.H / 2, W: r.W, H: r.H}, color, min(r.W, r.H)/2)
	})
}

// Icon centers the SVG canvas, preserving the artwork's own optical balance.
func Icon(p *ui.Painter, r ui.Rect, icon Glyph, size float32, color ui.Color) {
	Center(p, r, func() {
		canvas := ui.Rect{X: -size / 2, Y: -size / 2, W: size, H: size}
		// A local symmetric clip preserves the floating center. Reserve one
		// device pixel on the longer axis, uniformly; paint/icon-glyph.md#图形限框.
		icon.clip(p, canvas, 1/p.Scale(), func(ink ui.Rect) { p.Icon(icon.svg, ink, color) })
	})
}

type circleIconKey struct{}
type circleIcon struct {
	icon       Glyph
	size       float32
	background ui.Color
	foreground ui.Color
	paint      func(*ui.Painter, ui.Rect)
}

// CircleIcon caches its drawing callback: updating a transfer row must not
// allocate a new closure just to change the icon or hover color.
func CircleIcon(c *ui.Context, icon *ui.SVG, size float32, background, foreground ui.Color) ui.Element {
	e := ui.Box(c)
	s := ui.Local(e, circleIconKey{}, func() circleIcon { return circleIcon{} })
	s.icon, s.size, s.background, s.foreground = Resolve(c, icon, size, size), size, background, foreground
	if s.paint == nil {
		s.paint = s.draw
	}
	return e.Draw(s.paint)
}

func (s *circleIcon) draw(p *ui.Painter, r ui.Rect) {
	Circle(p, r, s.background)
	Icon(p, r, s.icon, s.size, s.foreground)
}

// Radial paints the shared native-viewer loading artwork in one coordinate system.
// The gallery uses the same painter without opening a native window in a Tester.
func Radial(p *ui.Painter, r ui.Rect, path *ui.Path, stroke, rotation float32, background, foreground ui.Color, rtl bool) {
	Circle(p, r, background)
	Center(p, r, func() {
		paint := func() {
			p.Transform(ui.Rotation(rotation), 0, 0, func() {
				p.StrokePath(path, stroke, foreground)
			})
		}
		if rtl {
			p.Transform(ui.Scaling(-1, 1), 0, 0, paint)
		} else {
			paint()
		}
	})
}
