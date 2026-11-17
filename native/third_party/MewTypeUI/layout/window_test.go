package layout_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func near(t *testing.T, id string, got, want float32) {
	t.Helper()
	if math.Abs(float64(got-want)) > 1 {
		t.Errorf("%s: got %.3f, want %.3f DIP", id, got, want)
	}
}
func truth(t *testing.T, id string, got, want bool) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", id, got, want)
	}
}
func rect(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, _ := tt.Find(label) // Hidden regions are absent from the committed tree.
	return r
}
func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func shell(t *testing.T, s *layout.State, o layout.Options, width int, source *theme.Theme) *ui.Tester {
	t.Helper()
	return ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		layout.Window(c, s, o, func() {
			layout.Pane(c, s, layout.Title, func() { ui.Text(c, "Window title").Label("Title text") }).Label("Title")
			layout.Pane(c, s, layout.Folders, func() { ui.Text(c, "Folders") }).Label("Rail")
			layout.Pane(c, s, layout.FolderTabs, func() { ui.Text(c, "Filters") }).Label("Tabs")
			layout.Pane(c, s, layout.Conversations, func() {
				ui.Button(c, "Open chat").OnClick(func() { s.Active = layout.Chat })
			}).Label("List")
			layout.Pane(c, s, layout.Chat, func() {
				ui.Button(c, "Open details").OnClick(func() { s.ThirdOpen = true })
			}).Label("Chat")
			layout.Pane(c, s, layout.Third, func() {
				ui.Button(c, "Close details").OnClick(func() { s.ThirdOpen = false })
			}).Label("Third")
		})
	}, width, int(source.Metrics().Layout().MinHeight()))
}

// window.style:13-38,229; window_session_controller.cpp:2772-2801,3445.
func TestWindowBreakpoints(t *testing.T) {
	tokens := theme.Light().Metrics()
	m := tokens.Layout()
	rail := tokens.Sidebar().Width()
	minimum, two := m.MinWidth(), m.ListMinWidth()+m.ChatMinWidth()
	three := two + m.ThirdMinWidth()
	defaultBody := m.DefaultWidth() - rail
	beforeThirdList := float32(math.Round(float64((three - 1) * m.ListRatio())))
	cases := []struct {
		id                      string
		width                   int
		folders                 bool
		mode                    layout.Mode
		rail, list, chat, third float32
	}{
		{"minimum", int(minimum), true, layout.Single, 0, minimum, 0, 0},
		{"before-rail", int(minimum + rail - 1), true, layout.Single, 0, minimum + rail - 1, 0, 0},
		{"rail", int(minimum + rail), true, layout.Single, rail, minimum, 0, 0},
		{"before-double", int(two + rail - 1), true, layout.Single, rail, two - 1, 0, 0},
		{"double", int(two + rail), true, layout.Double, rail, m.ListMinWidth(), m.ChatMinWidth(), 0},
		{"default", int(m.DefaultWidth()), true, layout.Double, rail, m.ListMinWidth(), defaultBody - m.ListMinWidth(), 0},
		{"before-triple", int(three + rail - 1), true, layout.Double, rail, beforeThirdList, three - 1 - beforeThirdList, 0},
		{"triple", int(three + rail), true, layout.Triple, rail, m.ListMinWidth(), m.ChatMinWidth(), m.ThirdMinWidth()},
		{"no-rail-single", int(two - 1), false, layout.Single, 0, two - 1, 0, 0},
		{"no-rail-double", int(two), false, layout.Double, 0, m.ListMinWidth(), m.ChatMinWidth(), 0},
		{"no-rail-triple", int(three), false, layout.Triple, 0, m.ListMinWidth(), m.ChatMinWidth(), m.ThirdMinWidth()},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			s := layout.State{ThirdOpen: tc.mode == layout.Triple}
			tt := shell(t, &s, layout.Options{Folders: tc.folders}, tc.width, theme.Light())
			truth(t, "mode", s.Info().Mode == tc.mode, true)
			near(t, "folder-width", rect(t, tt, "Rail").W, tc.rail)
			near(t, "list-width", rect(t, tt, "List").W, tc.list)
			near(t, "chat-width", rect(t, tt, "Chat").W, tc.chat)
			near(t, "third-width", rect(t, tt, "Third").W, tc.third)
			if tc.folders && tc.rail == 0 {
				near(t, "filter-tabs-height", rect(t, tt, "Tabs").H, m.FolderTabsHeight())
				near(t, "list-below-tabs", rect(t, tt, "List").Y, m.FolderTabsHeight())
			}
			truth(t, "idle", tt.FramePending(), false)
		})
	}
}

