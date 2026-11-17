package gallery

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
	"testing"
)

// paint/message-bottom.md#图片与状态矩阵: all requested gallery states
// have measured height contracts, including the invalid reactions-only shell.
func TestMessageBottomStateHeights(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Message()
	rm := th.Metrics().Reaction()
	for _, side := range []chat.Side{chat.Incoming, chat.Outgoing} {
		for _, kind := range []string{"inline", "edited", "image", "caption", "only-reactions", "text-tail"} {
			t.Run(fmt.Sprintf("%d/%s", side, kind), func(t *testing.T) {
				var line, photoHeight float32
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c, th)
					_, line = c.MeasureText(0, ui.Span{Text: "M", Size: th.Typography().Message().Size()})
					info := chat.Info{Label: "18:42"}
					if side == chat.Outgoing {
						info.Delivery = chat.Read
					}
					if kind == "edited" {
						info.Prefix = "edited"
					}
					msg := chat.Message{Text: "Hello", Info: info}
					o := chat.BubbleOptions{Side: side, SuppressTail: true}
					o.Footer = func(tail ui.Span) {
						content.Reactions(c, []content.Reaction{{ID: "a", Emoji: "+", Count: 1}}, content.ReactionOptions{InBubble: kind != "image", Outside: kind == "image", Trailing: tail})
					}
					var leading func()
					if kind == "image" || kind == "caption" {
						leading = func() {
							content.Photo(c, content.Media{ID: "photo", Label: "photo", Width: 200, Height: 120}, content.MediaOptions{Width: 200})
						}
						photoHeight = 120
					}
					if kind == "image" {
						msg.Text = ""
						o.MediaBottom = true
					}
					if kind == "text-tail" {
						o.Footer = nil
					}
					if kind == "only-reactions" {
						msg.Text = ""
						msg.Info = chat.Info{}
					}
					ui.Column(c).Padding(20).Children(func() { chat.Bubble(c, msg, o, leading).Label("bubble") })
				}, 600, 400)
				want := m.PaddingTop() + m.PaddingBottom() + line + th.Metrics().Preview().MediaGap() + rm.PaddingTop() + rm.IconSize() + rm.PaddingBottom()
				switch kind {
				case "image":
					want = m.PaddingTop() + m.PaddingBottom() + photoHeight + th.Metrics().Preview().MediaGap() + rm.PaddingTop() + rm.IconSize() + rm.PaddingBottom()
				case "caption":
					want += photoHeight
				case "text-tail":
					want = m.PaddingTop() + m.PaddingBottom() + line
				case "only-reactions":
					want = 0
				}
				actual := bottomBox(t, tt, "bubble").H
				bottomAssert(t, "requested message state height follows specification", bottomNear(actual, want))
				t.Logf("side=%d kind=%s height=%.6f", side, kind, actual)
			})
		}
	}
}
