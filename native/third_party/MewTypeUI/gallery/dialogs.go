package gallery

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func init() {
	Register(Page{ID: "dialogs/rows", Area: "dialogs", Title: "Conversation rows", View: dialogRows})
	Register(Page{ID: "dialogs/compact", Area: "dialogs", Title: "Compact conversations", View: dialogCompact})
	Register(Page{ID: "dialogs/list", Area: "dialogs", Title: "Virtual conversation list · 50,000", View: dialogList})
	Register(Page{ID: "dialogs/search", Area: "dialogs", Title: "Search", View: dialogSearch})
	Register(Page{ID: "dialogs/rail", Area: "dialogs", Title: "Folder rail", View: dialogRail})
	Register(Page{ID: "dialogs/groups", Area: "dialogs", Title: "Group headings", View: dialogGroups})
	Register(Page{ID: "dialogs/details", Area: "dialogs", Title: "Stories, corner badges and activities", View: dialogDetails})
	Register(Page{ID: "dialogs/scopes", Area: "dialogs", Title: "Search scopes and sender", View: dialogScopes})
	Register(Page{ID: "dialogs/folders", Area: "dialogs", Title: "文件夹切换", View: dialogFolderPages})
}

func dialogSamples() []dialogs.Conversation {
	return []dialogs.Conversation{
		{ID: "ordinary", Name: "Rin", Initials: "R", Preview: "See you at rehearsal", Time: "09:41"},
		{ID: "online", Name: "Sora", Initials: "S", Preview: "I am on my way", Time: "09:42", Online: true, Unread: 12},
		{ID: "muted", Name: "Quiet room", Initials: "Q", Kind: dialogs.Group, Preview: "A long preview that must leave room for the unread counter", Time: "09:43", Muted: true, Unread: 128},
		{ID: "pinned", Name: "Pinned notes", Initials: "P", Preview: "Remember the set list", Time: "Tue", Pinned: true},
		{ID: "draft", Name: "Draft", Initials: "D", Preview: "Earlier message", Time: "09:45", DraftLabel: "Draft: ", Draft: "Bring the spare cable", DraftSaving: true, Pinned: true},
		{ID: "typing", Name: "Typing", Initials: "T", Preview: "Earlier message", Activity: "typing…", Draft: "Unsaved reply", DraftLabel: "Draft: "},
		{ID: "sending", Name: "Sending", Initials: "S", Preview: "Message on its way", Receipt: dialogs.Sending, Time: "09:47"},
		{ID: "sent", Name: "Sent", Initials: "S", Preview: "Waiting to be read", Receipt: dialogs.Sent, Time: "09:48"},
		{ID: "read", Name: "Read", Initials: "R", Preview: "All caught up", Receipt: dialogs.Read, Time: "09:49"},
		{ID: "mentions", Name: "Mentions", Initials: "M", Preview: "Rin mentioned you", Unread: 2, Mention: true},
		{ID: "reactions", Name: "Reactions", Initials: "R", Preview: "A reaction to your message", Reaction: true},
		{ID: "polls", Name: "Poll", Initials: "P", Preview: "Choose next week's song", Poll: true, UnreadMark: true},
		{ID: "channel", Name: "Announcements", Initials: "A", Kind: dialogs.Channel, Preview: "Pinned a message", Service: true, Verified: true},
		{ID: "bot", Name: "Practice helper", Initials: "P", Kind: dialogs.Bot, Preview: "Session complete", Premium: true},
		{ID: "secret", Name: "Private room", Initials: "P", Kind: dialogs.Secret, Preview: "Encrypted conversation", Warning: "SCAM"},
		{ID: "archive", Name: "Archived chats", Kind: dialogs.Archive, Preview: "Old rehearsal room, project notes", Muted: true, Unread: 4},
		{ID: "tagged", Name: "Tagged conversation", Initials: "T", Preview: "A message with folder tags", Tags: []dialogs.Tag{{ID: "work", Label: "Work", Color: theme.Light().Colors().Conversation().TextForegroundService()}}},
		{ID: "forum", Name: "Rehearsal forum", Initials: "F", Kind: dialogs.Group, Forum: true, Topics: "General  ›  Set list", Sender: "Rin: ", Preview: "One more song", Unread: 3},
		{ID: "forum-tagged", Name: "Forum with tags", Initials: "F", Forum: true, Topics: "General  ›  Gear", Preview: "Spare strings", Tags: []dialogs.Tag{{ID: "music", Label: "Music", Color: theme.Light().Colors().Conversation().TextForegroundService()}}},
		{ID: "empty", Name: "An empty conversation", Initials: "E"},
		{ID: "long", Name: "An exceptionally long conversation name that should truncate", Initials: "L", Preview: "長い日本語のプレビュー、也可以显示中文与 emoji 🎸", Unread: 9999, Muted: true, Verified: true},
	}
}