func TestSingleNavigationAndThirdFallback(t *testing.T) {
	s := layout.State{}
	tt := shell(t, &s, layout.Options{Folders: true}, 380, theme.Light())
	click(t, tt, "Open chat")
	near(t, "single-chat", rect(t, tt, "Chat").W, theme.Light().Metrics().Layout().MinWidth())
	near(t, "single-list-hidden", rect(t, tt, "List").W, 0)
	click(t, tt, "Open details")
	truth(t, "third-in-main", s.Info().ThirdInMain, true)
	near(t, "single-details", rect(t, tt, "Third").W, theme.Light().Metrics().Layout().MinWidth())
	tt.Key(0, ui.KeyEscape)
	truth(t, "escape-closes-details", s.ThirdOpen, false)
	tt.Key(0, ui.KeyEscape)
	truth(t, "escape-returns-list", s.Active == layout.Conversations, true)
	s.ThirdOpen = true
	tt.SetSize(800, 480)
	truth(t, "double-details-in-main", s.Info().ThirdInMain, true)
	near(t, "double-details-width", rect(t, tt, "Third").W, s.Info().BodyWidth-s.Info().ListWidth)
	tt.SetSize(1100, 480)
	tt.Advance(theme.Light().Motion().ThirdPanel().Duration())
	tt.Frame()
	truth(t, "details-return-to-inline", s.Info().Mode == layout.Triple && !s.Info().ThirdInMain, true)
}

// mainwidget.cpp:2908-2925,2968-2973,3230-3237; dialogsSmallColumnWidth:2754.
func TestListDragCollapseAndRestore(t *testing.T) {
	m := theme.Light().Metrics()
	s := layout.State{Active: layout.Chat}
	commits := 0
	tt := shell(t, &s, layout.Options{Folders: true, OnResize: func() { commits++ }}, 1200, theme.Light())
	r := rect(t, tt, "Resize conversations")
	x, y := r.X+m.Layout().ResizeHitWidth()-1, r.Y+r.H/2
	tt.Move(x, y)
	truth(t, "wide-hit-target", tt.Cursor() == ui.CursorResizeEW, true)
	tt.Press(x, y)
	// Incremental moves must retain the unsnapped request across the 260 minimum.
	for requested := s.Info().ListWidth - 10; requested >= 120; requested -= 10 {
		x -= 10
		tt.Move(x, y)
	}
	tt.Release(x, y)
	duration := theme.Light().Motion().ListCollapse().Duration()
	tt.Advance(duration / 2)
	tt.Frame()
	avatarWidth := 2*m.Conversation().PaddingLeft() + m.Conversation().AvatarSize()
	near(t, "collapse-midpoint", s.Info().ListWidth, avatarWidth+(m.Layout().ListMinWidth()-avatarWidth)*(1-float32(math.Sqrt(.75))))
	tt.Advance(duration / 2)
	tt.Frame()
	truth(t, "compact-after-drag", s.Compact, true)
	near(t, "compact-avatar-width", rect(t, tt, "List").W, 2*m.Conversation().PaddingLeft()+m.Conversation().AvatarSize())
	truth(t, "one-drag-commit", commits == 1, true)
	tt.SetSize(711, 480)
	truth(t, "compact-does-not-bypass-single", s.Info().Mode == layout.Single, true)
	tt.SetSize(1200, 480)
	tt.Advance(theme.Light().Motion().ListCollapse().Duration())
	r = rect(t, tt, "Resize conversations")
	x, y = r.X+1, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x+300, y)
	tt.Release(x+300, y)
	truth(t, "expand-from-avatar-drag", s.Compact, false)
	near(t, "restored-list", rect(t, tt, "List").W, avatarWidth+300)
	ratio := s.ListRatio
	tt.SetSize(1600, 480)
	near(t, "saved-proportion", rect(t, tt, "List").W, min(m.Layout().ListMaxWidth(), float32(math.Round(float64((1600-m.Sidebar().Width())*ratio)))))
}

