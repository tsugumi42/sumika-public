package dialogs_test

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestFolderInkAtDeviceScales(t *testing.T) {
	th := theme.Light()
	s := dialogs.RailState{SelectedID: "other"}
	build := func(c *ui.Context) {
		theme.Apply(c, th)
		dialogs.Rail(c, []dialogs.Folder{{ID: "folder", Name: "Folder"}}, &s, dialogs.RailOptions{Mode: dialogs.IconsOnly})
	}
	tt := ui.NewTester(build, 100, 100)
	for _, scale := range []float32{1, 1.25, 2, 1.25} {
		tt.SetScale(scale)
		img := tt.Image()
		m := th.Metrics().Sidebar()
		// window.style:245; original filled silhouette: interior must be opaque,
		// horizontal body edges must resolve in <=1 physical pixel at 125%.
		x := int(m.Width() / 2 * scale)
		y := int(m.IconsOnlyHeight() / 2 * scale)
		fg := th.Colors().Sidebar().IconForeground()
		p := img.RGBAAt(x, y)
		check(t, "folder solid interior", p.R == fg.R && p.G == fg.G && p.B == fg.B)
		partial := 0
		for yy := int((m.IconsOnlyHeight()/2 + 8) * scale); yy < int((m.IconsOnlyHeight()/2+15)*scale); yy++ {
			p := img.RGBAAt(x, yy)
			bg := th.Materials().Sidebar().Background()
			if p.R != fg.R && p.R != bg.R {
				partial++
			}
		}
		check(t, "folder edge has no upscale blur", partial <= 1)
		fresh := ui.NewTester(build, 100, 100)
		fresh.SetScale(scale)
		check(t, "device scale cache matches fresh raster", bytes.Equal(img.Pix, fresh.Image().Pix))
	}
}

func TestFolderPagesClipAndTiming(t *testing.T) {
	selected := "a"
	th := theme.Light()
	clicks := 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		dialogs.FolderPages(c, []string{"a", "b", "c"}, selected, func(id string) {
			ui.ButtonBase(c).Fill().Label("page-" + id).Background(ui.RGB(220, 80, 40)).OnClick(func() { clicks++ })
		}).Label("pages")
	}, 400, 120)
	duration := th.Motion().Page().Duration()
	// basic.style:96-97, window_slide_animation.h:43-44, cpp:115-151.
	for _, target := range []string{"b", "a"} {
		selected = target
		tt.Frame()
		for step := 1; step <= 24; step++ {
			tt.Advance(duration / 24)
			tt.Frame()
			p := float32(math.Sqrt(1 - math.Pow(1-float64(step)/24, 2)))
			a, aOK := tt.Find("page-a")
			b, bOK := tt.Find("page-b")
			if step < 24 {
				check(t, "folder animation runs until duration", tt.FramePending())
				boundary := 400 * (1 - p)
				if target == "a" {
					boundary = 400 * p
				}
				left, right := a, b
				check(t, "folder visible clips partition viewport", !aOK || !bOK || left.X+left.W <= right.X+1)
				if bOK {
					check(t, "folder ease out circular boundary", near(b.X, boundary))
				}
			}
		}
		tt.Advance(time.Millisecond)
		tt.Frame()
		check(t, "folder motion stops", !tt.FramePending())
		check(t, "folder target fills viewport", near(found(tt, "page-"+target).W, 400))
	}
	// Input stays disabled during transition; rapid reversal stays clipped.
	selected = "c"
	tt.Frame()
	tt.ClickAt(350, 60)
	check(t, "moving folder blocks input", clicks == 0)
	tt.Advance(duration / 3)
	selected = "a"
	tt.Frame()
	tt.SetSize(260, 120)
	tt.Advance(duration + time.Millisecond)
	check(t, "interrupted resized folder fills target", near(found(tt, "page-a").W, 260))
	tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
	selected = "b"
	tt.Frame()
	check(t, "reduced motion skips folder transition", !tt.FramePending() && near(found(tt, "page-b").W, 260))
}