func dialogRows(c *ui.Context) {
	t := theme.T(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Text(c, "Hover and hold a row to inspect the real pointer states. Blue rows are selected.")
		ui.Column(c).Width(t.Metrics().Message().MaxWidth()).Children(func() {
			for i, item := range dialogSamples() {
				c.Key(item.ID)
				dialogs.Row(c, item, dialogs.RowOptions{Selected: i == 1 || i == 8, Disabled: i == 19})
			}
		})
	})
}
func dialogCompact(c *ui.Context) {
	ui.Scroll(c).Fill().Children(func() {
		ui.Text(c, "66 DIP compact list: counters overlay the avatars; names remain accessible.")
		ui.Column(c).Width(dialogs.CompactWidth(c)).Children(func() {
			for i, item := range dialogSamples() {
				c.Key(item.ID)
				dialogs.Row(c, item, dialogs.RowOptions{Compact: true, Selected: i == 1})
			}
		})
	})
}

type dialogListState struct {
	model                      *dialogs.Model
	list                       dialogs.ListState
	query, previousQuery, last string
	all                        []dialogs.Entry
}

func newDialogListState() dialogListState {
	entries := make([]dialogs.Entry, 50000)
	for i := range entries {
		entries[i].Conversation = dialogs.Conversation{ID: fmt.Sprintf("conversation-%d", i), Name: fmt.Sprintf("Conversation %05d", i), Initials: fmt.Sprintf("%d", i%100), Preview: "Use ↑ ↓, Home, End, PgUp/PgDn; double click or Enter to activate.", Time: "09:41", Unread: i % 17}
	}
	return dialogListState{model: dialogs.NewModel(entries), all: entries}
}
func dialogList(c *ui.Context) {
	s := ui.Local(c.Root(), "dialogs/list", newDialogListState)
	ui.Column(c).Fill().Children(func() {
		dialogs.Search(c, &s.query, dialogs.SearchOptions{Label: "Search conversations", Placeholder: "Search 50,000 conversations", ClearLabel: "Clear search"})
		if s.query != s.previousQuery {
			s.previousQuery = s.query
			if s.query == "" {
				s.model = dialogs.NewModel(s.all)
			} else {
				var filtered []dialogs.Entry
				query := strings.ToLower(s.query)
				for _, item := range s.all {
					if strings.Contains(strings.ToLower(item.Conversation.Name), query) {
						filtered = append(filtered, item)
					}
				}
				s.model = dialogs.NewModel(filtered)
			}
		}
		dialogs.List(c, s.model, &s.list, dialogs.ListOptions{Label: "Conversations", OnActivate: func(id string) { s.last = id }}).Grow(1)
		ui.Text(c, "Selected: "+s.list.SelectedID+" · Activated: "+s.last)
	})
}

type dialogSearchState struct{ query, event string }

