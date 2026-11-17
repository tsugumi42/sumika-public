package content

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// paint/reaction-size.md#绘制合同: resource resolution must not determine
// visible size; actual opaque pixels catch a 32-DIP canvas used as artwork.
func TestReactionBitmapInkBounds(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Reaction()
	for _, scale := range []float32{1, 1.25, 1.5} {
		for _, side := range []int{16, 256} {
			t.Run(fmt.Sprintf("scale=%g/source=%d", scale, side), func(t *testing.T) {
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c, th)
					ui.Column(c).Padding(24).Children(func() {
						Reactions(c, []Reaction{{ID: "bitmap", Emoji: "bitmap", Image: fixture(side, side), Count: 1}}, ReactionOptions{})
					})
				}, 200, 100)
				tt.SetScale(scale)
				pill := box(t, tt, "bitmap 1")
				ink := reactionInkBounds(tt.Image())
				want := m.ImageSize() / 2
				cx, cy := (float32(ink.Min.X+ink.Max.X)/2)/scale, (float32(ink.Min.Y+ink.Max.Y)/2)/scale
				if math.Abs(float64(float32(ink.Dx())/scale-want)) > 1 || math.Abs(float64(float32(ink.Dy())/scale-want)) > 1 {
					t.Fatalf("bitmap ink %v at scale %g; want %g DIP square", ink, scale, want)
				}
				if math.Abs(float64(cx-(pill.X+m.PaddingLeft()+m.IconSize()/2))) > 1 || math.Abs(float64(cy-(pill.Y+m.PaddingTop()+m.IconSize()/2))) > 1 {
					t.Fatalf("bitmap center (%g,%g) outside reaction slot center in %v", cx, cy, pill)
				}
			})
		}
	}
}

// Emoji is a resource-sized graphic; only the count follows text accessibility.
// paint/reaction-size.md#绘制合同. Compare actual glyph pixels, including fallback.
func TestReactionUnicodeIgnoresTextScale(t *testing.T) {
	d := theme.Light().Definition()
	for key := range d.Colors {
		d.Colors[key] = "#00000000"
	}
	d.Colors["window.background"] = "#000000"
	d.Colors["reaction.in.text"] = "#ffffff"
	th, err := theme.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, scale := range []float32{1, 1.25, 1.5} {
		t.Run(fmt.Sprint(scale), func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, th)
				ui.Column(c).Padding(24).Children(func() {
					Reactions(c, []Reaction{{ID: "unicode", Emoji: "👍"}}, ReactionOptions{})
				})
			}, 120, 100)
			tt.SetScale(scale)
			tt.SetPreferences(ui.Preferences{TextScale: 1})
			original := append([]byte(nil), tt.Image().Pix...)
			tt.SetPreferences(ui.Preferences{TextScale: 2})
			if !bytes.Equal(original, tt.Image().Pix) {
				t.Fatal("text accessibility changed reaction glyph pixels")
			}
		})
	}
}

func reactionInkBounds(img *image.RGBA) image.Rectangle {
	r := image.Rectangle{Min: img.Bounds().Max}
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			p := img.RGBAAt(x, y)
			if p.R > 180 && p.G < 130 && p.B < 100 && p.A > 200 {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}
