package layout_test

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

// window_slide_animation.cpp:114-151,196-211: cached layers, underlay shift,
// scrim, forward/backward traversal and 240 ms deadline.
func TestCachedPageNavigation(t *testing.T) {
	source := theme.Light()
	s := layout.State{}
	oldColor := ui.RGB(220, 80, 40)
	newColor := ui.RGB(30, 120, 220)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		layout.Window(c, &s, layout.Options{}, func() {
			layout.Pane(c, &s, layout.Conversations, func() {
				ui.Box(c).Fill().Background(oldColor)
				ui.Button(c, "Navigate").Absolute().OnClick(func() { s.Active = layout.Chat })
			})
			layout.Pane(c, &s, layout.Chat, func() { ui.Box(c).Fill().Background(newColor) })
		})
	}, 380, 480)
	tt.Image()
	click(t, tt, "Navigate")
	oldColor = ui.RGB(0, 255, 0)
	tt.Frame()
	start := tt.Image().RGBAAt(180, 240)
	truth(t, "departed pane retains its captured pixels", start.R > 180 && start.G < 100, true)
	tt.Advance(source.Motion().ThirdPanel().Duration() / 2)
	tt.Frame()
	image := tt.Image()
	incoming := image.RGBAAt(300, 240)
	truth(t, "arriving cached page covers its final side", incoming.B > 180 && incoming.R < 50, true)
	dimmed := image.RGBAAt(10, 240)
	truth(t, "departing image receives upstream scrim", dimmed.R < 220 && dimmed.R > 100, true)
	tt.Advance(source.Motion().ThirdPanel().Duration() / 2)
	tt.Frame()
	truth(t, "navigation deadline stops frames", tt.FramePending(), false)
	s.Active = layout.Conversations
	tt.Frame()
	tt.Advance(source.Motion().ThirdPanel().Duration())
	tt.Frame()
	final := tt.Image().RGBAAt(180, 240)
	truth(t, "backward navigation reaches live destination", final.G > 240 && final.R < 20, true)
	tt.SetPreferences(ui.Preferences{TextScale: 1, ReduceMotion: true})
	s.Active = layout.Chat
	tt.Frame()
	truth(t, "reduced motion bypasses navigation", tt.FramePending(), false)
}

// window_session_controller.cpp:2876-2967: requested/actual width and ratios.
func TestThirdWindowHostSizing(t *testing.T) {
	for _, width := range []int{380, 800, 1100} {
		for _, denied := range []bool{false, true} {
			s := layout.State{Active: layout.Chat}
			actual := float32(width)
			calls := 0
			host := &layout.WindowHost{Fixed: func() bool { return false }, CanExtendWithoutMove: func(float32) bool { return !denied }, ExtendWidth: func(delta float32) float32 {
				calls++
				if denied {
					return 0
				}
				actual += delta
				return delta
			}}
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, theme.Light())
				layout.Window(c, &s, layout.Options{Host: host}, func() {
					layout.Pane(c, &s, layout.Chat, func() { ui.Button(c, "Open").OnClick(func() { s.SetThird(c, true, host) }) })
					layout.Pane(c, &s, layout.Third, func() { ui.Button(c, "Close").OnClick(func() { s.SetThird(c, false, host) }) })
				})
			}, width, 480)
			click(t, tt, "Open")
			expected := float32(width)
			if !denied {
				expected += theme.Light().Metrics().Layout().ThirdMinWidth()
				if width < 640 {
					expected += 932 - float32(width)
				}
			}
			near(t, "upstream preferred extension", actual, expected)
			tt.SetSize(int(actual), 480)
			truth(t, "denial retains in-window details", s.Info().ThirdInMain, denied && width < 932)
			if !denied {
				near(t, "list ratio retains original absolute width", s.ListRatio*actual, theme.Light().Metrics().Layout().ListRatio()*float32(width))
			}
			click(t, tt, "Close")
			near(t, "closing returns actual extension", actual, float32(width))
			truth(t, "event callback does not resize on rebuild", calls <= 2, true)
		}
	}
}