func dialogSearch(c *ui.Context) {
	s := ui.Local(c.Root(), "dialogs/search", func() dialogSearchState { return dialogSearchState{} })
	ui.Column(c).Width(theme.T(c).Metrics().Message().MaxWidth()).Gap(theme.T(c).Metrics().Search().OuterPadding()).Children(func() {
		dialogs.Search(c, &s.query, dialogs.SearchOptions{Label: "Search", Placeholder: "Search", ClearLabel: "Clear search", OnChange: func(q string) { s.event = "Changed: " + q }, OnSubmit: func(q string) { s.event = "Submitted: " + q }})
		ui.Text(c, s.event)
		c.Key("disabled")
		dialogs.Search(c, &s.query, dialogs.SearchOptions{Label: "Disabled search", Placeholder: "Search", ClearLabel: "Clear disabled search", Disabled: true})
	})
}
func dialogRail(c *ui.Context) {
	s := ui.Local(c.Root(), "dialogs/rail", func() dialogs.RailState { return dialogs.RailState{SelectedID: "all"} })
	folders := []dialogs.Folder{{ID: "all", Name: "All chats", Badge: "12"}, {ID: "music", Name: "Music", Badge: "3"}, {ID: "quiet", Name: "Muted", Badge: "999+", Muted: true}, {ID: "wrapped", Name: "Long folder name wraps to three lines"}, {ID: "locked", Name: "Locked", Locked: true}}
	ui.Row(c).Fill().Gap(theme.T(c).Metrics().Search().OuterPadding()).Children(func() {
		dialogs.Rail(c, folders, s, dialogs.RailOptions{}).FillHeight()
		c.Key("icons")
		icons := ui.Local(c.Root(), "dialogs/rail-icons", func() dialogs.RailState { return dialogs.RailState{SelectedID: "quiet"} })
		dialogs.Rail(c, folders, icons, dialogs.RailOptions{Mode: dialogs.IconsOnly}).FillHeight()
		c.Key("text")
		labels := ui.Local(c.Root(), "dialogs/rail-labels", func() dialogs.RailState { return dialogs.RailState{SelectedID: "music"} })
		dialogs.Rail(c, folders, labels, dialogs.RailOptions{Mode: dialogs.TextOnly}).FillHeight()
	})
}
func dialogGroups(c *ui.Context) {
	ui.Column(c).Width(theme.T(c).Metrics().Message().MaxWidth()).Children(func() {
		dialogs.GroupHeading(c, "Recent conversations")
		for _, item := range dialogSamples()[:3] {
			c.Key(item.ID)
			dialogs.Row(c, item, dialogs.RowOptions{})
		}
		dialogs.GroupHeading(c, "Global search results")
	})
}

func dialogDetails(c *ui.Context) {
	t := theme.T(c)
	windowWidth, _ := c.Size()
	event := ui.Local(c.Root(), "dialogs/details-event", func() string { return "" })
	items := []dialogs.Conversation{
		{ID: "stories-unread", Name: "Unread stories", Initials: "U", Stories: dialogs.Stories{Count: 5, Unread: 3}, Preview: "Click the avatar"},
		{ID: "stories-read", Name: "Read stories", Initials: "R", Stories: dialogs.Stories{Count: 3}},
		{ID: "stories-live", Name: "Live story", Initials: "L", Stories: dialogs.Stories{Live: true}},
		{ID: "call-idle", Name: "Group call", Initials: "G", Kind: dialogs.Group, Call: true},
		{ID: "call-speaking", Name: "Speaking in call", Initials: "S", Kind: dialogs.Group, Call: true, Speaking: true},
		{ID: "subscription", Name: "Subscription", Initials: "S", Subscribed: true},
		{ID: "ttl", Name: "Auto-delete", Initials: "T", TTL: "1d"},
		{ID: "community", Name: "Community member", Initials: "C", CommunityMember: true},
		{ID: "promotion", Name: "Sponsored room", Initials: "P", Time: "09:41", Promotion: &dialogs.Promotion{Label: "Sponsored", Message: "Prepared sponsor message"}},
		{ID: "codes", Name: "Verification codes", VerificationCodes: true, Preview: "Example code: 12345", Unread: 1},
		{ID: "record", Name: "Recording", Initials: "R", Activity: "recording voice…", ActivityKind: dialogs.RecordingActivity},
		{ID: "upload", Name: "Uploading", Initials: "U", Activity: "uploading a file…", ActivityKind: dialogs.UploadingActivity},
		{ID: "sticker", Name: "Choosing sticker", Initials: "S", Activity: "choosing a sticker…", ActivityKind: dialogs.ChoosingStickerActivity},
	}
	ui.Column(c).Fill().Gap(0).Children(func() {
		ui.Text(c, *event)
		ui.Scroll(c).FillWidth().Grow(1).Children(func() {
			for _, selected := range []bool{false, true} {
				c.Key(selected)
				ui.Row(c).Gap(t.Metrics().Search().OuterPadding()).Children(func() {
					for _, compact := range []bool{false, true} {
						c.Key(compact)
						width := min(t.Metrics().Message().MaxWidth(), windowWidth-dialogs.CompactWidth(c)-t.Metrics().Search().OuterPadding())
						if compact {
							width = dialogs.CompactWidth(c)
						}
						ui.Column(c).Width(width).Gap(0).Children(func() {
							for _, item := range items {
								c.Key(item.ID)
								dialogs.Row(c, item, dialogs.RowOptions{Selected: selected, Compact: compact, OnStory: func(id string) { *event = "Stories " + id }, OnCommunity: func(id string) { *event = "Community " + id }})
							}
						})
					}
				})
			}
		})
	})
}

