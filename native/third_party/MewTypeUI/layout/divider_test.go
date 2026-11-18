package layout_test

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// historyResizeWidth=6 (chat.style:203) over a lineWidth=1 (basic.style:62).
// Verifies the painted surface, not just the hit box's declared dimensions.
func TestDividerPaintAndKeyboard(t *testing.T) {
	source := theme.Light()
	m := source.Metrics()
	width := m.Layout().ListMinWidth()
	commits := 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		ui.Row(c).Fill().Background(source.Colors().Window().Background()).Children(func() {
			ui.Box(c).Width(width).FillHeight().Shrink(0)
			layout.Divider(c, width, func(v float32) { width = v }, layout.DividerOptions{
				Label: "Divider", Min: m.Layout().ListMinWidth(), Max: m.Layout().ListMaxWidth(), OnCommit: func() { commits++ },
			})
			ui.Spacer(c)
		})
	}, 800, 100)
	r := rect(t, tt, "Divider")
	near(t, "divider-hit-width", r.W, m.Layout().ResizeHitWidth())
	img := tt.Image()
	linePixel := img.RGBAAt(int(r.X), 50)
	clearPixel := img.RGBAAt(int(r.X+m.Control().BorderWidth()), 50)
	truth(t, "line-is-painted", linePixel != clearPixel, true)
	bg := source.Colors().Window().Background()
	truth(t, "hit-area-is-transparent", clearPixel.R == bg.R && clearPixel.G == bg.G && clearPixel.B == bg.B, true)
	tt.Move(r.X+r.W-1, 50)
	focus := source.Colors().Control().Focus()
	hoverPixel := tt.Image().RGBAAt(int(r.X), 50)
	truth(t, "hover-color", hoverPixel.R == focus.R && hoverPixel.G == focus.G && hoverPixel.B == focus.B, true)
	click(t, tt, "Divider")
	tt.Key(0, ui.KeyEnd)
	near(t, "end-key-max", width, m.Layout().ListMaxWidth())
	tt.Key(0, ui.KeyLeft)
	near(t, "left-key-step", width, m.Layout().ListMaxWidth()-m.Conversation().PaddingLeft())
	tt.Key(0, ui.KeyHome)
	near(t, "home-key-min", width, m.Layout().ListMinWidth())
	truth(t, "keyboard-commits", commits >= 3, true)
}

// mainwidget.cpp:2908-2911 compares strictly below columnMinimalWidthLeft/2.
func TestExactCollapseThresholdAndForcedWide(t *testing.T) {
	m := theme.Light().Metrics()
	s := layout.State{Active: layout.Chat}
	force := false
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		layout.Window(c, &s, layout.Options{ForceWideList: force}, func() {
			layout.Pane(c, &s, layout.Conversations, func() { ui.Text(c, "Conversation") }).Label("List")
			layout.Pane(c, &s, layout.Chat, func() { ui.Text(c, "Chat") })
		})
	}, 1100, 480)
	r := rect(t, tt, "Resize conversations")
	x, y := r.X+1, r.Y+r.H/2
	threshold := m.Layout().ListMinWidth() / 2
	tt.Press(x, y)
	x -= s.Info().ListWidth - threshold
	tt.Move(x, y)
	truth(t, "threshold-stays-expanded", s.Compact, false)
	tt.Move(x-1, y)
	truth(t, "below-threshold-collapses", s.Compact, true)
	tt.Release(x-1, y)
	tt.Advance(theme.Light().Motion().ListCollapse().Duration())
	tt.Frame()
	force = true
	tt.Frame()
	near(t, "forced-wide-minimum", rect(t, tt, "List").W, m.Layout().ListMinWidth())
	truth(t, "forced-wide-keeps-preference", s.Compact, true)
	force = false
	tt.Frame()
	tt.Advance(theme.Light().Motion().ListCollapse().Duration())
	tt.Frame()
	near(t, "compact-restored-after-search", rect(t, tt, "List").W, m.Conversation().PaddingLeft()*2+m.Conversation().AvatarSize())
}
