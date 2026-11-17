package content

import (
	"image"
	"image/color"
	imagedraw "image/draw"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// WCAG 2.2 relative luminance, using the same sRGB convention as dialogs.
func statusLuminance(c color.RGBA) float64 {
	linear := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= .04045 {
			return x / 12.92
		}
		return math.Pow((x+.055)/1.055, 2.4)
	}
	return .2126*linear(c.R) + .7152*linear(c.G) + .0722*linear(c.B)
}

func TestMediaStatusLegibilityAndBounds(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	imagedraw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, imagedraw.Src)
	bitmap := ui.NewBitmap(img)
	loading, failed := "35% · 点击取消", "网络失败，点击重试"
	for _, tc := range []struct {
		name            string
		album, selected bool
		width           float32
	}{
		{name: "single", width: 200}, {name: "single-selected", selected: true, width: 200},
		{name: "album", album: true, width: 430}, {name: "album-selected", album: true, selected: true, width: 430},
		{name: "album-narrow", album: true, width: 240}, {name: "album-narrow-selected", album: true, selected: true, width: 240},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				surface(c, func() {
					appearance := Appearance{Selected: tc.selected}
					if !tc.album {
						Photo(c, Media{ID: "single", Label: "single", Bitmap: bitmap, Status: MediaLoading, Progress: .35, StatusText: loading}, MediaOptions{Appearance: appearance, Width: tc.width})
					} else {
						Album(c, []Media{
							{ID: "ready", Label: "ready", Bitmap: bitmap},
							{ID: "remote", Label: "remote", Bitmap: bitmap, Status: MediaRemote, StatusText: "点击下载"},
							{ID: "loading", Label: "loading", Bitmap: bitmap, Status: MediaLoading, Progress: .35, StatusText: loading},
							{ID: "failed", Label: "failed", Bitmap: bitmap, Status: MediaFailed, StatusText: failed},
						}, AlbumOptions{Appearance: appearance, Width: tc.width})
					}
				})
			}, 500, 600)
			targets := []struct{ owner, label string }{{"single", loading}}
			if tc.album {
				targets = []struct{ owner, label string }{{"loading", loading}, {"failed", failed}}
			}
			m := theme.Light().Metrics().Media()
			for _, target := range targets {
				owner, label := box(t, tt, target.owner), box(t, tt, target.label)
				// paint/album-state.md#状态文字底板: chat.style:102–103, 4 + 8/2 DIP.
				check(t, "status text fits padded media bounds", label.X >= owner.X+m.BadgeInset()+m.BadgePaddingX()-1 && label.X+label.W <= owner.X+owner.W-m.BadgeInset()-m.BadgePaddingX()+1 && label.Y+label.H <= owner.Y+owner.H-m.BadgeInset()-m.BadgePaddingY()+1)
				back := statusLuminance(pixel(tt, label.X-m.BadgePaddingX()/2, label.Y+label.H/2))
				rendered := tt.Image()
				strongest := float64(0)
				for y := max(0, int(label.Y)); y < min(rendered.Rect.Dy(), int(math.Ceil(float64(label.Y+label.H)))); y++ {
					for x := max(0, int(label.X)); x < min(rendered.Rect.Dx(), int(math.Ceil(float64(label.X+label.W)))); x++ {
						foreground := statusLuminance(rendered.RGBAAt(x, y))
						strongest = max(strongest, (max(foreground, back)+.05)/(min(foreground, back)+.05))
					}
				}
				// WCAG SC 1.4.3: normal text, including the selected overlay composite.
				check(t, "rendered status feedback remains readable on white media", strongest >= 4.5)
			}
		})
	}
}
