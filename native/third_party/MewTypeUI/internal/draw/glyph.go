package draw

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// Only constructors write this table, during package initialization. The source
// rectangles describe our licensed artwork, never the reference client's paths.
var glyphSources = map[*ui.SVG]*glyphSource{}
var emptyGlyph = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`))

type glyphSource struct {
	role             GlyphRole
	canvasW, canvasH float32
	bounds           ui.Rect
	body             string
	filled           bool
}

// Lucide records the centerline bounds of an unchanged Lucide path.
func Lucide(body string, bounds ui.Rect) *ui.SVG {
	s := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
	glyphSources[s] = &glyphSource{body: body, bounds: bounds, canvasW: 24, canvasH: 24}
	return s
}

// RecordFill attaches bounds to original library artwork with no pen.
func RecordFill(source *ui.SVG, body string, bounds ui.Rect) {
	glyphSources[source] = &glyphSource{body: body, bounds: bounds, filled: true}
}

type GlyphRole uint8

const (
	HeaderSearch GlyphRole = iota + 1
	HeaderCall
	HeaderMenu
	Back
	Close
	Attach
	Emoji
	Send
	Save
	Reply
	Edit
	Play
	Pause
	Download
	FileCancel
	File
	Copy
	Submenu
	Check
	Search
	Pin
	Verified
	Latest
	Recent
	Folder
	Group
	Channel
	Bot
	Lock
	ControlBack
	Dropdown
)

// Configure is called only during package initialization, alongside SVG data.
func Configure(source *ui.SVG, role GlyphRole, canvasW, canvasH float32) {
	s := glyphSources[source]
	s.role, s.canvasW, s.canvasH = role, canvasW, canvasH
}

func Resolve(c *ui.Context, source *ui.SVG, canvasW, canvasH float32) Glyph {
	s := glyphSources[source]
	if s == nil || s.role == 0 {
		return Glyph{source, canvasW, canvasH, canvasW, canvasH}
	}
	m := theme.T(c).Metrics().Glyph()
	var w, h float32
	switch s.role {
	case HeaderSearch:
		w, h = m.HeaderSearch().Width(), m.HeaderSearch().Height()
	case HeaderCall:
		w, h = m.HeaderCall().Width(), m.HeaderCall().Height()
	case HeaderMenu:
		w, h = m.HeaderMenu().Width(), m.HeaderMenu().Height()
	case Back:
		w, h = m.Back().Width(), m.Back().Height()
	case Close:
		w, h = m.Close().Width(), m.Close().Height()
	case Attach:
		w, h = m.Attach().Width(), m.Attach().Height()
	case Emoji:
		w, h = m.Emoji().Width(), m.Emoji().Height()
	case Send:
		w, h = m.Send().Width(), m.Send().Height()
	case Save:
		w, h = m.Save().Width(), m.Save().Height()
	case Reply:
		w, h = m.Reply().Width(), m.Reply().Height()
	case Edit:
		w, h = m.Edit().Width(), m.Edit().Height()
	case Play:
		w, h = m.Play().Width(), m.Play().Height()
	case Pause:
		w, h = m.Pause().Width(), m.Pause().Height()
	case Download:
		w, h = m.Download().Width(), m.Download().Height()
	case FileCancel:
		w, h = m.FileCancel().Width(), m.FileCancel().Height()
	case File:
		w, h = m.File().Width(), m.File().Height()
	case Copy:
		w, h = m.Copy().Width(), m.Copy().Height()
	case Submenu:
		w, h = m.Submenu().Width(), m.Submenu().Height()
	case Check:
		w, h = m.Check().Width(), m.Check().Height()
	case Search:
		w, h = m.Search().Width(), m.Search().Height()
	case Pin:
		w, h = m.Pin().Width(), m.Pin().Height()
	case Verified:
		w, h = m.Verified().Width(), m.Verified().Height()
	case Latest:
		w, h = m.Latest().Width(), m.Latest().Height()
	case Recent:
		w, h = m.Recent().Width(), m.Recent().Height()
	case Folder:
		w, h = m.Folder().Width(), m.Folder().Height()
	case Group:
		w, h = m.Group().Width(), m.Group().Height()
	case Channel:
		w, h = m.Channel().Width(), m.Channel().Height()
	case Bot:
		w, h = m.Bot().Width(), m.Bot().Height()
	case Lock:
		w, h = m.Lock().Width(), m.Lock().Height()
	case ControlBack:
		w, h = m.ControlBack().Width(), m.ControlBack().Height()
	case Dropdown:
		w, h = m.Dropdown().Width(), m.Dropdown().Height()
	}
	sx, sy := canvasW/s.canvasW, canvasH/s.canvasH
	stroke := m.Stroke()
	if min(s.canvasW, s.canvasH) <= 16 {
		stroke = m.SmallStroke()
	}
	return Fit(c, source, canvasW, canvasH, w*sx, h*sy, stroke*min(sx, sy))
}

// IsHost identifies SVGs whose geometry is owned by the application.
func IsHost(source *ui.SVG) bool {
	_, known := glyphSources[source]
	return !known
}

func Element(c *ui.Context, source *ui.SVG, canvasW, canvasH float32) ui.Element {
	if IsHost(source) {
		ratio := theme.T(c).Metrics().Glyph().HostInkRatio()
		return ui.Box(c).Size(canvasW, canvasH).Shrink(0).Children(func() {
			ui.Icon(c, source).Size(canvasW*ratio, canvasH*ratio).Attach(ui.AnchorCenter, ui.AnchorCenter)
		})
	}
	return Resolve(c, source, canvasW, canvasH).Element(c)
}

// Glyph keeps the ink clip with its SVG so raster filtering cannot enlarge it.
type Glyph struct {
	svg                             *ui.SVG
	canvasW, canvasH, width, height float32
}

func (g Glyph) Element(c *ui.Context) ui.Element {
	if g.canvasW == 0 || g.canvasH == 0 || g.width == 0 || g.height == 0 {
		return ui.Box(c).Size(g.canvasW, g.canvasH)
	}
	if g.width == g.canvasW && g.height == g.canvasH {
		return ui.Icon(c, g.svg).Size(g.canvasW, g.canvasH)
	}
	return ui.Box(c).Size(g.canvasW, g.canvasH).Shrink(0).Children(func() {
		ui.Box(c).Size(g.width, g.height).Attach(ui.AnchorCenter, ui.AnchorCenter).Clip().Children(func() {
			ui.Icon(c, g.svg).Size(g.width, g.height).Attach(ui.AnchorCenter, ui.AnchorCenter)
		})
	})
}

func (g Glyph) Paint(p *ui.Painter, r ui.Rect, color ui.Color) {
	g.clip(p, r, 0, func(ink ui.Rect) { p.Icon(g.svg, ink, color) })
}

func (g Glyph) clip(p *ui.Painter, r ui.Rect, fringe float32, paint func(ui.Rect)) {
	if g.canvasW == 0 || g.canvasH == 0 || g.width == 0 || g.height == 0 {
		return
	}
	w, h := r.W*g.width/g.canvasW, r.H*g.height/g.canvasH
	ink := ui.Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	if fringe != 0 {
		// The sampler boundary must keep the ink ratio too; independent
		// one-pixel insets would squash narrow glyphs. paint/icon-glyph.md#图形限框.
		switch {
		case w == h:
			w = max(0, w-fringe)
			h = w
		case w > h:
			clipped := max(0, w-fringe)
			h *= clipped / w
			w = clipped
		default:
			clipped := max(0, h-fringe)
			w *= clipped / h
			h = clipped
		}
	}
	p.Clip(ui.Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}, 0, func() { paint(ink) })
}

type glyphSize struct{ canvasW, canvasH, width, height, stroke float32 }
type glyphVariant struct {
	size glyphSize
	svg  *ui.SVG
}
type glyphCache struct{ variants []glyphVariant }

// Fit keeps the layout canvas and centers only the painted glyph within it.
// paint/icon-glyph.md#图形限框. Cache ownership is per window and source; a
// changing theme replaces the oldest variant rather than retaining theme history.
func Fit(c *ui.Context, source *ui.SVG, canvasW, canvasH, width, height, stroke float32) Glyph {
	if canvasW == 0 || canvasH == 0 || width == 0 || height == 0 {
		return Glyph{emptyGlyph, canvasW, canvasH, width, height}
	}
	stroke = min(stroke, min(width, height)/2)
	// The final pen stays in DIP while both path axes share one scale.
	s := glyphSources[source]
	if s == nil {
		return Glyph{source, canvasW, canvasH, canvasW, canvasH}
	}
	if s.filled {
		stroke = 0
	}
	key := glyphSize{canvasW, canvasH, width, height, stroke}
	cache := ui.Local(c.Root(), s, func() glyphCache { return glyphCache{} })
	for _, v := range cache.variants {
		if v.size == key {
			inkW, inkH := v.svg.Size()
			return Glyph{v.svg, canvasW, canvasH, inkW, inkH}
		}
	}
	b := s.bounds
	scale := min((width-stroke)/b.W, (height-stroke)/b.H)
	if stroke > 0 {
		// Keep the unchanged Lucide pen-2 artwork's ink ratio within the
		// acceptance band as well as preserving its centerline geometry.
		// Fixed output pens can require leaving more space on either axis.
		w, h, t := float64(b.W), float64(b.H), float64(stroke)
		original := (w + 2) / (h + 2)
		ratio := (w*float64(scale) + t) / (h*float64(scale) + t)
		// Reserve half of the user's 2% budget for raster edge quantization.
		limited := max(original*.99, min(ratio, original*1.01))
		if limited != ratio {
			scale = float32(t * (1 - limited) / (limited*h - w))
		}
	}
	pen := stroke / scale
	width, height = b.W*scale+stroke, b.H*scale+stroke
	body := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%g" height="%g" viewBox="0 0 %g %g"><g transform="translate(%g %g) scale(%g) translate(%g %g)" fill="none" stroke="currentColor" stroke-width="%g" stroke-linecap="round" stroke-linejoin="round">%s</g></svg>`, width, height, width, height, width/2, height/2, scale, -b.X-b.W/2, -b.Y-b.H/2, pen, s.body)
	v := glyphVariant{key, ui.MustParseSVG([]byte(body))}
	const variantsPerSource = 8 // Finite simultaneous uses; not a visual constant.
	if len(cache.variants) == variantsPerSource {
		copy(cache.variants, cache.variants[1:])
		cache.variants = cache.variants[:variantsPerSource-1]
	}
	cache.variants = append(cache.variants, v)
	return Glyph{v.svg, canvasW, canvasH, width, height}
}

// HostGlyph fits the conventional Lucide ink envelope inside the given canvas.
// Arbitrary host paths can have different bounds; see the recorded exception.
func HostGlyph(c *ui.Context, source *ui.SVG, canvasW, canvasH, width, height float32) ui.Element {
	if canvasW == 0 || canvasH == 0 {
		return ui.Box(c).Size(canvasW, canvasH)
	}
	m := theme.T(c).Metrics().Glyph()
	if m.HostInkRatio() == 0 || m.HostHeightRatio() == 0 {
		return ui.Box(c).Size(canvasW, canvasH)
	}
	scale := min(width/(m.HostInkRatio()*canvasW), height/(m.HostHeightRatio()*canvasH))
	return ui.Box(c).Size(canvasW, canvasH).Shrink(0).Children(func() {
		ui.Icon(c, source).Size(canvasW*scale, canvasH*scale).Attach(ui.AnchorCenter, ui.AnchorCenter)
	})
}
