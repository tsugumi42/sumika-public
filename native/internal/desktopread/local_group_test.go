package desktopread

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/localgroup"
	"sumika-native/internal/store"
)

// 走真实建群事务和桌面订阅：空群、发言身份、分页与角色过滤须一致。
func TestLocalGroupDirectoryMessagesAndOptionalWrite(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	ctx := context.Background()
	if _, err := s.CreateLocalGroup(ctx, api.LocalGroupCreate{Name: "只读时不能建", Characters: []string{"a", "b"}}); !errors.Is(err, api.ErrLocalGroupUnsupported) {
		t.Fatalf("未装写接口：%v", err)
	}
	st, _ := set.Platform("local")
	groups := localgroup.New(st, nil, []api.CharacterView{{ID: "a", Name: "同名"}, {ID: "b", Name: "同名"}}, func() time.Time { return time.UnixMilli(1000) })
	defer groups.Close()
	s.WithLocalGroups(groups)
	overview, err := s.WatchOverview(ctx, api.OverviewRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer overview.Close()
	next(t, overview)
	created, err := s.CreateLocalGroup(ctx, api.LocalGroupCreate{Name: "编造会客室", Characters: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	listed := next(t, overview)
	var found bool
	for _, v := range listed.Value.Conversations.Items {
		if v.Key == created.Key {
			found = true
			if v.Title != "编造会客室" || v.Last != nil || v.CreatedAt != 1000 || len(v.Observers) != 2 {
				t.Fatalf("空群投影：%+v", v)
			}
		}
	}
	if !found {
		t.Fatal("建完群概览订阅未出现空群")
	}
	sub, err := s.WatchConversation(ctx, api.ConversationRequest{Key: created.Key, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	initial := next(t, sub)
	if len(initial.Value.Page.Items) != 0 {
		t.Fatal("空群出现了伪造消息")
	}
	for i, actor := range []string{"owner", "a", "b"} {
		kind, origin := "character", "character"
		if actor == "owner" {
			kind, origin = "owner", "owner"
		}
		if _, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: created.Key.Conversation, ActorKind: kind, ActorID: actor, Origin: origin, Kind: "message", Text: actor + "的话", CreatedAt: int64(i + 2000)}, []string{"a", "b"}); err != nil {
			t.Fatal(err)
		}
	}
	changed := next(t, sub)
	for len(changed.Value.Page.Items) != 1 || changed.Value.Page.Items[0].ActorID != "b" {
		changed = next(t, sub)
	}
	var ids, names []string
	cursor := api.Cursor("")
	for {
		page, err := s.ReadMessages(ctx, api.ConversationRequest{Key: created.Key, Character: "a", Limit: 1}, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Value.Items {
			ids = append(ids, m.ActorID)
			names = append(names, m.DisplayName)
		}
		if !page.Value.HasMore {
			break
		}
		cursor = page.Value.NextCursor
	}
	if !reflect.DeepEqual(ids, []string{"b", "a", "owner"}) || !reflect.DeepEqual(names, []string{"同名", "同名", "主人"}) {
		t.Fatalf("角色 ID 不随名字合并：%v %v", ids, names)
	}
	bad := created.Key
	bad.Scene = "private"
	if _, err := s.ReadMessages(ctx, api.ConversationRequest{Key: bad, Limit: 10}, ""); !errors.Is(err, store.ErrDisplayScope) {
		t.Fatalf("伪造场合放行：%v", err)
	}
	outsider := New(set, []api.CharacterView{{ID: "c", Name: "旁观者"}}, nil)
	defer outsider.Close()
	if _, err := outsider.ReadMessages(ctx, api.ConversationRequest{Key: created.Key, Limit: 10}, ""); !errors.Is(err, store.ErrDisplayScope) {
		t.Fatalf("非成员可读：%v", err)
	}
}
