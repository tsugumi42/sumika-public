package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/paths"
)

func linkStore(t *testing.T) *Store {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	st, _ := set.For("qq")
	return st
}

func snapshotCount(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM link_snapshots`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func readLink(t *testing.T, st *Store, conv string, seq int64) string {
	t.Helper()
	b, found, err := st.MessageLink(context.Background(), conv, seq, linkClean)
	if err != nil || !found {
		t.Fatalf("%s/%d found=%v err=%v", conv, seq, found, err)
	}
	return string(b)
}

const linkClean = "https://x.com/i/status/90001"

func TestLinkSnapshotsShared(t *testing.T) {
	t.Parallel()
	st, ctx := linkStore(t), context.Background()
	a, b := []byte(`{"text":"甲"}`), []byte(`{"text":"乙"}`)
	for seq := int64(1); seq <= 20; seq++ {
		for _, conv := range []string{"qq:group:90001", "qq:group:90002"} {
			if _, err := st.RecordLink(ctx, conv, seq, linkClean, a); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := snapshotCount(t, st); n != 1 {
		t.Fatalf("同一份结果存了 %d 份", n)
	}
	// 缓存刷新以后的新结果只给新消息，旧消息还是当时那份
	if _, err := st.RecordLink(ctx, "qq:group:90001", 21, linkClean, b); err != nil {
		t.Fatal(err)
	}
	if got := readLink(t, st, "qq:group:90001", 1); got != string(a) {
		t.Fatal("旧消息的详情被改了", got)
	}
	if got := readLink(t, st, "qq:group:90001", 21); got != string(b) {
		t.Fatal("新消息的详情不对", got)
	}
	if n := snapshotCount(t, st); n != 2 {
		t.Fatalf("快照数 %d，要 2", n)
	}
}

func TestLinkSnapshotFillAndReplay(t *testing.T) {
	t.Parallel()
	st, ctx := linkStore(t), context.Background()
	conv := "qq:private:90001:90002"
	if _, err := st.RecordLink(ctx, conv, 1, linkClean, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.MessageLink(ctx, conv, 1, linkClean); err != nil || !found {
		t.Fatal("取数失败的关联也要在", found, err)
	}
	if b, _, _ := st.MessageLink(ctx, conv, 1, linkClean); b != nil {
		t.Fatal("失败的关联不能有详情", string(b))
	}
	if n := snapshotCount(t, st); n != 0 {
		t.Fatal("失败的关联存了快照", n)
	}
	a := []byte(`{"text":"甲"}`)
	if _, err := st.RecordLink(ctx, conv, 1, linkClean, a); err != nil {
		t.Fatal(err)
	}
	if got := readLink(t, st, conv, 1); got != string(a) {
		t.Fatal("补齐详情", got)
	}
	// 重放带来另一份结果：不改详情，也不留没人引用的快照
	if _, err := st.RecordLink(ctx, conv, 1, linkClean, []byte(`{"text":"乙"}`)); err != nil {
		t.Fatal(err)
	}
	if got := readLink(t, st, conv, 1); got != string(a) {
		t.Fatal("重放改了详情", got)
	}
	if n := snapshotCount(t, st); n != 1 {
		t.Fatal("重放留下孤儿快照", n)
	}
}

// 0004 时期详情存在 message_links 行里，升级以后照旧读得到，重放也不另起快照
func TestLinkSnapshotLegacyRow(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.ChatDB("qq")), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := open(p.ChatDB("qq"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, "migrations/chat", 4); err != nil {
		t.Fatal(err)
	}
	old := `{"text":"旧"}`
	if _, err := db.Exec(`INSERT INTO message_links VALUES('qq:group:90001',1,?,0,?)`, linkClean, old); err != nil {
		t.Fatal(err)
	}
	db.Close()
	set, err := OpenGo(p, "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	st, _ := set.For("qq")
	if got := readLink(t, st, "qq:group:90001", 1); got != old {
		t.Fatal("旧行读不到", got)
	}
	previous, err := st.RecordLink(context.Background(), "qq:group:90001", 1, linkClean, []byte(`{"text":"新"}`))
	if err != nil || previous != 0 {
		t.Fatal(previous, err)
	}
	if got := readLink(t, st, "qq:group:90001", 1); got != old {
		t.Fatal("重放盖掉了旧详情", got)
	}
	var snap any
	if err := st.Events.QueryRow(`SELECT snapshot FROM message_links`).Scan(&snap); err != nil || snap != nil || snapshotCount(t, st) != 0 {
		t.Fatal("旧行被挂上了快照", snap, err)
	}
}

// LinkNotes 只给这个角色看到过的、这个会话的；指定 seq 只给那一条；同一条消息里按处理的先后；失败的 Result 为空
func TestLinkNotesVisibility(t *testing.T) {
	t.Parallel()
	st, ctx := linkStore(t), context.Background()
	g, other := "qq:group:90001", "qq:group:90002"
	ev := func(seq int64, conv string, seen ...string) {
		if _, err := st.Events.Exec(`INSERT INTO events(seq,id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			seq, "e"+string(rune('a'+seq)), "qq", conv, "user", "30001", "message", "看", `{"segs":[],"from":{"id":"30001","card":"编的名片"}}`, 1000*seq); err != nil {
			t.Fatal(err)
		}
		for _, c := range seen {
			if _, err := st.Events.Exec(`INSERT INTO observations VALUES(?,?)`, c, seq); err != nil {
				t.Fatal(err)
			}
		}
	}
	ev(1, g, "ca")
	ev(2, g, "cb")
	ev(3, other, "ca")
	second := "https://www.bilibili.com/video/BV1xx411c7mD"
	for _, r := range []struct {
		conv  string
		seq   int64
		clean string
		body  []byte
	}{{g, 1, linkClean, []byte(`{"a":1}`)}, {g, 1, second, nil}, {g, 2, linkClean, []byte(`{"a":1}`)}, {other, 3, linkClean, []byte(`{"a":1}`)}} {
		if _, err := st.RecordLink(ctx, r.conv, r.seq, r.clean, r.body); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.LinkNotes(ctx, g, "ca", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[0].CleanURL != linkClean || string(got[0].Result) != `{"a":1}` || got[1].CleanURL != second || got[1].Result != nil {
		t.Fatalf("ca 在本群只看得到第 1 条的两个链接：%+v", got)
	}
	if got[0].CreatedAt != 1000 || !got[0].Segments.Valid {
		t.Fatalf("要带上时间和发言人：%+v", got[0])
	}
	if n, _ := st.LinkNotes(ctx, g, "ca", 2, 0); len(n) != 0 {
		t.Fatalf("cb 看到的那条 ca 读不到：%+v", n)
	}
	if n, _ := st.LinkNotes(ctx, g, "cb", 2, 0); len(n) != 1 || n[0].Previous != 1 {
		t.Fatalf("cb 读第 2 条，之前发过 1 次：%+v", n)
	}
	if n, _ := st.LinkNotes(ctx, g, "ca", 3, 0); len(n) != 0 {
		t.Fatalf("别的会话的 seq 读不到：%+v", n)
	}
}