func dialogScopes(c *ui.Context) {
	type state struct {
		selected, event string
		sender          bool
	}
	s := ui.Local(c.Root(), "dialogs/scopes", func() state { return state{selected: "chat", sender: true} })
	items := []dialogs.SearchScope{{ID: "own", Label: "My messages"}, {ID: "chat", Label: "This chat"}, {ID: "topic", Label: "This topic"}, {ID: "community", Label: "This community"}, {ID: "archive", Label: "Archive"}, {ID: "public", Label: "Public posts"}}
	ui.Column(c).Width(theme.T(c).Metrics().Message().MaxWidth()).MaxWidthPercent(100).Children(func() {
		var sender *dialogs.SearchScope
		if s.sender {
			sender = &dialogs.SearchScope{ID: "rin", Label: "From Rin"}
		}
		dialogs.SearchScopes(c, items, s.selected, dialogs.SearchScopeOptions{Heading: "Search in", Sender: sender, CancelScopeLabel: "Cancel scope", CancelSenderLabel: "Cancel sender", OnSelect: func(id string) { s.selected = id }, OnCancelScope: func() { s.selected = "" }, OnSender: func() { s.event = "Host sender chooser requested" }, OnCancelSender: func() { s.sender = false }})
		ui.Button(c, "Reset search scope").OnClick(func() { s.selected = "chat"; s.sender = true })
		ui.Text(c, s.event)
	})
}

func dialogFolderPages(c *ui.Context) {
	type folderDemo struct {
		rail   dialogs.RailState
		lists  [3]dialogs.ListState
		models [3]*dialogs.Model
	}
	s := ui.Local(c.Root(), "folder-pages", func() folderDemo {
		s := folderDemo{rail: dialogs.RailState{SelectedID: "all"}}
		for i := range s.models {
			var entries []dialogs.Entry
			for j, row := range dialogSamples()[:10] {
				if i == 0 || j%2 == i-1 {
					entries = append(entries, dialogs.Entry{Conversation: row})
				}
			}
			s.models[i] = dialogs.NewModel(entries)
		}
		return s
	})
	folders := []dialogs.Folder{{ID: "all", Name: "全部"}, {ID: "work", Name: "工作"}, {ID: "friends", Name: "朋友"}}
	ids := []string{"all", "work", "friends"}
	ui.Row(c).Fill().Gap(0).Children(func() {
		dialogs.Rail(c, folders, &s.rail, dialogs.RailOptions{}).FillHeight()
		ui.Column(c).Grow(1).MinWidth(0).FillHeight().Gap(0).Children(func() {
			ui.Row(c).FillWidth().Height(theme.T(c).Metrics().Layout().FolderTabsHeight()).Gap(0).Children(func() {
				for _, folder := range folders {
					ui.ButtonBase(c.Key(folder.ID)).Grow(1).Height(theme.T(c).Metrics().Layout().FolderTabsHeight()).Center().Label("切换" + folder.Name).OnClick(func() { s.rail.SelectedID = folder.ID }).Children(func() { ui.Text(c, folder.Name) })
				}
			})
			dialogs.FolderPages(c, ids, s.rail.SelectedID, func(id string) {
				i := 0
				for ids[i] != id {
					i++
				}
				ui.Column(c).Fill().Gap(0).Children(func() {
					dialogs.GroupHeading(c, folders[i].Name)
					dialogs.List(c, s.models[i], &s.lists[i], dialogs.ListOptions{Label: "文件夹" + id}).Grow(1)
				})
			})
		})
	})
}
