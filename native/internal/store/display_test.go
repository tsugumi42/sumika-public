package store

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestDisplayTimelineIsolation(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	qq, _ := set.Platform("qq")
	local, _ := set.Platform("local")
	ctx := context.Background()
	add := func(st *Store, conv, text string, at int64, obs []string, reply *string, kind string) DisplayRow {
		t.Helper()
		channel := "qq"
		if st == local {
			channel = "console"
		}
		seq, err := st.AppendEvent(NewEvent{Channel: channel, Conversation: conv, ActorKind: "human", ActorID: "90001", Kind: kind, Text: text, CreatedAt: at, ReplyTo: reply}, obs)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return DisplayRow{ID: id, Seq: seq}
	}
	conv := "qq:group:90002"
	first := add(qq, conv, "first", 100, []string{"a", "b"}, nil, "message")
	add(local, "console:a", "local", 100, nil, nil, "message")
	hidden := add(qq, conv, "hidden", 110, nil, nil, "message")
	second := add(qq, conv, "second", 100, []string{"b"}, &first.ID, "message")
	third := add(qq, conv, "third", 120, []string{"a"}, &hidden.ID, "message")
	old := add(qq, conv, "old", 1, []string{"a"}, nil, "message")
	recallSeq, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "90001", Kind: "notice", CreatedAt: 130, ReplyTo: &first.ID, Segments: map[string]any{"notice": "recall"}}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	var recallID string
	if err := qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, recallSeq).Scan(&recallID); err != nil {
		t.Fatal(err)
	}
	scope := DisplayScope{Platform: "qq", Conversation: conv, Characters: []string{"a", "b"}}
	want := []string{first.ID, second.ID, third.ID, old.ID, recallID}
	sort.Strings(want)
	var got []string
	cursor := ""
	for {
		page, err := qq.ReadDisplay(ctx, scope, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			got = append(got, r.ID)
			if r.ID == first.ID && !r.Recalled {
				t.Fatal("跨页撤回丢失")
			}
			if r.ID == second.ID && (r.Reply == nil || r.Reply.ID != first.ID || !r.Reply.Recalled) {
				t.Fatal("引用摘要不含跨页撤回")
			}
			if r.ID == third.ID && r.Reply != nil {
				t.Fatal("泄露未观察引用")
			}
		}
		if !page.HasMore {
			break
		}
		if cursor == page.NextCursor {
			t.Fatal("游标未前进")
		}
		cursor = page.NextCursor
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("事件身份 got=%v want=%v", got, want)
	}
	page, err := qq.ReadDisplay(ctx, scope, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	other := scope
	other.Conversation = "qq:private:90003:90001"
	if _, err := qq.ReadDisplay(ctx, other, page.NextCursor, 1); !errors.Is(err, ErrDisplayParameter) {
		t.Fatalf("跨会话游标: %v", err)
	}
	other = scope
	other.Character = "unknown"
	if _, err := qq.ReadDisplay(ctx, other, "", 1); !errors.Is(err, ErrDisplayScope) {
		t.Fatalf("越权角色: %v", err)
	}
	for _, limit := range []int{-1, 0, 1001} {
		if _, err := qq.ReadDisplay(ctx, scope, "", limit); !errors.Is(err, ErrDisplayParameter) {
			t.Fatalf("非法 limit=%d: %v", limit, err)
		}
	}
	localPage, err := local.ReadDisplay(ctx, DisplayScope{Platform: "local", Conversation: "console:a", Characters: []string{"a"}}, "", 200)
	if err != nil || len(localPage.Items) != 1 || localPage.Items[0].Seq != 1 {
		t.Fatalf("本地无观察记录: %+v %v", localPage, err)
	}
}

func TestDisplayConversationDirectoryOver200(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	ids := make([]string, 205)
	want := make(map[string]bool, 205)
	for i := range ids {
		ids[i] = strings.Repeat("a", i+1)
		want["console:"+ids[i]] = true
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := set.ReadDisplayConversations(context.Background(), ids, "", cursor, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if !want[item.Conversation] || seen[item.Conversation] || item.Last != nil {
				t.Fatalf("错误或重复目录项: %+v", item)
			}
			seen[item.Conversation] = true
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 205 {
		t.Fatalf("目录漏项: %d", len(seen))
	}
}
