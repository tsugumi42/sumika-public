package gallery

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "chat/header", Area: "chat", Title: "Header", View: chatHeaders})
	Register(Page{ID: "chat/bubbles", Area: "chat", Title: "Message bubbles", View: chatBubbles})
	Register(Page{ID: "chat/dates", Area: "chat", Title: "Date markers", View: chatDates})
	Register(Page{ID: "chat/unread", Area: "chat", Title: "Unread marker", View: chatUnread})
	Register(Page{ID: "chat/services", Area: "chat", Title: "System notices", View: chatServices})
	Register(Page{ID: "chat/latest", Area: "chat", Title: "Jump to latest", View: chatLatest})
	Register(Page{ID: "chat/reveal", Area: "chat", Title: "Message reveal", View: chatReveal})
	Register(Page{ID: "chat/history", Area: "chat", Title: "History composition", View: chatHistory})
	Register(Page{ID: "chat/sending", Area: "chat", Title: "Sending snapshot", View: chatPage})
	Register(Page{ID: "chat/avatars", Area: "chat", Title: "Group-end viewport avatars", View: chatAvatars})
}

func chatHeaders(c *ui.Context) {
	state := ui.Local(c.Root(), "chat.header.demo", func() string { return "No action yet" })
	ui.Column(c).Fill().Children(func() {
		for _, status := range []chat.Status{chat.Offline, chat.Online, chat.Typing, chat.Connecting} {
			c.Key(status)
			subtitle := []string{"last seen recently", "online", "typing…", "connecting…"}[status]
			chat.Header(c, chat.HeaderInfo{Title: "Evening study group", Subtitle: subtitle, Status: status}, chat.HeaderOptions{
				OnBack: func() { *state = "Back" }, BackLabel: "Back",
				OnOpen: func() { *state = "Open conversation" },
				Actions: []chat.Action{
					{ID: "search", Label: "Search messages", Kind: chat.SearchAction, OnActivate: func() { *state = "Search" }},
					{ID: "call", Label: "Call", Kind: chat.CallAction, Disabled: status == chat.Connecting, OnActivate: func() { *state = "Call" }},
					{ID: "menu", Label: "More actions", Kind: chat.MenuAction, Active: status == chat.Online, OnActivate: func() { *state = "Menu" }},
				},
			})
		}
		chat.Header(c, chat.HeaderInfo{}, chat.HeaderOptions{SelectionLabel: "3 messages selected", Actions: []chat.Action{
			{ID: "cancel", Label: "Cancel selection", Kind: chat.CancelAction, OnActivate: func() { *state = "Selection cleared" }},
		}})
		chat.Header(c, chat.HeaderInfo{Title: strings.Repeat("A long conversation name ", 8), Subtitle: "72 members, 4 online"}, chat.HeaderOptions{
			Actions: []chat.Action{{ID: "menu", Label: "Conversation menu", Kind: chat.MenuAction}},
		})
		ui.Text(c, *state)
	})
}

func chatBubbles(c *ui.Context) {
	selected := ui.Local(c.Root(), "chat.bubble.selected", func() bool { return false })
	ui.Scroll(c).Fill().Children(func() {
		ui.Checkbox(c, selected, "Select all samples")
		for _, side := range []chat.Side{chat.Incoming, chat.Outgoing} {
			for i, group := range []chat.Group{{}, {Next: true}, {Previous: true, Next: true}, {Previous: true}} {
				c.Key(fmt.Sprintf("%d:%d", side, i))
				chat.Row(c, chat.Message{Text: "Consecutive messages share only the sending-side corners.", SenderName: "Mio", Info: chat.Info{Label: "18:42", Delivery: chat.Read}},
					chat.RowOptions{BubbleOptions: chat.BubbleOptions{Side: side, Group: group, Selected: *selected}, ReserveAvatar: side == chat.Incoming}, nil)
			}
		}
		for _, delivery := range []chat.Delivery{chat.Sending, chat.Sent, chat.Read, chat.Failed} {
			c.Key(delivery)
			chat.Row(c, chat.Message{Text: "Short message", Info: chat.Info{Label: "edited 18:43", Delivery: delivery}}, chat.RowOptions{BubbleOptions: chat.BubbleOptions{Side: chat.Outgoing, Selected: *selected}}, nil)
		}
		chat.Row(c, chat.Message{Text: "Final line and timestamp wrap together, without overlapping.\nThis final line ends here.", Info: chat.Info{Label: "18:44"}}, chat.RowOptions{}, nil)
		chat.Row(c, chat.Message{Text: "آخر سطر باللغة العربية", Info: chat.Info{Label: "18:45", Delivery: chat.Read}}, chat.RowOptions{BubbleOptions: chat.BubbleOptions{Side: chat.Outgoing}}, nil)
		messageBottomSamples(c, *selected)
	})
}

