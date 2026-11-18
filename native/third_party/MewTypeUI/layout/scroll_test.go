package layout_test

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/theme"
)

// The same profile must keep its scroll position when it becomes a main section
// (mainwidget.cpp:2031-2067), then returns to the third column.
func TestThirdScrollSurvivesPresentationChange(t *testing.T) {
	s := layout.State{Active: layout.Chat, ThirdOpen: true}
	m := theme.Light().Metrics()
	ids := [...]int{11, 23, 37, 41, 59, 67, 73, 89, 97, 103, 127, 131, 149, 157, 163, 179, 181, 193, 211, 227}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		layout.Window(c, &s, layout.Options{}, func() {
			layout.Pane(c, &s, layout.Conversations, func() { ui.Text(c, "Conversations") })
			layout.Pane(c, &s, layout.Chat, func() { ui.Text(c, "Chat") })
			layout.Pane(c, &s, layout.Third, func() {
				ui.Scroll(c).Fill().Children(func() {
					for _, id := range ids {
						ui.Textf(c.Key(id), "Profile row %d", id).Height(m.Conversation().RowHeight()).Shrink(0)
					}
				})
			}).Label("Profile viewport")
		})
	}, 1100, 480)
	r := rect(t, tt, "Profile viewport")
	tt.Scroll(r.X+r.W/2, r.Y+r.H/2, 0, m.Conversation().RowHeight()*3)
	before := rect(t, tt, "Profile row 67").Y
	tt.SetSize(800, 480)
	near(t, "third-scroll-in-main", rect(t, tt, "Profile row 67").Y, before)
	tt.SetSize(1100, 480)
	tt.Advance(theme.Light().Motion().ThirdPanel().Duration())
	tt.Frame()
	near(t, "third-scroll-back-inline", rect(t, tt, "Profile row 67").Y, before)
}
