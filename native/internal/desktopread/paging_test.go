package desktopread

import (
	"context"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

func TestConversationRebuildAndOldPageVersions(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	id := add(t, qq, conv, "message", "human", 1, []string{"a"})
	req := api.ConversationRequest{Key: keyOf(conv), Limit: 200}
	old, e := s.ReadMessages(context.Background(), req, "")
	if e != nil {
		t.Fatal(e)
	}
	// A 的请求卡在查询入口，离开 A 时取消；B 和重建的 A 使用各自的订阅寿命。
	s.queryGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	entered := make(chan struct{})
	go func() { close(entered); _, e := s.ReadMessages(ctx, req, ""); result <- e }()
	<-entered
	cancel()
	<-s.queryGate
	if e = <-result; e == nil {
		t.Fatal("旧 A 取消后仍交付")
	}
	b, e := s.WatchConversation(context.Background(), api.ConversationRequest{Key: keyOf("qq:group:90003"), Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	if v := next(t, b); v.Value.Conversation.Key.Conversation != "qq:group:90003" {
		t.Fatal("B 身份被 A 覆盖")
	}
	b.Close()
	if _, e = qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "90001", Kind: "notice", CreatedAt: 2, ReplyTo: &id, Segments: map[string]any{"notice": "recall"}}, []string{"a"}); e != nil {
		t.Fatal(e)
	}
	a, e := s.WatchConversation(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	fresh := next(t, a)
	a.Close()
	current := map[api.EventKey]api.MessageView{}
	merge := func(items []api.MessageView) {
		for _, m := range items {
			existing, ok := current[m.Ref.Event]
			if !ok || m.Version >= existing.Version {
				current[m.Ref.Event] = m
			}
		}
	}
	merge(fresh.Value.Page.Items)
	merge(fresh.Value.Changed)
	merge(old.Value.Items)
	m := current[api.EventKey{Platform: "qq", EventID: id}]
	if !m.Recalled || m.Version <= old.Value.Items[0].Version {
		t.Fatalf("旧页倒退撤回状态: %+v", m)
	}
}