// paint/message-bottom.md#图片与状态矩阵; deliberately includes the invalid
// reactions-only input as an exterior list, without inventing a message shell.
func messageBottomSamples(c *ui.Context, selected bool) {
	items := []content.Reaction{{ID: "like", Emoji: "👍", Count: 1}, {ID: "heart", Emoji: "♥", Count: 12, Own: true}, {ID: "zero", Emoji: "☆"}}
	for _, side := range []chat.Side{chat.Incoming, chat.Outgoing} {
		for _, kind := range []string{"inline", "wrapped", "image", "caption", "edited", "only-reactions", "text-tail"} {
			c.Key(fmt.Sprintf("bottom:%d:%s", side, kind))
			ui.Column(c).FillWidth().Children(func() {
				ui.Text(c, fmt.Sprintf("%s · %v", kind, side))
				a := content.Appearance{Outgoing: side == chat.Outgoing, Selected: selected}
				info := chat.Info{Label: "18:52"}
				if side == chat.Outgoing {
					info.Delivery = chat.Read
				}
				if kind == "edited" {
					info.Prefix = "已编辑"
					info.Views = "1.2K"
					info.Replies = "3"
				}
				if kind == "only-reactions" {
					content.Reactions(c, items, content.ReactionOptions{Appearance: a, Outside: true})
					return
				}
				o := chat.RowOptions{BubbleOptions: chat.BubbleOptions{Side: side, Selected: selected}}
				message := chat.Message{Text: "回应最后一行和时间共享空间。", Info: info}
				if kind == "text-tail" {
					message.Text = "Time fits here."
				} else {
					o.Footer = func(tail ui.Span) {
						list := items[:1]
						if kind == "wrapped" {
							list = []content.Reaction{{ID: "a", Emoji: "👍", CountLabel: "123456789012345"}, {ID: "b", Emoji: "♥", CountLabel: "123456789012345"}, {ID: "c", Emoji: "☆", Count: 3}, {ID: "d", Emoji: "👍", CountLabel: "123456789012345"}, {ID: "e", Emoji: "♥", CountLabel: "123456789012345"}}
						}
						content.Reactions(c, list, content.ReactionOptions{Appearance: a, InBubble: kind != "image", Outside: kind == "image", Trailing: tail})
					}
				}
				var media func()
				if kind == "image" || kind == "caption" {
					media = func() {
						content.Photo(c, demoMedia("Bottom sample photo"), content.MediaOptions{Width: theme.T(c).Metrics().Media().BubbleMinWidth(), Preview: true, BottomInfo: func() {
							if kind == "image" {
								chat.ImageInfo(c, info, chat.BodyOptions{Side: side, Selected: selected})
							}
						}})
					}
					if kind == "image" {
						message.Text = ""
						o.MediaBottom = true
					}
				}
				chat.Row(c, message, o, media)
			})
		}
	}
}

func chatDates(c *ui.Context) {
	chat.DateMarker(c, "November 3", chat.DateOptions{})
	chat.DateMarker(c, "Today", chat.DateOptions{})
	ui.Text(c, "Scroll the History composition page to exercise the floating date and its timeout.")
}
func chatUnread(c *ui.Context) { chat.UnreadMarker(c, "Unread messages") }
func chatServices(c *ui.Context) {
	chat.ServiceMessage(c, "Aoi created the group", chat.ServiceOptions{})
	chat.ServiceMessage(c, "Mio pinned a message", chat.ServiceOptions{Selected: true})
	chat.ServiceMessage(c, strings.Repeat("A host-localized system notice that can wrap. ", 8), chat.ServiceOptions{})
}
func chatLatest(c *ui.Context) {
	clicked := ui.Local(c.Root(), "chat.latest.demo", func() int { return 0 })
	ui.Row(c).Fill().Children(func() {
		for _, count := range []int{0, 3, 12345} {
			c.Key(count)
			ui.Column(c).Grow(1).FillHeight().Clip().Children(func() {
				ui.Text(c, fmt.Sprintf("Unread %d; clicks %d", count, *clicked))
				chat.LatestButton(c, chat.LatestOptions{Visible: true, Unread: count, Label: "Jump to latest", OnActivate: func() { *clicked++ }})
			})
		}
	})
}
func chatReveal(c *ui.Context) {
	state := ui.Local(c.Root(), "chat.reveal.demo", func() chat.RevealState { return chat.RevealState{} })
	ui.Button(c, "Reveal new message").OnClick(func() { state.Start(c.Now()) })
	height := theme.T(c).Metrics().Header().Height()
	chat.Reveal(c, state, chat.RevealOptions{Height: height}, func() {
		chat.Row(c, chat.Message{Text: "New messages open their layout space over 150 ms.", Info: chat.Info{Label: "18:47", Delivery: chat.Sent}}, chat.RowOptions{BubbleOptions: chat.BubbleOptions{Side: chat.Outgoing}}, nil)
	})
}

