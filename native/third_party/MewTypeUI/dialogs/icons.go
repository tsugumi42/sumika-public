package dialogs

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
)

// Lucide 1.58.0 paths from CrychicUI/icon/svg. ISC and Feather MIT
// notices are in THIRD_PARTY_NOTICES.md. Only SVG data is shared.
func glyph(body string, x, y, w, h float32) *ui.SVG {
	return draw.Lucide(body, ui.Rect{X: x, Y: y, W: w, H: h})
}

const folderShape = `<path fill="currentColor" d="M8 7h6c1 0 1.5.5 2 1l2 2h10a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2Z"/>`

var (
	muteIcon  = glyph(`<path d="M10.268 21a2 2 0 0 0 3.464 0"/><path d="M17 17H4a1 1 0 0 1-.74-1.673C4.59 13.956 6 12.499 6 8a6 6 0 0 1 .258-1.742"/><path d="m2 2 20 20"/><path d="M8.668 3.01A6 6 0 0 1 18 8c0 2.687.77 4.653 1.707 6.05"/>`, 2, 2, 20, 20)
	pinIcon   = glyph(`<path d="M12 17v5"/><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"/>`, 5, 2, 14, 20)
	clockIcon = glyph(`<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>`, 2, 2, 20, 20)
	sentIcon  = glyph(`<path d="M20 6 9 17l-5-5"/>`, 4, 6, 16, 11)
	readIcon  = glyph(`<path d="M18 6 7 17l-5-5"/><path d="m22 10-7.5 7.5L13 16"/>`, 2, 6, 20, 11.5)
	// Original filled folder silhouette; no client artwork. The 36 DIP canvas matches the rail slot.
	folderIcon       = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="36" height="36" viewBox="0 0 36 36">` + folderShape + `</svg>`))
	groupIcon        = glyph(`<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><path d="M16 3.128a4 4 0 0 1 0 7.744"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><circle cx="9" cy="7" r="4"/>`, 2, 3, 20, 18)
	channelIcon      = glyph(`<path d="M11 6a13 13 0 0 0 8.4-2.8A1 1 0 0 1 21 4v12a1 1 0 0 1-1.6.8A13 13 0 0 0 11 14H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2z"/><path d="M6 14a12 12 0 0 0 2.4 7.2 2 2 0 0 0 3.2-2.4A8 8 0 0 1 10 14"/><path d="M8 6v8"/>`, 3, 3, 18, 19)
	botIcon          = glyph(`<path d="M12 8V4H8"/><rect width="16" height="12" x="4" y="8" rx="2"/><path d="M2 14h2"/><path d="M20 14h2"/><path d="M15 13v2"/><path d="M9 13v2"/>`, 2, 4, 20, 16)
	archiveIcon      = glyph(`<rect width="20" height="5" x="2" y="3" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>`, 2, 3, 20, 18)
	verifiedIcon     = glyph(`<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="m9 12 2 2 4-4"/>`, 4, 2, 16, 20)
	clearIcon        = glyph(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`, 6, 6, 12, 12)
	lockIcon         = glyph(`<rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>`, 3, 2, 18, 20)
	subscriptionFill = glyph(`<path fill="currentColor" d="M11.525 2.295a.53.53 0 0 1 .95 0l2.31 4.679a2.123 2.123 0 0 0 1.595 1.16l5.166.756a.53.53 0 0 1 .294.904l-3.736 3.638a2.123 2.123 0 0 0-.611 1.878l.882 5.14a.53.53 0 0 1-.771.56l-4.618-2.428a2.122 2.122 0 0 0-1.973 0L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.122 2.122 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.122 2.122 0 0 0 1.597-1.16z"/>`, 2, 2, 20, 20)
)

func init() {
	draw.Configure(pinIcon, draw.Pin, 20, 20)
	draw.Configure(verifiedIcon, draw.Verified, 20, 20)
	draw.Configure(groupIcon, draw.Group, 16, 10)
	draw.Configure(channelIcon, draw.Channel, 12, 11)
	draw.Configure(botIcon, draw.Bot, 16, 11)
	draw.Configure(lockIcon, draw.Lock, 20, 20)
	draw.Configure(clearIcon, draw.Close, 11, 11)
	draw.RecordFill(folderIcon, folderShape, ui.Rect{X: 6, Y: 7, W: 24, H: 21})
	draw.Configure(folderIcon, draw.Folder, 36, 36)
}
