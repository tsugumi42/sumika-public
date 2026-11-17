package overlay

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestViewerIntrinsicReplacementAndDoubleClick(t *testing.T) {
	state := ViewerState{}
	state.Show("photo")
	items := []ViewerItem{{ID: "photo", Image: fixture(80, 40), IntrinsicWidth: 800, IntrinsicHeight: 400}}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		viewerContent(c, &state, items, ViewerOptions{})
	}, 400, 300)
	// paint/viewer.md#图片矩形: intrinsic dimensions control fit, not the thumbnail.
	check(t, "intrinsic fit geometry", state.Fit && near(rect(t, tt, "Viewer image").H, 200))
	tt.ClickAt(200, 150)
	tt.ClickAt(200, 150)
	check(t, "double click selects actual size", !state.Fit && state.Zoom == 0 && !state.dragging && near(rect(t, tt, "Viewer image").H, 300))
	tt.Press(200, 150)
	tt.Move(240, 170)
	tt.Release(240, 170)
	check(t, "intrinsic image remains pannable", state.Pan == (ui.Point{X: 40, Y: 20}))
	items[0].Image = fixture(800, 400)
	tt.Frame()
	check(t, "original arrival preserves zoom and pan", !state.Fit && state.Pan == (ui.Point{X: 40, Y: 20}))
	tt.Advance(time.Second)
	tt.ClickAt(200, 150)
	tt.ClickAt(200, 150)
	check(t, "double click returns fit and center", state.Fit && state.Pan == (ui.Point{}) && near(rect(t, tt, "Viewer image").H, 200))
}

func TestViewerOpeningClickGuard(t *testing.T) {
	state := ViewerState{}
	state.Show("a")
	items := []ViewerItem{{ID: "a", Image: fixture(100, 100)}, {ID: "b", Image: fixture(100, 100)}}
	closed := 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		viewerContent(c, &state, items, ViewerOptions{OnClose: func() { closed++ }})
	}, 400, 300)
	tt.ClickAt(100, 100)
	click(t, tt, "Close viewer")
	check(t, "opening clicks cannot dismiss", state.Open && closed == 0)
	tt.Advance(499 * time.Millisecond)
	tt.ClickAt(100, 100)
	check(t, "guard holds before boundary", state.Open)
	tt.Key(0, ui.KeyRight)
	tt.Advance(time.Millisecond)
	tt.ClickAt(100, 100)
	check(t, "navigation does not restart guard", !state.Open && closed == 1)
	state.Show("a")
	tt.Frame()
	tt.ClickAt(100, 100)
	check(t, "show restarts guard", state.Open)
	tt.Key(0, ui.KeyEscape)
	check(t, "escape bypasses click guard", !state.Open && closed == 2)
	state.Show("a")
	tt.Frame()
	tt.Advance(500 * time.Millisecond)
	click(t, tt, "Close viewer")
	check(t, "close button works at boundary", !state.Open && closed == 3)
}
