package desktopread

import (
	"context"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

func TestQuoteCannotReadAnotherConversation(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	target := add(t, qq, "qq:private:90001:90003", "message", "human", 1, []string{"a"})
	conv := "qq:group:90002"
	if _, e := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "90001", Kind: "message", Text: "编造引用", CreatedAt: 2, ReplyTo: &target}, []string{"a"}); e != nil {
		t.Fatal(e)
	}
	page, e := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 200}, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Value.Items) != 1 {
		t.Fatal("引用事件缺失")
	}
	m := page.Value.Items[0]
	if m.ReplyTo == nil || m.ReplyTo.Event.EventID != target || m.Reply.State != api.Unsupported || m.Reply.Value.Text != "" {
		t.Fatalf("跨会话引用泄露: %+v", m.Reply)
	}
}

func TestQuoteMentionsProjectedOutsidePage(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	st, _ := set.Platform("qq")
	if _, err := st.Events.Exec(`INSERT INTO qq_people(uid,nickname,cards_json) VALUES ('10001','编的昵称','{"90002":"编的群名片"}')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tc := range []struct{ conv, want string }{{"qq:group:90002", "编的群名片"}, {"qq:private:90003:90004", "编的昵称"}} {
		t.Run(tc.conv, func(t *testing.T) {
			seq, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: tc.conv, Kind: "message", ActorKind: "human", ActorID: "qq:10003", Text: "@10001 红气泡", CreatedAt: 1, Segments: map[string]any{"segs": []types.Seg{{Kind: types.SegAt, Target: "10001"}, {Kind: types.SegText, Text: " 红气泡"}}}}, []string{"a"})
			if err != nil {
				t.Fatal(err)
			}
			var target string
			if err := st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&target); err != nil {
				t.Fatal(err)
			}
			if _, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: tc.conv, Kind: "message", ActorKind: "human", ActorID: "qq:10004", Text: "编的回复", CreatedAt: 2, ReplyTo: &target}, []string{"a"}); err != nil {
				t.Fatal(err)
			}
			page, err := s.ReadMessages(ctx, api.ConversationRequest{Key: keyOf(tc.conv), Limit: 1}, "")
			if err != nil {
				t.Fatal(err)
			}
			q := page.Value.Items[0].Reply
			if len(page.Value.Items) != 1 || q.State != api.Ready || len(q.Value.Segments) != 2 || q.Value.Segments[0].Name != tc.want || q.Value.Segments[0].Target != "10001" {
				t.Fatalf("页外引用丢失段或名字：%+v", q)
			}
			full, err := s.ReadMessages(ctx, api.ConversationRequest{Key: keyOf(tc.conv), Limit: 10}, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range full.Value.Items {
				if m.Ref.Event.EventID == target && m.Segments[0].Name != q.Value.Segments[0].Name {
					t.Fatal("正文和引用取名不同")
				}
			}
		})
	}
	brief := forwardBrief(store.ForwardRecord{Tree: types.ForwardTree{Nodes: []types.ForwardMessageNode{{Name: "编的甲", Segs: []types.Seg{{Kind: types.SegAt, Target: "10001"}, {Kind: types.SegAt, Target: types.AtAll}, {Kind: types.SegAt, Target: "伪造名字"}}}}}})
	if brief.Lines[0] != "编的甲：@***01@全体成员@某人" {
		t.Fatal(brief.Lines[0])
	}
}
