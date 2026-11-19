package gallery

import (
	"image"
	"image/color"
	"image/draw"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestComparisonPixelStates(t *testing.T) {
	a := NewApp()
	reference := image.NewRGBA(image.Rect(0, 0, 10, 10))
	draw.Draw(reference, reference.Bounds(), image.NewUniform(color.RGBA{100, 120, 140, 255}), image.Point{}, draw.Src)
	a.SetReference(ui.NewBitmap(reference))
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Fill().Background(ui.RGB(20, 40, 60)).Effect(a.Comparison.prepare(a.Reference))
	}, 20, 10)
	cases := []struct {
		name, mode string
		x          float64
		replace    bool
		want       color.RGBA
	}{
		{"alpha", "alpha", 0, false, color.RGBA{60, 80, 100, 255}},
		{"difference", "difference", 0, false, color.RGBA{80, 80, 80, 255}},
		{"outside", "difference", 10, false, color.RGBA{20, 40, 60, 255}},
		{"replacement", "alpha", 0, true, color.RGBA{100, 120, 140, 255}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			a.Comparison.Mode, a.Comparison.X = test.mode, test.x
			if test.replace {
				replacement := image.NewRGBA(image.Rect(0, 0, 10, 10))
				draw.Draw(replacement, replacement.Bounds(), image.NewUniform(color.RGBA{180, 200, 220, 255}), image.Point{}, draw.Src)
				a.SetReference(ui.NewBitmap(replacement))
			}
			tt.Frame()
			got := tt.Image().RGBAAt(2, 2)
			if got != test.want {
				t.Fatalf("pixel: got %v, want %v", got, test.want)
			}
		})
	}
}
