package chat

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestAvatarColumnAndBubbleKeepIndependentMargins(t *testing.T) {
	source := theme.Light()
	m := source.Metrics().Message()
	value := strings.Repeat("A prepared group message. ", 12)
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, source)
		Row(c, Message{Text: value, SenderName: "Aoi"}, RowOptions{ReserveAvatar: true}, nil)
	}, 380, 500)
	body := box(tt, value)
	// message.cpp:6559-6565: the avatar is at 14, but bubble left is 16+40.
	verify(t, "avatar column preserves the narrow bubble width budget", near(body.W, 380-m.MarginLeft()-m.MarginRight()-m.PaddingLeft()-m.PaddingRight()))
}