type chatHistoryState struct {
	scroll   ui.ScrollState
	viewport ui.Handle
	days     [3]ui.Handle
	date     chat.DateState
	selected map[int]bool
}

func chatHistory(c *ui.Context) {
	s := ui.Local(c.Root(), "chat.history.demo", func() chatHistoryState { return chatHistoryState{selected: map[int]bool{}} })
	ui.Column(c).Fill().Children(func() {
		chat.Header(c, chat.HeaderInfo{Title: "Evening study group", Subtitle: "3 members, 2 online", Status: chat.Online}, chat.HeaderOptions{})
		ui.Column(c).Grow(1).MinHeight(0).Clip().Bind(&s.viewport).Children(func() {
			ui.Scroll(c).Fill().TrackScroll(&s.scroll).Children(func() {
				for i := range 72 {
					c.Key(fmt.Sprintf("message-%d", i))
					ui.Column(c).Children(func() {
						if i%24 == 0 {
							day := i / 24
							chat.DateMarker(c, []string{"November 1", "November 2", "Today"}[day], chat.DateOptions{}).Bind(&s.days[day])
						}
						if i == 64 {
							chat.UnreadMarker(c, "Unread messages")
						}
						side := chat.Side(i % 2)
						chat.Row(c, chat.Message{Text: fmt.Sprintf("Message %d: %s", i, strings.Repeat("A prepared, fictional conversation. ", i%3+1)), SenderName: []string{"Aoi", "Mio"}[side], Info: chat.Info{Label: "18:42", Delivery: chat.Read}},
							chat.RowOptions{BubbleOptions: chat.BubbleOptions{Side: side, Selected: s.selected[i]}, ReserveAvatar: side == chat.Incoming}, nil).
							ContextMenu(func(m *ui.Menu) {
								if m.Item("Toggle selection").Chosen() {
									s.selected[i] = !s.selected[i]
								}
							})
					})
				}
			})
			viewport := s.viewport.Bounds(c)
			day, bottom := 0, float32(0)
			for i := 1; i < len(s.days); i++ {
				next := s.days[i].Bounds(c)
				if next.Y <= viewport.Y {
					day = i
				} else {
					bottom = next.Y - viewport.Y
					break
				}
			}
			chat.FloatingDate(c, []string{"November 1", "November 2", "Today"}[day], &s.date, chat.FloatingDateOptions{
				ScrollOffset: s.scroll.Y, DayBottom: bottom,
			})
			chat.LatestButton(c, chat.LatestOptions{Visible: chat.LatestVisible(c, s.scroll.MaxY-s.scroll.Y, true), Unread: 8,
				Label: "Jump to latest", OnActivate: func() {
					s.scroll.ScrollTo(0, math.MaxFloat32, ui.Animation{Duration: theme.T(c).Motion().ChatScroll().Duration(), Ease: theme.T(c).Motion().ChatScroll().Easing().Curve()})
				},
			})
		})
	})
}

func chatAvatars(c *ui.Context) {
	t := theme.T(c)
	groupTop := ui.Local(c.Root(), "chat.avatar.groupTop", func() float32 { return 0 })
	ui.Column(c).Fill().Children(func() {
		ui.Text(c, "Scroll: the sender photo follows the visible group bottom.")
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Draw(func(_ *ui.Painter, r ui.Rect) { *groupTop = r.Y + t.Metrics().Message().MarginTop() }).Children(func() {
				for i := range 6 {
					chat.Row(c.Key(fmt.Sprintf("avatar-message-%d", i)), chat.Message{Text: strings.Repeat("A fictional grouped message. ", 10), SenderName: "Mio"}, chat.RowOptions{ReserveAvatar: true, AvatarGroupTop: groupTop, BubbleOptions: chat.BubbleOptions{Group: chat.Group{Previous: i > 0, Next: i < 5}}}, nil)
				}
			})
		})
	})
}
