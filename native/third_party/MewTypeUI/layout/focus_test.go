package layout_test

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestFocusFollowsPaneVisibility(t *testing.T) {
	s := layout.State{}
	draft := ""
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		layout.Window(c, &s, layout.Options{}, func() {
			layout.Pane(c, &s, layout.Conversations, func() { ui.TextInputBase(c, &draft).Label("Search draft") })
			layout.Pane(c, &s, layout.Chat, func() { ui.Text(c, "Chat") })
			layout.Pane(c, &s, layout.Third, func() { ui.Text(c, "Details") })
		})
	}, 1100, 480)
	click(t, tt, "Search draft")
	tt.Type("a")
	tt.SetSize(639, 480)
	tt.Type("b")
	truth(t, "visible-editor-keeps-focus-when-narrowing", draft == "ab", true)
	tt.SetSize(1100, 480)
	s.ThirdOpen = true
	tt.Frame()
	tt.Advance(theme.Light().Motion().ThirdPanel().Duration())
	tt.Type("c")
	truth(t, "adjacent-third-does-not-steal-editor", draft == "abc", true)
	s.ThirdOpen = false
	s.Active = layout.Chat
	tt.SetSize(639, 480)
	tt.Key(0, ui.KeyEscape)
	truth(t, "hidden-editor-hands-escape-to-shell", s.Active == layout.Conversations, true)
}

// adaptiveChatWideWidth=880, window/window.style:40.
func TestWideChatThreshold(t *testing.T) {
	m := theme.Light().Metrics().Layout()
	threshold := m.ListMinWidth() + m.WideChatWidth()
	s := layout.State{Active: layout.Chat, ListRatio: m.ListMinWidth() / threshold}
	tt := shell(t, &s, layout.Options{}, int(threshold)-1, theme.Light())
	truth(t, "before-wide-chat", s.Info().WideChat, false)
	tt.SetSize(int(threshold), 480)
	truth(t, "wide-chat-boundary", s.Info().WideChat, true)
	near(t, "wide-chat-width", rect(t, tt, "Chat").W, m.WideChatWidth())
}