func TestTitleBarAndNestedBounds(t *testing.T) {
	s := layout.State{Active: layout.Chat}
	tt := shell(t, &s, layout.Options{}, 800, theme.Light())
	tt.SetTitleBar(ui.TitleBar{Height: 32, Left: 76, Right: 138})
	near(t, "native-top-inset", rect(t, tt, "Chat").Y, 32)
	near(t, "native-controls-left", rect(t, tt, "Title text").X, 76)
	truth(t, "native-controls-right", rect(t, tt, "Title text").X+rect(t, tt, "Title text").W <= 800-138, true)
	tt.SetTitleBar(ui.TitleBar{})
	near(t, "fullscreen-no-inset", rect(t, tt, "Chat").Y, 0)
	var info layout.Info
	nested := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, theme.Light())
		ui.Column(c).Fill().Children(func() {
			ui.Box(c).Height(54).Shrink(0)
			ui.Box(c).Width(640).Grow(1).Children(func() {
				layout.Window(c, &s, layout.Options{}, func() {
					layout.Pane(c, &s, layout.Chat, func() { ui.Text(c, "Nested") }).Label("Nested pane")
				})
				info = s.Info()
			})
		})
	}, 1600, 600)
	nested.SetTitleBar(ui.TitleBar{Height: 32, Right: 138})
	truth(t, "container-breakpoint", info.Mode == layout.Double, true)
	near(t, "no-duplicate-title-inset", rect(t, nested, "Nested pane").Y, 54)
	near(t, "nested-chat-width", rect(t, nested, "Nested pane").W, theme.Light().Metrics().Layout().ChatMinWidth())
}

func TestThemeGeometryAndDividerKeyboard(t *testing.T) {
	source, err := theme.Compile(theme.Definition{Version: theme.Version, Base: theme.LightBase,
		Metrics: map[string]float32{"layout.listMinWidth": 280, "layout.chatMinWidth": 400, "sidebar.width": 80, "layout.resizeHitWidth": 10, "control.borderWidth": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := layout.State{Active: layout.Chat}
	tt := shell(t, &s, layout.Options{Folders: true}, 760, source)
	near(t, "theme-list-min", rect(t, tt, "List").W, source.Metrics().Layout().ListMinWidth())
	near(t, "theme-chat-min", rect(t, tt, "Chat").W, source.Metrics().Layout().ChatMinWidth())
	near(t, "theme-divider-hit", rect(t, tt, "Resize conversations").W, source.Metrics().Layout().ResizeHitWidth())
	// A roomy window permits keyboard resizing without taking the chat below minimum.
	tt.SetSize(1200, 480)
	before := s.Info().ListWidth
	click(t, tt, "Resize conversations")
	tt.Key(0, ui.KeyRight)
	near(t, "keyboard-resize", s.Info().ListWidth, before+source.Metrics().Conversation().PaddingLeft())
	tt.Key(0, ui.KeyEnd)
	near(t, "keyboard-list-max", s.Info().ListWidth, source.Metrics().Layout().ListMaxWidth())
	for _, width := range []int{760, 1200, 760} {
		tt.SetSize(width, 480)
		truth(t, fmt.Sprintf("minimum-after-resize-%d", width), s.Info().ChatWidth >= source.Metrics().Layout().ChatMinWidth()-1, true)
	}
}
