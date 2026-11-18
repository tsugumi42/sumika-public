package gallery

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "content/reaction-size", Area: "content", Title: "Reaction image sizing", View: reactionSizePage})
}

// The host prepares full artwork and an explicitly padded center frame once.
// Existing Twemoji wave artwork is CC-BY-4.0; see THIRD_PARTY_NOTICES.md.
func reactionSizePage(c *ui.Context) {
	t := theme.T(c)
	m := t.Metrics().Message()
	images := ui.Local(c.Root(), "reaction-size-art", func() [2]*ui.Bitmap {
		plain := ui.NewBitmap(ui.Render(func(c *ui.Context) {
			c.Root().Background(ui.Transparent)
			ui.Image(c, emojiWave).Size(128, 128).Fit(ui.Contain)
		}, 128, 128, 1))
		center := ui.NewBitmap(ui.Render(func(c *ui.Context) {
			c.Root().Background(ui.Transparent)
			ui.Image(c, emojiWave).Absolute().Left(32).Top(32).Size(64, 64).Fit(ui.Contain)
		}, 128, 128, 1))
		return [2]*ui.Bitmap{plain, center}
	})
	s := ui.Local(c.Root(), "reaction-size-own", func() bool { return false })
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Padding(m.PaddingTop(), m.PaddingLeft()).Gap(m.PaddingTop()).AlignItems(ui.Start).Children(func() {
			chat.Bubble(c.Key("sample"), chat.Message{Text: "三小时后又是一条好汉", Info: chat.Info{Label: "12:00"}}, chat.BubbleOptions{Footer: func(tail ui.Span) {
				content.Reactions(c, []content.Reaction{{ID: "wave", Emoji: "👋", Image: images[0], Count: 1, Own: *s}}, content.ReactionOptions{InBubble: true, Trailing: tail, OnActivate: func(string) { *s = !*s }}).Label("Sizing reference reaction")
			}}, nil)
			for _, state := range []struct {
				name       string
				appearance content.Appearance
				outside    bool
			}{
				{name: "普通"}, {name: "出站", appearance: content.Appearance{Outgoing: true}},
				{name: "选中", appearance: content.Appearance{Selected: true}},
				{name: "禁用", appearance: content.Appearance{Disabled: true}},
				{name: "气泡外", outside: true},
			} {
				ui.Text(c, state.name)
				content.Reactions(c.Key(state.name), []content.Reaction{
					{ID: "static", Emoji: "👋", Image: images[0], Count: 1},
					{ID: "center", Emoji: "Center", Image: images[1], ImageKind: content.ReactionCenterFrame, Count: 12, Own: true},
					{ID: "custom", Emoji: "Custom", Image: images[0], ImageKind: content.ReactionCustomEmoji, CountLabel: "1.2K"},
					{ID: "unicode", Emoji: "👍", Count: 1},
					{ID: "zero", Emoji: "👋", Image: images[0]},
					{ID: "avatar", Emoji: "👋", Image: images[0], Userpics: []content.ReactionUserpic{{ID: "a", Image: contentSticker()}}},
				}, content.ReactionOptions{Appearance: state.appearance, Outside: state.outside, OnActivate: func(string) { *s = !*s }}).MaxWidth(m.MaxWidth())
			}
		})
	})
}
