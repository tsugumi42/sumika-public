package desktopread

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func fixture(t *testing.T) (*Service, *store.Set) {
	t.Helper()
	p, e := paths.Init(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	set, e := store.OpenGo(p, "local", "qq")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { set.Close() })
	return New(set, []api.CharacterView{{ID: "a", Name: "同名"}, {ID: "b", Name: "同名"}}, nil), set
}
func add(t *testing.T, st *store.Store, conv, kind, actor string, at int64, obs []string) string {
	t.Helper()
	seq, e := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, Kind: kind, ActorKind: actor, ActorID: "90001", Text: "编造正文", CreatedAt: at}, obs)
	if e != nil {
		t.Fatal(e)
	}
	var id string
	if e = st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}
func next[T any](t *testing.T, sub api.Subscription[T]) api.Snapshot[T] {
	t.Helper()
	select {
	case v := <-sub.Updates():
		if v.Err != nil {
			t.Fatal(v.Err)
		}
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("等待快照超时")
		return api.Snapshot[T]{}
	}
}
func TestDirectAPIVisibilityAndPaging(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	want := []string{add(t, qq, conv, "message", "human", 2, []string{"a", "b"}), add(t, qq, conv, "notice", "human", 2, []string{"b"}), add(t, qq, conv, "message", "human", 1, []string{"a"})}
	add(t, qq, conv, "message", "human", 3, nil)
	req := api.ConversationRequest{Key: keyOf(conv), Limit: 1}
	var got []string
	var cursor api.Cursor
	for {
		p, e := s.ReadMessages(context.Background(), req, cursor)
		if e != nil {
			t.Fatal(e)
		}
		for _, m := range p.Value.Items {
			got = append(got, m.Ref.Event.EventID)
		}
		if !p.Value.HasMore {
			break
		}
		cursor = p.Value.NextCursor
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for _, bad := range []api.ConversationRequest{{Key: keyOf(conv), Character: "foreign", Limit: 200}, {Key: keyOf("console:foreign"), Limit: 200}, {Key: api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: "home:cabin"}, Limit: 200}} {
		if _, e := s.ReadMessages(context.Background(), bad, ""); e == nil {
			t.Fatalf("越权请求获准: %+v", bad)
		}
	}
}
func TestSubscriptionBurstReplay(t *testing.T) {
	t.Parallel()
	newFixture := fixtureFactory(t)
	for _, n := range []int{65, 1000} {
		// 整套（巡检）每种 20 遍；-short（合并前、推送前）2 遍（ADR-225）
		repeats := 20
		if testing.Short() {
			repeats = 2
		}
		for repeat := range repeats {
			t.Run(fmt.Sprintf("%d/%d", n, repeat), func(t *testing.T) {
				t.Parallel()
				s, set := newFixture(t)
				qq, _ := set.Platform("qq")
				conv := "qq:group:90002"
				sub, e := s.WatchConversation(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 200})
				if e != nil {
					t.Fatal(e)
				}
				defer sub.Close()
				initial := next(t, sub)
				seen := map[string]bool{}
				for i := range n {
					add(t, qq, conv, "message", "human", int64(n-i), []string{"a"})
				}
				var version = initial.Version
				for len(seen) < n {
					v := next(t, sub)
					if v.Version <= version {
						t.Fatal("版本未递增")
					}
					version = v.Version
					for _, m := range v.Value.Page.Items {
						seen[m.Ref.Event.EventID] = true
					}
					for _, m := range v.Value.Changed {
						seen[m.Ref.Event.EventID] = true
					}
				}
				rows, e := qq.Events.Query(`SELECT id FROM events e WHERE e.conversation=? AND EXISTS (SELECT 1 FROM observations o WHERE o.event_seq=e.seq AND o.character_id IN ('a','b'))`, conv)
				if e != nil {
					t.Fatal(e)
				}
				expected := map[string]bool{}
				for rows.Next() {
					var id string
					if e = rows.Scan(&id); e != nil {
						t.Fatal(e)
					}
					expected[id] = true
				}
				rows.Close()
				if !reflect.DeepEqual(seen, expected) {
					t.Fatalf("最终集合不同 got=%d want=%d", len(seen), len(expected))
				}
				sub.Close()
				sub.Close()
				if _, ok := <-sub.Updates(); ok {
					t.Fatal("取消后仍发布")
				}
			})
		}
	}
}
func TestUnreadReadOnlyBaselines(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	with := "qq:group:90002"
	missing := "qq:group:90003"
	if _, e := qq.Events.Exec(`INSERT INTO qq_read(conversation,read_at) VALUES (?,10)`, with); e != nil {
		t.Fatal(e)
	}
	add(t, qq, with, "message", "human", 10, []string{"a"})
	add(t, qq, with, "message", "character", 11, []string{"a"})
	add(t, qq, with, "notice", "human", 11, []string{"a"})
	add(t, qq, with, "message", "human", 11, nil)
	for range 101 {
		add(t, qq, with, "message", "human", 11, []string{"a"})
	}
	add(t, qq, missing, "message", "human", 20, []string{"a"})
	// 任一表的写入都由临时审计触发器拒绝，包括建立 qq_read 行。
	tables, e := qq.Events.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND sql NOT LIKE 'CREATE VIRTUAL TABLE%'`)
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for tables.Next() {
		var name string
		tables.Scan(&name)
		names = append(names, name)
	}
	tables.Close()
	for i, name := range names {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			_, e = qq.Events.Exec(fmt.Sprintf(`CREATE TEMP TRIGGER audit_%d_%s BEFORE %s ON "%s" BEGIN SELECT RAISE(ABORT,'只读审计发现写入'); END`, i, op, op, name))
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	sub, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	v := next(t, sub)
	sub.Close()
	got := map[string]int{}
	for _, c := range v.Value.Conversations.Items {
		if c.Key.Platform == "local" && c.HasUnread {
			t.Fatal("本地虚构未读")
		}
		got[c.Key.Conversation] = c.Unread
	}
	if got[with] != 101 || got[missing] != 0 { // 101 条未读照实数，不封顶（2026-11-02）
		t.Fatalf("未读=%v", got)
	}
	// 同一进程重建订阅仍使用缺行会话的第一次基线。
	base := s.baselines[missing+"\x00"]
	if base != 20 {
		t.Fatalf("基线=%d", base)
	}
}
