package gallery

import (
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
	"math"
	"testing"
)

func bottomAssert(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", name)
	}
}
func bottomNear(a, b float32) bool { return math.Abs(float64(a-b)) < 0.02 }
func bottomBox(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("missing %s", label)
	}
	return r
}

// paint/message-bottom.md#有回应时最后一行回应让时间;
// reactions.cpp:402-418, chat.style:25,100,673-695.
func TestMessageBottomReactionBoundary(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Reaction()
	mm := th.Metrics().Message()
	for _, side := range []chat.Side{chat.Incoming, chat.Outgoing} {
		for _, rows := range []int{1, 2, 3} {
			t.Run(string(rune('a'+side))+string(rune('0'+rows)), func(t *testing.T) {
				var width float32 = 300
				var tail ui.Span
				var pillWidth, fh, ascent, countAscent float32
				items := []content.Reaction{{ID: "a", Emoji: "+", Count: 1}, {ID: "b", Emoji: "+", Count: 1}, {ID: "c", Emoji: "+", Count: 1}}[:rows]
				tt := ui.NewTester(func(c *ui.Context) {
					theme.Apply(c, th)
					tail = chat.InfoReservation(c, chat.Info{Label: "18:42", Delivery: chat.Read}, chat.BodyOptions{Side: side})
					cw, _ := c.MeasureText(0, ui.Span{Text: "1", Size: th.Typography().ContentTitle().Size(), Weight: th.Typography().ContentTitle().Weight()})
					pillWidth = m.PaddingLeft() + m.IconSize() + m.LabelGap() + cw + m.PaddingRight()
					_, fh = c.MeasureText(0, ui.Span{Text: "18:42", Size: th.Typography().MessageTime().Size()})
					ascent = ui.Font{Size: th.Typography().MessageTime().Size()}.Metrics().Ascent
					countAscent = ui.Font{Size: th.Typography().ContentTitle().Size(), Weight: th.Typography().ContentTitle().Weight()}.Metrics().Ascent
					ui.Column(c).Padding(20).Children(func() {
						ui.Column(c).Width(width).Children(func() {
							content.Reactions(c, items, content.ReactionOptions{InBubble: true, Trailing: tail}).Label("reactions")
						})
					})
				}, 500, 300)
				// Make one full-width capsule per row, followed by a final ordinary pill.
				for i := range rows - 1 {
					items[i].CountLabel = "1234567890123456789012345678901234567890"
				}
				width = pillWidth - m.InBubbleShift() + tail.ObjectWidth
				tt.Frame()
				r := bottomBox(t, tt, "reactions")
				last := bottomBox(t, tt, "+ 1")
				expected := float32(rows)*(m.IconSize()+m.PaddingTop()+m.PaddingBottom()) + float32(rows-1)*m.Gap()
				bottomAssert(t, "exact width retains final reaction row", bottomNear(r.H, expected))
				bottomAssert(t, "bubble reaction left shift", bottomNear(last.X, r.X-m.InBubbleShift()))
				height := r.H
				width--
				tt.Frame()
				r = bottomBox(t, tt, "reactions")
				bottomAssert(t, "one DIP shortage adds only K", bottomNear(r.H-height, tail.ObjectHeight))
				bottomAssert(t, "earlier rows unchanged by time", bottomNear(bottomBox(t, tt, "+ 1").Y, last.Y))
				// Compare independently derived baselines. Telegram intentionally places
				// the date lower; equality would contradict its 5 DIP bottom delta.
				dateBaseline := height + mm.TimestampDeltaY() - fh + ascent
				countBaseline := height - m.IconSize() - m.PaddingTop() - m.PaddingBottom() + float32(math.Floor(float64((m.IconSize()+m.PaddingTop()+m.PaddingBottom()-fh)/2))) + countAscent
				t.Logf("rows=%d side=%d width=%.6f S=%.6f K=%.6f height %.6f->%.6f baseline delta=%.6f", rows, side, width+1, tail.ObjectWidth, tail.ObjectHeight, height, r.H, dateBaseline-countBaseline)
			})
		}
	}
}

