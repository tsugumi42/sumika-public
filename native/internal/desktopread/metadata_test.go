package desktopread

import (
	"context"
	"sync"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

func TestRecallBetweenPageAndLoadedRefresh(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	id := add(t, qq, conv, "message", "human", 1, []string{"a"})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.afterPageRead = func(ctx context.Context) {
		once.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	}
	sub, e := s.WatchConversation(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	<-entered
	if _, e = qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "90001", Kind: "notice", CreatedAt: 2, ReplyTo: &id, Segments: map[string]any{"notice": "recall"}}, []string{"a"}); e != nil {
		t.Fatal(e)
	}
	close(release)
	v := next(t, sub)
	foundPage, foundChanged := false, false
	for _, m := range v.Value.Page.Items {
		if m.Ref.Event.EventID == id {
			foundPage = true
			if !m.Recalled {
				t.Fatal("同版本的最新页仍携带旧撤回元数据")
			}
		}
	}
	for _, m := range v.Value.Changed {
		if m.Ref.Event.EventID == id {
			foundChanged = true
			if !m.Recalled {
				t.Fatal("区间重查没有读取提交后的撤回")
			}
		}
	}
	if !foundPage || !foundChanged {
		t.Fatal("快照丢失已加载身份")
	}
}
