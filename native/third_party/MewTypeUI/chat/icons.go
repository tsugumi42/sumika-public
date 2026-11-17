package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
)

// Lucide 1.58.0 artwork, redistributed under ISC/MIT; see THIRD_PARTY_NOTICES.md.
// Parsing happens once, outside component builds. Hosts may supply other SVGs.
func lucide(paths string, x, y, w, h float32) *ui.SVG {
	return draw.Lucide(paths, ui.Rect{X: x, Y: y, W: w, H: h})
}

var (
	backIcon       = lucide(`<path d="m12 19-7-7 7-7"/><path d="M19 12H5"/>`, 5, 5, 14, 14)
	downIcon       = lucide(`<path d="m6 9 6 6 6-6"/>`, 6, 9, 12, 6)
	sentIcon       = lucide(`<path d="M20 6 9 17l-5-5"/>`, 4, 6, 16, 11)
	readIcon       = lucide(`<path d="M18 6 7 17l-5-5"/><path d="m22 10-7.5 7.5L13 16"/>`, 2, 6, 20, 11.5)
	pendingIcon    = lucide(`<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>`, 2, 2, 20, 20)
	viewsInfoIcon  = lucide(`<path d="M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0"/><circle cx="12" cy="12" r="3"/>`, 2, 5, 20, 14)
	replyInfoIcon  = lucide(`<path d="M2.992 16.342a2 2 0 0 1 .094 1.167l-1.065 3.29a1 1 0 0 0 1.236 1.168l3.413-.998a2 2 0 0 1 1.099.092 10 10 0 1 0-4.777-4.719"/>`, 2, 2, 20, 20)
	pinInfoIcon    = lucide(`<path d="M12 17v5"/><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"/>`, 5, 2, 14, 20)
	silentInfoIcon = lucide(`<path d="M11 4.702a.7.7 0 0 0-1.203-.498L6.413 7.587A1.4 1.4 0 0 1 5.416 8H3a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2.416a1.4 1.4 0 0 1 .997.413l3.383 3.384A.7.7 0 0 0 11 19.298z"/><path d="m16.5 14.5 5-5"/><path d="m16.5 9.5 5 5"/>`, 2, 4, 19.5, 16)
	searchIcon     = lucide(`<path d="m21 21-4.34-4.34"/><circle cx="11" cy="11" r="8"/>`, 3, 3, 18, 18)
	menuIcon       = lucide(`<circle cx="12" cy="12" r="1"/><circle cx="12" cy="5" r="1"/><circle cx="12" cy="19" r="1"/>`, 11, 4, 2, 16)
	cancelIcon     = lucide(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`, 6, 6, 12, 12)
	callIcon       = lucide(`<path d="M13.832 16.568a1 1 0 0 0 1.213-.303l.355-.465A2 2 0 0 1 17 15h3a2 2 0 0 1 2 2v3a2 2 0 0 1-2 2A18 18 0 0 1 2 4a2 2 0 0 1 2-2h3a2 2 0 0 1 2 2v3a2 2 0 0 1-.8 1.6l-.468.351a1 1 0 0 0-.292 1.233 14 14 0 0 0 6.392 6.384"/>`, 2, 2, 20, 20)
)

func init() {
	draw.Configure(backIcon, draw.Back, 24, 24)
	draw.Configure(downIcon, draw.Latest, 24, 24)
	draw.Configure(searchIcon, draw.HeaderSearch, 24, 24)
	draw.Configure(callIcon, draw.HeaderCall, 24, 24)
	draw.Configure(menuIcon, draw.HeaderMenu, 24, 24)
	draw.Configure(cancelIcon, draw.Close, 24, 24)
}
