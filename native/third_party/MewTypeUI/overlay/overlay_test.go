package overlay

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func check(t *testing.T, id string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", id)
	}
}
func near(a, b float32) bool { return abs(a-b) <= 1 }
func rect(t *testing.T, tester *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tester.Find(label)
	if !ok {
		t.Fatalf("missing %q; visible=%v", label, tester.Texts())
	}
	return r
}
func click(t *testing.T, tester *ui.Tester, label string) {
	t.Helper()
	if err := tester.Click(label); err != nil {
		t.Fatal(err)
	}
}
func fixture(w, h int) *ui.Bitmap {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			im.SetRGBA(x, y, color.RGBA{R: 180, G: 60, B: 80, A: 255})
		}
	}
	return ui.NewBitmap(im)
}

func zoomFixture() *ui.Bitmap {
	im := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := range 400 {
		for x := range 800 {
			ink := color.RGBA{R: 180, G: 60, B: 80, A: 255}
			if (x/100+y/100)%2 != 0 {
				ink = color.RGBA{R: 20, G: 140, B: 200, A: 255}
			}
			im.SetRGBA(x, y, ink)
		}
	}
	return ui.NewBitmap(im)
}
func inkAt(test *ui.Tester, x, y float32) color.RGBA { return test.Image().RGBAAt(int(x), int(y)) }

