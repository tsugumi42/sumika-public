package dialogs

import (
	"image"
	"image/color"
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type ttlImage struct {
	avatar   *ui.Bitmap
	icon     *ui.SVG
	revision uint64
	initials string
	kind     PeerKind
	codes    bool
	theme    *theme.Theme
	scale    float32
	bitmap   *ui.Bitmap
}

// Bitmap intentionally hides its CPU pixels. Rasterize this tiny prepared image
// only when its pixels/theme change; ordinary rows reuse the cropped result.
func ttlBackdrop(c *ui.Context, host ui.Element, item Conversation) *ui.Bitmap {
	t := theme.T(c)
	m := t.Metrics().Conversation()
	cache := ui.Local(host, "ttl-image", func() ttlImage { return ttlImage{} })
	first, second := avatarColors(t, item)
	fg := t.Colors().Premium().ButtonForeground()
	scale := c.Preferences().TextScale
	if cache.bitmap != nil && cache.avatar == item.Avatar && cache.icon == item.AvatarIcon && cache.revision == item.AvatarRevision && cache.initials == item.Initials && cache.kind == item.Kind && cache.codes == item.VerificationCodes && cache.theme == t && cache.scale == scale {
		return cache.bitmap
	}
	side := int(math.Ceil(float64(m.AvatarSize())))
	full := ui.Render(func(ctx *ui.Context) {
		theme.Apply(ctx, t)
		ui.Box(ctx).Size(m.AvatarSize(), m.AvatarSize()).Gradient(first, second, 180).Center().Blur(m.TtlBlur()).Children(func() { avatarContent(ctx, item, m.AvatarSize(), scale) })
	}, side, side, 1)
	part := int(math.Ceil(float64(m.TtlSize())))
	crop := image.NewRGBA(image.Rect(0, 0, part, part))
	var r, g, b float64
	for y := range part {
		for x := range part {
			pixel := full.RGBAAt(side-part+x, side-part+y)
			if pixel.A != 0 && pixel.A != 255 {
				pixel.R = uint8(uint32(pixel.R) * 255 / uint32(pixel.A))
				pixel.G = uint8(uint32(pixel.G) * 255 / uint32(pixel.A))
				pixel.B = uint8(uint32(pixel.B) * 255 / uint32(pixel.A))
			}
			r += float64(pixel.R)
			g += float64(pixel.G)
			b += float64(pixel.B)
			crop.SetRGBA(x, y, color.RGBA{R: pixel.R, G: pixel.G, B: pixel.B, A: 255})
		}
	}
	n := float64(part * part)
	background := luminance(r/n, g/n, b/n)
	foreground := luminance(float64(fg.R), float64(fg.G), float64(fg.B))
	contrast := (max(background, foreground) + .05) / (min(background, foreground) + .05)
	if contrast < 4.5 { // dialogs_row.cpp:264-273, contrast-triggered 20% black.
		for i := 0; i < len(crop.Pix); i += 4 {
			for j := range 3 {
				crop.Pix[i+j] = uint8(float32(crop.Pix[i+j]) * (1 - m.TtlDarken()))
			}
		}
	}
	*cache = ttlImage{avatar: item.Avatar, icon: item.AvatarIcon, revision: item.AvatarRevision, initials: item.Initials, kind: item.Kind, codes: item.VerificationCodes, theme: t, scale: scale, bitmap: ui.NewBitmap(crop)}
	return cache.bitmap
}

func luminance(r, g, b float64) float64 {
	linear := func(v float64) float64 {
		v /= 255
		if v <= .04045 {
			return v / 12.92
		}
		return math.Pow((v+.055)/1.055, 2.4)
	}
	return .2126*linear(r) + .7152*linear(g) + .0722*linear(b)
}

func ttlTimer(c *ui.Context, host ui.Element, value string) {
	t := theme.T(c)
	m := t.Metrics().Conversation()
	fg := t.Colors().Premium().ButtonForeground()
	path := ui.Local(host, "timer-path", func() ui.Path { return ui.Path{} })
	// Timer text is at most two runes, matching ttl_icon.cpp:25.
	count := 0
	for index := range value {
		if count == 2 {
			value = value[:index]
			break
		}
		count++
	}
	ui.Box(c).Absolute().Fill().Padding(m.TtlInset()).Center().Children(func() { text(c, value, t.Typography().ConversationTTL(), fg).SingleLine() })
	host.DrawOver(func(p *ui.Painter, r ui.Rect) {
		p.Transform(ui.Translation(r.X, r.Y), 0, 0, func() {
			radius := r.W/2 - m.TtlInset()
			path.Reset()
			arc(path, float64(r.W/2), float64(r.H/2), float64(radius), float64(radius), math.Pi/2, math.Pi)
			p.StrokePath(path, m.TtlStroke(), fg)
			path.Reset()
			arc(path, float64(r.W/2), float64(r.H/2), float64(radius), float64(radius), -math.Pi/2, math.Pi)
			path.Dash(0, 0, m.TtlStroke()*3)
			p.StrokePath(path, m.TtlStroke(), fg)
		})
	})
}
