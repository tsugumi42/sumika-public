package desktopread

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/api"
)

// Close 要等订阅的 worker 退出才返回，之后的调用一律拒绝，不再查库。
func TestCloseStopsQueriesBeforeStoresClose(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	add(t, qq, conv, "message", "human", 1, []string{"a"})
	sub, err := s.WatchConversation(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	next(t, sub)
	overview, err := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close 没等到订阅退出")
	}
	for _, ch := range []<-chan api.Snapshot[api.ConversationSnapshot]{sub.Updates()} {
		for range ch {
		}
	}
	for range overview.Updates() {
	}
	if _, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 10}, ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("关了以后还能查：%v", err)
	}
	if _, err := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 10}); !errors.Is(err, ErrClosed) {
		t.Fatalf("关了以后还能订阅：%v", err)
	}
	s.Close()
	sub.Close()
	overview.Close()
}

func TestCharacterFailureListedSeparately(t *testing.T) {
	t.Parallel()
	_, set := fixture(t)
	s := NewResults(set, []api.Result[api.CharacterView]{
		{State: api.Ready, Value: api.CharacterView{ID: "a", Name: "编造甲"}},
		{State: api.Failed, Value: api.CharacterView{ID: "b", Name: "b"}, Err: errors.New("character.yml 有误")},
	}, nil)
	sub, err := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	v := next(t, sub)
	items := v.Value.Characters.Items
	if len(items) != 2 || items[0].State != api.Ready || items[1].State != api.Failed || items[1].Err == nil {
		t.Fatalf("角色：%+v", items)
	}
}