// message-bottom.md#没有回应时正文末行让时间: actual shaped widths,
// exact fit versus one DIP shortage, including the Markdown insertion path.
func TestMessageBottomTextBoundary(t *testing.T) {
	th := theme.Light()
	var width float32 = 200
	var tail ui.Span
	var tw, fh float32
	state := content.MarkdownState{}
	for _, markdown := range []bool{false, true} {
		tt := ui.NewTester(func(c *ui.Context) {
			theme.Apply(c, th)
			tail = chat.InfoReservation(c, chat.Info{Label: "18:42"}, chat.BodyOptions{})
			tw, fh = c.MeasureText(0, ui.Span{Text: "Hello", Size: th.Typography().Message().Size()})
			ui.Column(c).Padding(20).Children(func() {
				ui.Column(c).Width(width).Children(func() {
					if markdown {
						content.Markdown(c, "Hello", &state, content.MarkdownOptions{Trailing: tail}).Label("body")
					} else {
						chat.Body(c, "Hello", chat.Info{Label: "18:42"}, chat.BodyOptions{}).Label("body")
					}
				})
			})
		}, 400, 200)
		width = tw + tail.ObjectWidth
		tt.Frame()
		h := bottomBox(t, tt, "body").H
		bottomAssert(t, "exact text fit uses one real line", bottomNear(h, fh))
		width--
		tt.Frame()
		overflow := bottomBox(t, tt, "body").H
		bottomAssert(t, "text one DIP shortage adds K", bottomNear(overflow-h, tail.ObjectHeight))
		t.Logf("markdown=%v width %.6f->%.6f height %.6f->%.6f K=%.6f", markdown, width+1, width, h, overflow, tail.ObjectHeight)
	}
}

func TestMessageBottomBubbleOwnership(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Message()
	r := th.Metrics().Reaction()
	var width float32 = 240
	var tail ui.Span
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		ui.Column(c).Padding(20).Children(func() {
			ui.Column(c).Width(width).Children(func() {
				chat.Bubble(c, chat.Message{Text: "Hello", Info: chat.Info{Label: "18:42"}}, chat.BubbleOptions{Footer: func(s ui.Span) {
					tail = s
					content.Reactions(c, []content.Reaction{{ID: "a", Emoji: "+", Count: 1}}, content.ReactionOptions{InBubble: true, Trailing: s}).Label("reactions")
				}}, nil).Label("bubble")
			})
		})
	}, 500, 300)
	b := bottomBox(t, tt, "bubble")
	p := bottomBox(t, tt, "+ 1")
	date := bottomBox(t, tt, "18:42")
	reactions := bottomBox(t, tt, "reactions")
	bottomAssert(t, "reaction and date share final row", date.Y < p.Y+p.H && date.Y+date.H > p.Y)
	bottomAssert(t, "date bottom differs from pill by five", bottomNear(date.Y+date.H-p.Y-p.H, m.TimestampDeltaY()))
	bottomAssert(t, "reaction left inset is eight", bottomNear(p.X-b.X, m.PaddingLeft()-r.InBubbleShift()))
	bottomAssert(t, "reaction footer bottom padding", bottomNear(b.Y+b.H-reactions.Y-reactions.H, m.PaddingBottom()))
	bottomAssert(t, "time right inset is nine", bottomNear(b.X+b.W-date.X-date.W, m.PaddingRight()-m.TimestampDeltaX()))
	bottomAssert(t, "body footer has source gap", bottomNear(reactions.Y-b.Y-m.PaddingTop()-date.H, th.Metrics().Preview().MediaGap()))
	bottomAssert(t, "natural bubble includes tail width", b.W >= p.W-r.InBubbleShift()+tail.ObjectWidth+m.PaddingLeft()+m.PaddingRight()-0.02)
	count := bottomBox(t, tt, "Reaction count a")
	regular := ui.Font{Size: th.Typography().MessageTime().Size()}.Metrics()
	semibold := ui.Font{Size: th.Typography().ContentTitle().Size(), Weight: th.Typography().ContentTitle().Weight()}.Metrics()
	actualDelta := date.Y + regular.Ascent - count.Y - semibold.Ascent
	expectedDelta := p.H + m.TimestampDeltaY() - date.H - float32(math.Floor(float64((p.H-count.H)/2))) + regular.Ascent - semibold.Ascent
	bottomAssert(t, "actual count and date baselines follow specification", bottomNear(actualDelta, expectedDelta))
	t.Logf("bubble height=%.6f pill=%+v date=%+v baseline delta=%.6f", b.H, p, date, actualDelta)
}
