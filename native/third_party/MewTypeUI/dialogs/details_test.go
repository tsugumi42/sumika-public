package dialogs_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/dialogs"
	"github.com/tsugumi42/MewTypeUI/theme"
)

func TestStoryOutlineAndIndependentActivation(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Conversation()
	item := dialogs.Conversation{ID: "peer", Name: "Stories", Initials: "S", Stories: dialogs.Stories{Count: 5, Unread: 3}}
	story, chat := "", ""
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		services = c.Services()
		dialogs.Row(c, item, dialogs.RowOptions{OnActivate: func(id string) { chat = id }, OnStory: func(id string) { story = id }})
	}, 360, 150)
	avatar := found(tt, "Stories peer")
	// dialogs_row.cpp:523-525: inset = 1.5 * 2 DIP on each edge.
	check(t, "story avatar inset", near(avatar.X, m.PaddingLeft()+m.StoryWidth()*1.5) && near(avatar.W, m.AvatarSize()-3*m.StoryWidth()))
	if err := tt.Click("Stories peer"); err != nil {
		t.Fatal(err)
	}
	check(t, "story click keeps chat action separate", story == "peer" && chat == "")
	unread := append([]byte(nil), tt.Image().Pix...)
	item.Stories.Unread = 0
	services.Invalidate()
	tt.Frame()
	check(t, "read story palette differs", !bytes.Equal(unread, tt.Image().Pix))
	item.Stories = dialogs.Stories{}
	services.Invalidate()
	tt.Frame()
	_, has := tt.Find("Stories peer")
	check(t, "story action disappears when stories expire", !has)
	check(t, "static story requests no continuing frames", !tt.FramePending())
}

func TestCornerBadgePrecedenceAndCompactUnread(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Conversation()
	item := dialogs.Conversation{ID: "peer", Name: "Badge", Initials: "B", TTL: "1d", CommunityMember: true}
	compact := false
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		services = c.Services()
		dialogs.Row(c, item, dialogs.RowOptions{Compact: compact})
	}, 360, 150)
	has := func(label string) bool { _, ok := tt.Find(label); return ok }
	frame := func() {
		services.Invalidate()
		tt.Frame()
		tt.Advance(th.Motion().Online().Duration() + time.Millisecond)
		tt.Frame()
	}
	check(t, "community precedes ttl", has("Community badge") && !has("TTL badge"))
	item.Call = true
	frame()
	check(t, "call precedes community", has("Call badge") && !has("Community badge"))
	b := found(tt, "Call badge")
	check(t, "call size and position", near(b.W, m.CallSize()) && near(b.X+b.W, m.PaddingLeft()+m.AvatarSize()+m.CallOutset()))
	item.Subscribed = true
	frame()
	check(t, "subscription precedes call", has("Subscription badge") && !has("Call badge"))
	b = found(tt, "Subscription badge")
	check(t, "subscription position", near(b.X+b.W, m.PaddingLeft()+m.AvatarSize()+m.SubscriptionOutset()))
	compact = true
	item.Unread = 3
	frame()
	check(t, "compact unread covers corner badge", has("3") && !has("Subscription badge"))
	item.Unread = 0
	item.Subscribed = false
	item.Call = false
	item.CommunityMember = false
	frame()
	check(t, "ttl returns after top layers clear", has("TTL badge"))
	check(t, "ttl size", near(found(tt, "TTL badge").W, m.TtlSize()))
}

func TestSpecialRowPromotionPrecedence(t *testing.T) {
	item := dialogs.Conversation{ID: "promo", Name: "Room", Initials: "R", Time: "09:41", Preview: "Old message", Draft: "Draft", DraftLabel: "Draft: ", Activity: "typing", Promotion: &dialogs.Promotion{Label: "Sponsored", Message: "Sponsor message"}}
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c, theme.Light()); dialogs.Row(c, item, dialogs.RowOptions{}) }, 360, 150)
	check(t, "promotion replaces time", tt.HasText("Sponsored") && !tt.HasText("09:41"))
	check(t, "promotion overrides draft and activity", tt.HasText("Sponsor message") && !tt.HasText("Draft") && !tt.HasText("typing") && !tt.HasText("Old message"))
}