func TestBoxLayoutFocusActionsAndScroll(t *testing.T) {
	state := BoxState{}
	chosen := ""
	locked := false
	tester := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Button(c, "Host").OnClick(func() { state.Open = true })
		Box(c, &state, "Question", BoxOptions{Locked: locked, CloseButton: true, Actions: []BoxAction{{ID: "accept", Label: "Accept", Default: true}, {ID: "cancel", Label: "Cancel"}}, OnAction: func(id string) { chosen = id }}, func() { ui.Text(c, "Body").Label("Dialog body") })
	}, 640, 480)
	click(t, tester, "Host")
	tester.Advance(theme.Light().Motion().Dialog().Duration())
	tester.Frame()
	panel, body, accept, cancel := rect(t, tester, "Question"), rect(t, tester, "Dialog body"), rect(t, tester, "Accept"), rect(t, tester, "Cancel")
	m := theme.Light().Metrics().Dialog()
	// layers.style:38, 81-82, 117-128.
	check(t, "box source width", near(panel.W, m.Width()))
	check(t, "box body padding", near(body.X-panel.X, m.PaddingLeft()) && near(body.Y-panel.Y, m.TitleHeight()+m.PaddingTop()))
	check(t, "box source button height", near(accept.H, m.ButtonHeight()))
	check(t, "box action ordering and gap", accept.X > cancel.X && near(accept.X-cancel.X-cancel.W, m.ButtonGap()))
	check(t, "box action right inset", near(panel.X+panel.W-accept.X-accept.W, m.ButtonRight()))
	check(t, "box rounded source corner", inkAt(tester, panel.X+1, panel.Y+1) != inkAt(tester, panel.X+m.Radius()+1, panel.Y+1))
	// layerBg belongs behind boxBg (colors.palette:129,135).
	check(t, "dialog stays above scrim", inkAt(tester, panel.X+m.Radius()+2, panel.Y+m.Radius()+2) == color.RGBA{R: 255, G: 255, B: 255, A: 255})
	for range 8 {
		tester.Key(0, ui.KeyTab)
	}
	check(t, "modal traps keyboard focus", !tester.Focused("Host"))
	tester.Key(0, ui.KeyEnter)
	check(t, "default action commits once", chosen == "accept" && !state.Open)
	tester.Advance(theme.Light().Motion().Dialog().Duration())
	tester.Frame()
	check(t, "box restores host focus", tester.Focused("Host"))
	locked = true
	click(t, tester, "Host")
	tester.Advance(theme.Light().Motion().Dialog().Duration())
	tester.Key(0, ui.KeyEscape)
	tester.ClickAt(0, 0)
	check(t, "locked dialog resists dismissal", state.Open)
	click(t, tester, "Close")
	check(t, "explicit close remains available", !state.Open)
}
func TestBoxLongBodyAndTheme(t *testing.T) {
	state := BoxState{Open: true}
	custom, err := theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase, Metrics: map[string]float32{"dialog.width": 350, "dialog.radius": 0}, Colors: map[string]string{"dialog.background": "#123456"}})
	if err != nil {
		t.Fatal(err)
	}
	tester := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, custom)
		Box(c, &state, "Long", BoxOptions{Actions: []BoxAction{{ID: "done", Label: "Done"}}}, func() {
			for i := range 40 {
				ui.Text(c.Key(i), "Line")
			}
		})
	}, 500, 300)
	tester.Advance(custom.Motion().Dialog().Duration())
	tester.Frame()
	panel, done := rect(t, tester, "Long"), rect(t, tester, "Done")
	check(t, "box respects edited width", near(panel.W, 350))
	check(t, "long body preserves button visibility", done.Y+done.H <= 300-custom.Metrics().Dialog().Margin()+1)
	check(t, "zero radius reaches corner in rendered surface", inkAt(tester, panel.X+1, panel.Y+1) == color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 255})
}
func TestToastGeometryExpiryAndRestart(t *testing.T) {
	state := ToastState{}
	state.Show()
	var tw, line float32
	actionCount := 0
	tester := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		tw, line = measure(c, "Copied", theme.T(c).Typography().Body(), 0)
		Toast(c, &state, "Copied", ToastOptions{})
	}, 640, 480)
	tr := theme.Light()
	tester.Advance(tr.Motion().ToastSlide().Duration())
	tester.Frame()
	r := rect(t, tester, "Copied")
	m := tr.Metrics().Toast()
	// widgets.style:1553-1559; toast_widget.cpp:486-491.
	check(t, "toast source padding", near(r.W, tw+m.PaddingLeft()+m.PaddingRight()) && near(r.H, line+m.PaddingTop()+m.PaddingBottom()))
	check(t, "toast bottom margin and centering", near(480-r.Y-r.H, m.Margin()) && near(r.X, (640-r.W)/2))
	check(t, "toast rounded source corner", inkAt(tester, r.X+1, r.Y+1) != inkAt(tester, r.X+m.Radius()+1, r.Y+1))
	tester.Advance(tr.Motion().ToastHold().Duration() - tr.Motion().ToastSlide().Duration() - time.Millisecond)
	check(t, "toast holds until lifetime boundary", state.Open)
	state.Show()
	tester.Frame()
	tester.Advance(time.Millisecond * 2)
	check(t, "repeated show restarts lifetime", state.Open)
	tester.Advance(tr.Motion().ToastHold().Duration())
	tester.Advance(tr.Motion().ToastSlide().Duration())
	tester.Frame()
	check(t, "toast expires and slides out", !state.Open && !tester.HasText("Copied"))
	tester = ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, tr)
		Toast(c, &state, "Removed", ToastOptions{Infinite: true, Action: "Undo", OnAction: func() { actionCount++ }})
	}, 640, 480)
	state.Show()
	tester.Frame()
	tester.Advance(tr.Motion().ToastSlide().Duration())
	tester.Frame()
	click(t, tester, "Undo")
	check(t, "toast action delivered once", actionCount == 1 && !state.Open)
}
func TestViewerIdentityFitZoomDragAndNavigation(t *testing.T) {
	state := ViewerState{}
	state.Show("a")
	items := []ViewerItem{{ID: "a", Title: "Landscape", Information: "Author / time", Image: zoomFixture()}, {ID: "b", Title: "Portrait", Image: fixture(100, 200)}}
	changed := ""
	showThumbnail := true
	tester := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		if showThumbnail {
			ui.Button(c, "Thumbnail")
		}
		c.Key("window-viewer")
		viewerContent(c, &state, items, ViewerOptions{OnChange: func(id string) { changed = id }})
	}, 400, 300)
	r := rect(t, tester, "Viewer image")
	check(t, "large image fits with aspect ratio", near(r.W, 400) && near(r.H, 200) && near(r.Y, 50))
	// media_view.style:266,269; top placement requested by the overlay brief.
	info := rect(t, tester, "Author / time")
	check(t, "viewer information uses sourced offsets", near(info.X, theme.Light().Metrics().Viewer().InfoLeft()) && near(info.Y, theme.Light().Metrics().Viewer().HeaderOffset()))
	showThumbnail = false
	tester.Frame()
	check(t, "viewer survives thumbnail removal", state.Open && tester.HasText("Landscape"))
	tester.Key(ui.Ctrl, ui.KeyEqual)
	// media_view_overlay_widget.cpp:2945-2947 keeps the equal-sized fit and discrete stages separate.
	r = rect(t, tester, "Viewer image")
	check(t, "equal fit scale enters discrete stage", near(r.W, 400) && !state.Fit)
	tester.Key(ui.Ctrl, ui.KeyEqual)
	r = rect(t, tester, "Viewer image")
	// Find reports the viewport-clipped box; the checker proves natural pixel scale.
	check(t, "zoom inserts natural scale after fit", near(r.W, 400) && near(r.H, 300) && inkAt(tester, 75, 150) == (color.RGBA{R: 180, G: 60, B: 80, A: 255}))
	tester.Press(200, 150)
	tester.Move(250, 180)
	tester.Release(250, 180)
	check(t, "drag pans enlarged image", near(state.Pan.X, 50) && near(state.Pan.Y, 30))
	tester.Press(250, 180)
	tester.Move(390, 295)
	tester.Release(390, 295)
	check(t, "drag clamps image edges", state.Pan.X <= 200 && state.Pan.Y <= 50)
	tester.Key(ui.Ctrl, ui.Key0)
	tester.Frame()
	r = rect(t, tester, "Viewer image")
	check(t, "reset returns fit and recenters", near(r.W, 400) && state.Pan == (ui.Point{}))
	tester.Key(0, ui.KeyRight)
	r = rect(t, tester, "Viewer image")
	check(t, "navigation selects ID and keeps small native size", state.ID == "b" && changed == "b" && near(r.W, 100) && near(r.H, 200))
	tester.Key(0, ui.KeyRight)
	check(t, "viewer does not wrap at end", state.ID == "b")
	items[0], items[1] = items[1], items[0]
	tester.Frame()
	check(t, "viewer reorder preserves ID", state.ID == "b" && tester.HasText("Portrait"))
	tester.Key(0, ui.KeyEscape)
	check(t, "escape closes viewer", !state.Open)
}
func TestViewerLoadingFailureArrivalAndControls(t *testing.T) {
	state := ViewerState{}
	state.Show("pending")
	retry := ""
	items := []ViewerItem{{ID: "pending", Title: "Pending", Phase: ViewerLoading, Progress: .5}}
	tester := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		viewerContent(c, &state, items, ViewerOptions{OnRetry: func(id string) { retry = id }})
	}, 400, 300)
	check(t, "viewer exposes loading state", tester.HasText("Loading…"))
	tester.Advance(theme.Light().Motion().ViewerLoading().Duration())
	tester.Frame()
	loading := rect(t, tester, "Loading…")
	// basic.style:122; media_view_overlay_widget.cpp:1309-1313 centers the 50 DIP radial.
	check(t, "loading radial uses sourced size and center", near(loading.W, theme.Light().Metrics().Viewer().LoadingSize()) && near(loading.H, loading.W) && near(loading.X+loading.W/2, 200) && near(loading.Y+loading.H/2, 150))
	items[0].Phase = ViewerFailed
	items[0].Error = "Cannot decode"
	tester.Frame()
	click(t, tester, "Retry")
	check(t, "viewer exposes failure and retry intent", retry == "pending" && tester.HasText("Cannot decode"))
	items[0].Phase = ViewerReady
	items[0].Image = fixture(800, 400)
	tester.Frame()
	r := rect(t, tester, "Viewer image")
	check(t, "late image arrival applies fit", near(r.W, 400) && near(r.H, 200))
	tester.Advance(theme.Light().Motion().ViewerLoading().Duration())
	tester.Frame()
	// radial_animation.cpp:60-66 removes the loader after the 350 ms completion fade.
	check(t, "ready image finishes loading fade", !tester.HasText("Loading…"))
	tester.Advance(theme.Light().Motion().ViewerWait().Duration() + theme.Light().Motion().ViewerHide().Duration())
	tester.Frame()
	p := inkAt(tester, 377, 27)
	tester.Move(200, 100)
	tester.Advance(theme.Light().Motion().ViewerShow().Duration())
	tester.Frame()
	check(t, "pointer activity restores controls", p != inkAt(tester, 377, 27))
	items = nil
	tester.Frame()
	check(t, "removed current ID closes viewer", !state.Open)
}

