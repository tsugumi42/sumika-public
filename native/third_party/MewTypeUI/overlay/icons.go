package overlay

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
)

// Lucide 1.58.0, ISC; Feather-derived shapes MIT. See THIRD_PARTY_NOTICES.md.
func glyph(shape string, x, y, w, h float32) *ui.SVG {
	return draw.Lucide(shape, ui.Rect{X: x, Y: y, W: w, H: h})
}

var (
	closeIcon   = glyph(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`, 6, 6, 12, 12)
	leftIcon    = glyph(`<path d="m15 18-6-6 6-6"/>`, 9, 6, 6, 12)
	rightIcon   = glyph(`<path d="m9 18 6-6-6-6"/>`, 9, 6, 6, 12)
	checkIcon   = glyph(`<path d="M20 6 9 17l-5-5"/>`, 4, 6, 16, 11)
	zoomInIcon  = glyph(`<circle cx="11" cy="11" r="8"/><line x1="21" x2="16.65" y1="21" y2="16.65"/><line x1="11" x2="11" y1="8" y2="14"/><line x1="8" x2="14" y1="11" y2="11"/>`, 3, 3, 18, 18)
	zoomOutIcon = glyph(`<circle cx="11" cy="11" r="8"/><line x1="21" x2="16.65" y1="21" y2="16.65"/><line x1="8" x2="14" y1="11" y2="11"/>`, 3, 3, 18, 18)
)

func init() {
	draw.Configure(closeIcon, draw.Close, 24, 24)
	draw.Configure(leftIcon, draw.Submenu, 24, 24)
	draw.Configure(rightIcon, draw.Submenu, 24, 24)
	draw.Configure(checkIcon, draw.Check, 24, 24)
}