func TestDistinctActivityPeriodsAndReducedMotion(t *testing.T) {
	th := theme.Light()
	cases := []struct {
		kind   dialogs.ActivityKind
		period time.Duration
	}{
		{dialogs.RecordingActivity, th.Motion().RecordActivity().Duration()},
		{dialogs.UploadingActivity, th.Motion().UploadActivity().Duration()},
		{dialogs.ChoosingStickerActivity, th.Motion().StickerActivity().Duration()},
	}
	for _, tc := range cases {
		t.Run(string(rune('A'+tc.kind)), func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				theme.Apply(c, th)
				dialogs.Row(c, dialogs.Conversation{ID: "peer", Name: "Room", Activity: "Working", ActivityKind: tc.kind}, dialogs.RowOptions{})
			}, 360, 100)
			start := append([]byte(nil), tt.Image().Pix...)
			tt.Advance(tc.period / 4)
			tt.Frame()
			check(t, "activity advances at quarter period", !bytes.Equal(start, tt.Image().Pix))
			tt.Advance(tc.period * 3 / 4)
			tt.Frame()
			check(t, "activity repeats at source period", bytes.Equal(start, tt.Image().Pix))
			tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
			tt.Frame()
			check(t, "reduced motion stops activity frames", !tt.FramePending())
		})
	}
}

func TestCallSpectrumStartStop(t *testing.T) {
	th := theme.Light()
	speaking := false
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		services = c.Services()
		dialogs.Row(c, dialogs.Conversation{ID: "call", Name: "Call", Call: true, Speaking: speaking}, dialogs.RowOptions{})
	}, 360, 150)
	idle := append([]byte(nil), tt.Image().Pix...)
	speaking = true
	services.Invalidate()
	tt.Frame()
	tt.Advance(th.Motion().SpeakingFade().Duration())
	tt.Frame()
	check(t, "speaking changes idle spectrum", !bytes.Equal(idle, tt.Image().Pix))
	speaking = false
	services.Invalidate()
	tt.Frame()
	tt.Advance(th.Motion().SpeakingFade().Duration() + time.Millisecond)
	tt.Frame()
	check(t, "speaking stops at fade duration", bytes.Equal(idle, tt.Image().Pix) && !tt.FramePending())
}

func TestSearchScopeMenuSenderAndCancel(t *testing.T) {
	th := theme.Light()
	m := th.Metrics().Search()
	scopes := []dialogs.SearchScope{{ID: "chat", Label: "This chat"}, {ID: "archive", Label: "Archive"}, {ID: "public", Label: "Public posts"}}
	selected := "chat"
	sender := true
	requested := 0
	tt := ui.NewTester(func(c *ui.Context) {
		theme.Apply(c, th)
		var from *dialogs.SearchScope
		if sender {
			from = &dialogs.SearchScope{ID: "rin", Label: "From Rin"}
		}
		dialogs.SearchScopes(c, scopes, selected, dialogs.SearchScopeOptions{Heading: "Search in", Sender: from, CancelScopeLabel: "Cancel scope", CancelSenderLabel: "Cancel sender", OnSelect: func(id string) { selected = id }, OnCancelScope: func() { selected = "" }, OnSender: func() { requested++ }, OnCancelSender: func() { sender = false }}).Label("scopes")
	}, 360, 250)
	check(t, "scope geometry", near(found(tt, "scopes").H, m.GroupHeight()+2*(m.ScopeHeight()+th.Metrics().Control().BorderWidth())))
	if err := tt.Click("This chat"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Public posts"); err != nil {
		t.Fatal(err)
	}
	check(t, "scope menu commits stable id", selected == "public" && tt.HasText("Public posts"))
	if err := tt.Click("From Rin"); err != nil {
		t.Fatal(err)
	}
	check(t, "sender activates separately", requested == 1 && sender)
	if err := tt.Click("Cancel sender"); err != nil {
		t.Fatal(err)
	}
	check(t, "sender cancel preserves scope", !sender && selected == "public" && !tt.HasText("From Rin"))
	if err := tt.Click("Cancel scope"); err != nil {
		t.Fatal(err)
	}
	check(t, "scope cancel removes scope row", selected == "" && !tt.HasText("Public posts"))
}
