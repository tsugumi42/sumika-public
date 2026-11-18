package overlay

import (
	"fmt"
	"image"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// The viewer cases from gallery/centering_test.go exercise the native window's
// renderer here: the public Viewer entry point requires an actual owner window.
func TestViewerRadialPixelCentersAcrossRotation(t *testing.T) {
	center := func(img *image.RGBA) (x, y float64) {
		t.Helper()
		var weight float64
		for py := range img.Bounds().Dy() {
			for px := range img.Bounds().Dx() {
				w := float64(img.RGBAAt(px, py).R) / 255
				weight += w
				x += (float64(px) + .5) * w
				y += (float64(py) + .5) * w
			}
		}
		if weight == 0 {
			t.Fatal("no foreground pixels")
		}
		return x / weight, y / weight
	}
	for _, scale := range []float32{1, 1.25, 1.5} {
		t.Run(fmt.Sprintf("viewer/%.2f", scale), func(t *testing.T) {
			makeFrame := func(key string, phase int) *image.RGBA {
				d := theme.Light().Definition()
				for key := range d.Colors {
					d.Colors[key] = "#00000000"
				}
				d.Colors["window.background"] = "#000000"
				d.Colors[key] = "#ffffff"
				d.Motion["viewerLoadingRotation"] = theme.MotionDefinition{Duration: new(float64(8)), Easing: new(theme.Linear)}
				d.Motion["viewerLoading"] = theme.MotionDefinition{Duration: new(float64(0)), Easing: new(theme.Linear)}
				th, err := theme.Compile(d)
				if err != nil {
					t.Fatal(err)
				}
				state := ViewerState{Open: true, ID: "p"}
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c, th)
					ui.Box(c).Absolute().Left(13.3).Top(17.3).Size(280, 120).Children(func() {
						viewerContent(c, &state, []ViewerItem{{ID: "p", Phase: ViewerLoading, Progress: 1}}, ViewerOptions{})
					})
				}, 320, 160)
				tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
				tt.SetScale(scale)
				tt.Frame()
				tt.SetPreferences(ui.Preferences{TextScale: 1})
				tt.Advance(time.Duration(phase) * 2 * time.Millisecond)
				tt.Frame()
				return tt.Image()
			}
			for phase := range 4 {
				ox, oy := center(makeFrame("control.radialBackground", phase))
				ix, iy := center(makeFrame("control.radialForeground", phase))
				// basic.style:122-123; retain the branch's 0.3 physical px limit.
				check(t, "radial x", math.Abs(ix-ox) <= .3)
				check(t, "radial y", math.Abs(iy-oy) <= .3)
			}
		})
	}
}