func TestViewerPinchOverImage(t *testing.T) {
	state := ViewerState{}
	state.Show("photo")
	items := []ViewerItem{{ID: "photo", Title: "Photo", Image: zoomFixture()}}
	tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); viewerContent(c, &state, items, ViewerOptions{}) }, 400, 300)
	tester.Pinch(200, 150, 1+theme.Light().Metrics().Viewer().PinchStep())
	r := rect(t, tester, "Viewer image")
	check(t, "pinch reaches discrete fit boundary", near(r.W, 400) && !state.Fit)
	tester.Pinch(200, 150, 1+theme.Light().Metrics().Viewer().PinchStep())
	r = rect(t, tester, "Viewer image")
	// media_view_overlay_widget.cpp:7663-7681: each 0.25 increment advances one source stage.
	check(t, "pinch over image advances discrete zoom", near(r.W, 400) && near(r.H, 300) && inkAt(tester, 75, 150) == (color.RGBA{R: 180, G: 60, B: 80, A: 255}))
}

func TestViewerFitBeyondDiscreteLimits(t *testing.T) {
	state := ViewerState{}
	state.Show("large")
	items := []ViewerItem{{ID: "large", Title: "Large", Image: fixture(1600, 400)}}
	tester := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); viewerContent(c, &state, items, ViewerOptions{}) }, 100, 100)
	// media_view_overlay_widget.cpp:2962-2968: an out-of-range fit stage remains reachable.
	tester.Key(ui.Ctrl, ui.KeyMinus)
	r := rect(t, tester, "Viewer image")
	check(t, "zoom out cannot enlarge a very large fitted image", near(r.W, 100) && state.Fit)
	tester.Key(ui.Ctrl, ui.Key0)
	for range int(theme.Light().Metrics().Viewer().MaxZoomLevel()) + 1 {
		tester.Key(ui.Ctrl, ui.KeyMinus)
	}
	r = rect(t, tester, "Viewer image")
	check(t, "zoom out reaches fit below ordinary minimum", near(r.W, 100) && state.Fit)
	state.Show("tiny")
	items = []ViewerItem{{ID: "tiny", Title: "Tiny", Image: fixture(1, 1)}}
	tester.Frame()
	for range int(theme.Light().Metrics().Viewer().MaxZoomLevel()) + 1 {
		tester.Key(ui.Ctrl, ui.KeyEqual)
	}
	r = rect(t, tester, "Viewer image")
	check(t, "zoom in reaches fit above ordinary maximum", near(r.W, 100) && state.Fit)
	tester.Key(ui.Ctrl, ui.KeyEqual)
	r = rect(t, tester, "Viewer image")
	check(t, "zoom in cannot shrink a very small fitted image", near(r.W, 100) && state.Fit)
}
