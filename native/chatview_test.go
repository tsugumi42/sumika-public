package main

import (
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"testing"
)

func TestChatColorRoundTrip(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"#000000", "#ffffff", "#3377bb", "#f80", "#12af", "#80706040"} {
		got := chatColor(text).Color()
		want := chatColor(text)
		back := chatOKLCH(got)
		if d := back.L - want.L; d < -0.005 || d > 0.005 {
			t.Fatalf("%s lightness roundtrip: %v -> %v", text, want, back)
		}
		if got != back.Color() {
			t.Fatalf("%s unstable sRGB roundtrip: %v -> %v", text, got, back.Color())
		}
	}
	if got := chatColor("#3377bb").Color(); got.R != 0x33 || got.G != 0x77 || got.B != 0xbb || got.A != 255 {
		t.Fatal(got)
	}
	for _, bad := range []string{"", "red", "#nope", "#12", "123456", "#12345g"} {
		if got := chatColor(bad); got != (chatui.OKLCH{}) {
			t.Fatalf("%q must use fallback: %+v", bad, got)
		}
	}
}

func TestChatMessageProjection(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"local", "qq"} {
		for _, scene := range []string{"private", "group"} {
			k := api.ConversationKey{Platform: platform, Scene: scene, Conversation: "test-conversation"}
			for _, actor := range []string{"owner", "character", "human"} {
				v := view("sample", actor, "编造名字", "正文", t0)
				v.Ref.Conversation = k
				v.Segments = []api.DisplaySegment{{Kind: "text", Text: "正文"}, {Kind: "at", Target: "all"}}
				m := msgFromView(k, v, func(string) (character, bool) { return character{Name: "编造角色", Color: "#3377bb"}, true })
				got := chatMessage(m)
				if got.Self != (platform == "local" && actor == "owner" || platform == "qq" && actor == "character") {
					t.Fatalf("%s/%s/%s wrong side", platform, scene, actor)
				}
				if got.Markdown != (actor == "character") {
					t.Fatal("markdown inferred from side")
				}
				if &got.Blocks[0] != &m.blocks[0] {
					t.Fatal("second block cache")
				}
				if got.Steps != nil {
					t.Fatal("invented process data")
				}
			}
		}
	}
}

func TestChatParallelNotesAndQuotes(t *testing.T) {
	t.Parallel()
	v := quoting(view("reply", "human", "编造访客", "正文", t0), "target", "", "", true)
	v.Aborted, v.Recalled, v.Unsent = true, true, true
	m := msgFromView(v.Ref.Conversation, v, charOf)
	got := chatMessage(m)
	if got.State != chatui.Recalled || len(got.Notes) != 2 || got.Notes[0] != "被打断" || got.Notes[1] != "已撤回" {
		t.Fatal(got.State, got.Notes)
	}
	if got.Quote.Name != "某人" || got.Quote.Text != "[消息]" || got.Quote.Target != "target" || got.Quote.Gone {
		t.Fatal(got.Quote)
	}
	for _, state := range []api.State{api.Loading, api.Failed, api.Unsupported} {
		v.Reply.State = state
		m = msgFromView(v.Ref.Conversation, v, charOf)
		if !chatMessage(m).Quote.Gone {
			t.Fatalf("unavailable quote %v", state)
		}
	}
	v.ReplyTo = nil
	v.Segments = []api.DisplaySegment{{Kind: "reply"}}
	if !chatMessage(msgFromView(v.Ref.Conversation, v, charOf)).Quote.Gone {
		t.Fatal("orphan reply disappeared")
	}
	v.Recalled, v.Aborted, v.Partial = false, false, true
	if got = chatMessage(msgFromView(v.Ref.Conversation, v, charOf)); got.State != chatui.Sent || len(got.Notes) != 0 {
		t.Fatal("partial marked failed", got)
	}
}

func TestChatReactionsIdentityAndOrder(t *testing.T) {
	t.Parallel()
	rs := []api.ReactionView{
		{ID: "1", Ref: "128077", ActorKind: "human", ActorID: "a", DisplayName: "同名"},
		{ID: "2", Ref: "128077", ActorKind: "human", ActorID: "a", DisplayName: "改名"},
		{ID: "3", Ref: "14", ActorKind: "owner", ActorID: "a", DisplayName: "主人"},
		{ID: "4", Ref: "128077", ActorKind: "owner", ActorID: "a", DisplayName: "同名"},
		{ID: "5", Ref: "14"}, {ID: "5", Ref: "14"}, {ID: "6", Ref: "14"},
		{ID: "7", Ref: "55296", ActorKind: "human", ActorID: "b"},
	}
	got := chatReactions("qq", rs)
	if len(got) != 3 || got[0].Emoji != "👍" || got[0].Count != 2 || !got[0].Mine || got[1].Face != "14" || got[1].Count != 3 || got[2].Face != "55296" {
		t.Fatalf("%+v", got)
	}
	if got[0].By[0].Name != "同名" || got[0].By[0].ID == got[0].By[1].ID {
		t.Fatal(got[0].By)
	}
}
