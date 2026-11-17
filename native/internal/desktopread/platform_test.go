package desktopread

import (
	"context"
	"reflect"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

func TestPlatformIdentityWithEqualSequence(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	local, _ := set.Platform("local")
	qq, _ := set.Platform("qq")
	seq, e := local.AppendEvent(store.NewEvent{Channel: "console", Conversation: "console:a", ActorKind: "human", ActorID: "owner", Kind: "message", Text: "编造本地消息", CreatedAt: 1}, nil)
	if e != nil || seq != 1 {
		t.Fatalf("本地首序号=%d err=%v", seq, e)
	}
	var localID string
	if e = local.Events.QueryRow(`SELECT id FROM events WHERE seq=1`).Scan(&localID); e != nil {
		t.Fatal(e)
	}
	qqID := add(t, qq, "qq:group:90002", "message", "human", 1, []string{"a"})
	want := map[api.EventKey]bool{{Platform: "local", EventID: localID}: true, {Platform: "qq", EventID: qqID}: true}
	got := map[api.EventKey]bool{}
	for _, conv := range []string{"console:a", "qq:group:90002"} {
		req := api.ConversationRequest{Key: keyOf(conv), Limit: 200}
		page, e := s.ReadMessages(context.Background(), req, "")
		if e != nil {
			t.Fatal(e)
		}
		if len(page.Value.Items) != 1 {
			t.Fatalf("事件数 %s=%d", conv, len(page.Value.Items))
		}
		m := page.Value.Items[0]
		if m.Seq != 1 || m.Ref.Conversation != req.Key || m.Ref.Event.Platform != req.Key.Platform {
			t.Fatalf("同序号平台身份混合: %+v", m)
		}
		got[m.Ref.Event] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("事件身份集合 got=%v want=%v", got, want)
	}
}
