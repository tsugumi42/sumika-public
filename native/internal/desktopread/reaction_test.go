package desktopread

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// react 在 conv 里给 target 贴一个表情（跟 channel/intake_notice.go 存的通知同一个形状）。obs 是看到这条通知的角色
func react(t *testing.T, st *store.Store, conv, target, actorKind, actorID, ref string, at int64, obs []string) {
	t.Helper()
	body := map[string]any{"notice": "reaction", "actor": actorID, "msg_id": "p1"}
	if ref != "" {
		body["ref"] = ref
	}
	if _, e := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: actorKind, ActorID: actorID, Kind: "notice", Text: actorID + " 回应了一条消息", CreatedAt: at, ReplyTo: &target, Segments: body}, obs); e != nil {
		t.Fatal(e)
	}
}

func reactionsOf(t *testing.T, s *Service, conv, id string) []api.ReactionView {
	t.Helper()
	var cursor api.Cursor
	for {
		p, e := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 2}, cursor)
		if e != nil {
			t.Fatal(e)
		}
		for _, m := range p.Value.Items {
			if m.Ref.Event.EventID == id {
				return m.Reactions
			}
		}
		if !p.Value.HasMore {
			t.Fatalf("翻完了也没有 %s", id)
		}
		cursor = p.Value.NextCursor
	}
}

// 表情回应：一条消息上的回应按时间排、认人按号、名字查名册（主人固定叫"主人"）；
// 看不见的（没人观察）、没有表情编号的、别的种类的通知、别的会话里指向同一个 id 的，都不算。
// 通知都比被贴的消息新，被贴的消息在第二、三页（每页 2 条），也要带上回应
func TestReactionsOnMessage(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	if _, e := qq.Events.Exec(`INSERT INTO qq_people(uid,nickname,cards_json) VALUES ('90003','编造昵称','{"90002":"编造群名片"}'),('90004','只有昵称','{}')`); e != nil {
		t.Fatal(e)
	}
	conv, other := "qq:group:90002", "qq:group:90009"
	target := add(t, qq, conv, "message", "human", 100, []string{"a"})
	add(t, qq, other, "message", "human", 101, []string{"a"})
	react(t, qq, conv, target, "human", "qq:90003", "76", 110, []string{"a"})
	react(t, qq, conv, target, "owner", "owner", "76", 140, []string{"a"}) // 先入库、时间靠后：排序按时间不按入库先后
	react(t, qq, conv, target, "human", "qq:90004", "76", 120, []string{"a"})
	react(t, qq, conv, target, "human", "qq:90003", "128077", 130, []string{"b"}) // 另一个角色观察到的也算
	react(t, qq, conv, target, "human", "qq:90003", "5", 150, nil)                // 没人观察到：不算
	react(t, qq, conv, target, "human", "qq:90003", "", 160, []string{"a"})       // 没有表情编号：认不出是哪个
	react(t, qq, other, target, "human", "qq:90003", "9", 170, []string{"a"})     // 别的会话里指向这个 id：不算
	if _, e := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:90003", Kind: "notice", CreatedAt: 180, ReplyTo: &target, Segments: map[string]any{"notice": "poke", "ref": "7"}}, []string{"a"}); e != nil {
		t.Fatal(e) // 戳一戳也有 reply_to 和 ref：不能算回应
	}
	got := reactionsOf(t, s, conv, target)
	var brief [][4]string
	for _, r := range got {
		brief = append(brief, [4]string{r.Ref, r.ActorID, r.DisplayName, r.ActorKind})
	}
	want := [][4]string{
		{"76", "qq:90003", "编造群名片", "human"},
		{"76", "qq:90004", "只有昵称", "human"},
		{"128077", "qq:90003", "编造群名片", "human"},
		{"76", "owner", "主人", "owner"},
	}
	if !reflect.DeepEqual(brief, want) {
		t.Fatalf("得 %v\n应为 %v", brief, want)
	}
	for i := 1; i < len(got); i++ {
		if got[i].At < got[i-1].At || got[i].ID == "" {
			t.Fatalf("应按时间排且带通知 id：%+v", got)
		}
	}
	if r := reactionsOf(t, s, conv, add(t, qq, conv, "message", "human", 200, []string{"a"})); len(r) != 0 {
		t.Fatalf("没人贴的消息不该有回应：%+v", r)
	}
}

// 订阅：打开以后才来的回应，下一份快照里被贴的那条带着它，版本比上一份新
func TestReactionArrivesInSnapshot(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	target := add(t, qq, conv, "message", "human", 1, []string{"a"})
	sub, e := s.WatchConversation(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	first := next(t, sub)
	if len(first.Value.Page.Items) != 1 || len(first.Value.Page.Items[0].Reactions) != 0 {
		t.Fatalf("起点不对：%+v", first.Value.Page.Items)
	}
	react(t, qq, conv, target, "human", "qq:90003", "76", 2, []string{"a"})
	second := next(t, sub)
	for _, list := range [][]api.MessageView{second.Value.Page.Items, second.Value.Changed} {
		found := false
		for _, m := range list {
			if m.Ref.Event.EventID == target {
				found = true
				if len(m.Reactions) != 1 || m.Reactions[0].Ref != "76" || m.Version <= first.Value.Page.Items[0].Version {
					t.Fatalf("回应没带上或版本没涨：%+v（上一份版本 %d）", m, first.Value.Page.Items[0].Version)
				}
			}
		}
		if !found {
			t.Fatal("快照里没有被贴的那条")
		}
	}
}

// 一条消息最多读 maxReactionsPerMessage 个：留最早的，超出的不读
func TestReactionsCapPerMessage(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	target := add(t, qq, conv, "message", "human", 1, []string{"a"})
	for i := range 520 {
		react(t, qq, conv, target, "human", fmt.Sprintf("qq:%d", 100000+i), "76", int64(10+i), []string{"a"})
	}
	got := reactionsOf(t, s, conv, target)
	if len(got) != 500 || got[0].ActorID != "qq:100000" || got[499].ActorID != "qq:100499" {
		t.Fatalf("应留最早的 500 个：%d 个，首 %v 尾 %v", len(got), got[0].ActorID, got[len(got)-1].ActorID)
	}
}
