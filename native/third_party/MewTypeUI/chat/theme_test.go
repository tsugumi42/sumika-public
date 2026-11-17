package chat

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

func TestChatThemeOverridesAndIncomingSelection(t *testing.T) {
	d := theme.Definition{Version: theme.Version, Base: theme.LightBase, Metrics: map[string]float32{"message.maxWidth": 300, "message.radiusLarge": 0, "message.paddingLeft": 20}, Colors: map[string]string{"message.in.backgroundSelected": "#eebe99", "message.out.background": "#aaddcc"}}
	source, err := theme.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		Bubble(c, Message{Text: "Theme geometry and selection roles remain independently editable."}, BubbleOptions{Selected: true}, nil).Label("incoming")
	}, 500, 250)
	r := box(tt, "incoming")
	verify(t, "incoming selection uses its palette role", pixel(tt, r.X+r.W/2, r.Y+2) == rgba(source.Colors().Message().In().BackgroundSelected()))
	verify(t, "zero radius keeps square corner", pixel(tt, r.X, r.Y) == rgba(source.Colors().Message().In().BackgroundSelected()))
	verify(t, "custom maximum width reaches rendered shell", near(r.W, source.Metrics().Message().MaxWidth()))
}
