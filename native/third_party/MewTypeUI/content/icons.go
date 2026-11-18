package content

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
)

// Paths are Lucide 1.58.0, distributed through CrychicUI. See THIRD_PARTY_NOTICES.md.
func glyph(paths string, x, y, w, h float32) *ui.SVG {
	return draw.Lucide(paths, ui.Rect{X: x, Y: y, W: w, H: h})
}

var (
	playIcon     = glyph(`<path d="M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z"/>`, 5, 3, 16, 18)
	pauseIcon    = glyph(`<rect x="14" y="3" width="5" height="18" rx="1"/><rect x="5" y="3" width="5" height="18" rx="1"/>`, 5, 3, 14, 18)
	fileIcon     = glyph(`<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/>`, 4, 2, 16, 20)
	downloadIcon = glyph(`<path d="M12 15V3"/><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="m7 10 5 5 5-5"/>`, 3, 3, 18, 18)
	cancelIcon   = glyph(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`, 6, 6, 12, 12)
	copyIcon     = glyph(`<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>`, 2, 2, 20, 20)
)

func init() {
	draw.Configure(playIcon, draw.Play, 24, 24)
	draw.Configure(pauseIcon, draw.Pause, 24, 24)
	draw.Configure(fileIcon, draw.File, 24, 24)
	draw.Configure(downloadIcon, draw.Download, 24, 24)
	draw.Configure(cancelIcon, draw.FileCancel, 24, 24)
	draw.Configure(copyIcon, draw.Copy, 24, 24)
}
