package desktopread

import (
	"context"
	"reflect"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

func TestObservationAppearsInsideLoadedHistory(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	old := add(t, qq, conv, "message", "human", 1, []string{"a"})
	in := store.NewInbound{Event: store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "90001", Kind: "message", CreatedAt: 2}, PlatformID: "90005", Observers: []string{"b"}}
	hidden, e := qq.AppendInbound(in)
	if e != nil {
		t.Fatal(e)
	}
	tail := add(t, qq, conv, "message", "human", 3, []string{"a"})
	req := api.ConversationRequest{Key: keyOf(conv), Character: "a", Limit: 1}
	sub, e := s.WatchConversation(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	first := next(t, sub)
	history, e := s.ReadMessages(context.Background(), req, first.Value.Page.NextCursor)
	if e != nil {
		t.Fatal(e)
	}
	if len(history.Value.Items) != 1 || history.Value.Items[0].Ref.Event.EventID != old {
		t.Fatal("历史页未加载旧行")
	}
	in.Observers = []string{"a"}
	if _, e = qq.AppendInbound(in); e != nil {
		t.Fatal(e)
	}
	v := next(t, sub)
	got := map[string]bool{}
	for _, m := range v.Value.Changed {
		got[m.Ref.Event.EventID] = true
	}
	if !reflect.DeepEqual(got, map[string]bool{old: true, hidden.EventID: true, tail: true}) {
		t.Fatalf("已加载区间漏补观察 got=%v", got)
	}
}
