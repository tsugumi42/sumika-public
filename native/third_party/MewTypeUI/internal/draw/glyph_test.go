package draw

import (
	"fmt"
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

var aspectOutline = Lucide(`<rect x="2" y="7" width="20" height="10" rx="2"/>`, ui.Rect{X: 2, Y: 7, W: 20, H: 10})
var aspectFill = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24"><rect x="2" y="7" width="20" height="10" fill="currentColor"/></svg>`))
var aspectHost = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/></svg>`))
var aspectDown = Lucide(`<path d="m6 9 6 6 6-6"/>`, ui.Rect{X: 6, Y: 9, W: 12, H: 6})
var aspectMenu = Lucide(`<circle cx="12" cy="12" r="1"/><circle cx="12" cy="5" r="1"/><circle cx="12" cy="19" r="1"/>`, ui.Rect{X: 11, Y: 4, W: 2, H: 16})
var aspectTheme = func() *theme.Theme {
	th, err := theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase, Colors: map[string]string{"window.background": "#000000"}})
	if err != nil {
		panic(err)
	}
	return th
}()

func init() {
	RecordFill(aspectFill, `<rect x="2" y="7" width="20" height="10" fill="currentColor"/>`, ui.Rect{X: 2, Y: 7, W: 20, H: 10})
}

func aspectBounds(img *image.RGBA) image.Rectangle {
	b := image.Rectangle{Min: img.Bounds().Max}
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			if img.RGBAAt(x, y).R >= 128 {
				b.Min.X = min(b.Min.X, x)
				b.Min.Y = min(b.Min.Y, y)
				b.Max.X = max(b.Max.X, x+1)
				b.Max.Y = max(b.Max.Y, y+1)
			}
		}
	}
	return b
}

func TestGlyphAspectRatio(t *testing.T) {
	// paint/icon-glyph.md#图形限框: compare actual artwork, not layout boxes.
	// Include real non-square glyphs with the preset pen and an edited target.
	for _, tc := range []struct {
		name         string
		source       *ui.SVG
		w, h, stroke float32
		fitEdge      bool
	}{
		{"outline-wide", aspectOutline, 26, 12, 2, true},
		{"outline-tall", aspectOutline, 11, 26, 1, true},
		{"filled-wide", aspectFill, 26, 10, 0, true},
		{"filled-tall", aspectFill, 10, 26, 0, true},
		{"latest-fixed-pen", aspectDown, 20, 10, 1.75, false},
		{"menu-edited-target", aspectMenu, 4, 24, 1.75, false},
		{"menu-large-pen", aspectMenu, 24, 24, 4, false},
		{"menu-thin-pen", aspectMenu, 24, 24, 1.4, false},
	} {
		for _, painter := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/painter=%t", tc.name, painter), func(t *testing.T) {
				canvas := float32(32)
				if tc.name == "menu-large-pen" || tc.name == "menu-thin-pen" {
					canvas = 24
				}
				imageOf := func(fitted bool) *image.RGBA {
					tt := ui.NewTester(func(c *ui.Context) {
						theme.Apply(c, aspectTheme)
						e := ui.Box(c).Absolute().Left(20).Top(20).Size(canvas, canvas).TextColor(ui.RGB(255, 255, 255))
						if !fitted {
							e.Children(func() { ui.Icon(c, tc.source).Size(24, 24).Attach(ui.AnchorCenter, ui.AnchorCenter) })
							return
						}
						g := Fit(c, tc.source, canvas, canvas, tc.w, tc.h, tc.stroke)
						if painter {
							e.Draw(func(p *ui.Painter, r ui.Rect) { Icon(p, r, g, canvas, ui.RGB(255, 255, 255)) })
						} else {
							e.Children(func() { g.Element(c).Label("glyph canvas") })
						}
					}, 80, 80)
					tt.SetScale(16)
					img := tt.Image()
					if fitted && !painter {
						bounds, found := tt.Find("glyph canvas")
						if !found || bounds.W != canvas || bounds.H != canvas {
							t.Errorf("layout canvas %v (found %t), want %.0fx%.0f DIP", bounds, found, canvas, canvas)
						}
					}
					return img
				}
				original, actual := aspectBounds(imageOf(false)), aspectBounds(imageOf(true))
				want, got := float64(original.Dx())/float64(original.Dy()), float64(actual.Dx())/float64(actual.Dy())
				if math.Abs(got/want-1) > .02 {
					t.Errorf("rendered ratio %.6f, original %.6f (bounds %v -> %v)", got, want, original, actual)
				}
				if tc.fitEdge {
					// When the output pen already preserves the source ink ratio,
					// the largest contained glyph reaches one target edge.
					w, h := float64(actual.Dx())/16, float64(actual.Dy())/16
					if w > float64(tc.w)+1 || h > float64(tc.h)+1 {
						t.Errorf("ink %.3fx%.3f exceeds target %.3fx%.3f DIP", w, h, tc.w, tc.h)
					}
					if math.Min(math.Abs(w-float64(tc.w)), math.Abs(h-float64(tc.h))) > 1 {
						t.Errorf("ink %.3fx%.3f leaves both target edges %.3fx%.3f DIP unused", w, h, tc.w, tc.h)
					}
				}
			})
		}
	}
}

func TestHostGlyphAspectRatio(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, aspectTheme)
		HostGlyph(c, aspectHost, 36, 36, 26, 23).TextColor(ui.RGB(255, 255, 255))
	}, 40, 40)
	tt.SetScale(16)
	b := aspectBounds(tt.Image())
	ratio := float64(b.Dx()) / float64(b.Dy())
	if math.Abs(ratio-1) > .02 {
		t.Errorf("host circle rendered ratio %.6f, want 1 (bounds %v)", ratio, b)
	}
}
