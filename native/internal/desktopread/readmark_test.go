package desktopread

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

func unreadOf(t *testing.T, s *Service, conv string) int {
	t.Helper()
	sub, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	for _, c := range next(t, sub).Value.Conversations.Items {
		if c.Key.Conversation == conv {
			return c.Unread
		}
	}
	t.Fatal("概览里没有会话 " + conv)
	return -1
}

func readAtOf(t *testing.T, st *store.Store, conv string) (int64, bool) {
	t.Helper()
	var at int64
	e := st.Events.QueryRow(`SELECT read_at FROM qq_read WHERE conversation=?`, conv).Scan(&at)
	if e != nil {
		return 0, false
	}
	return at, true
}

// 标已读：往前记、不超过真有的最新一条、不倒退；没往前就不写不通知
func TestMarkReadForwardOnlyAndClamped(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	s.WithReadMark(true)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	if _, e := qq.Events.Exec(`INSERT INTO qq_read (conversation, read_at) VALUES (?, ?)`, conv, 10); e != nil { // 像导入来的旧行
		t.Fatal(e)
	}
	for _, at := range []int64{11, 12, 13} {
		add(t, qq, conv, "message", "human", at, []string{"a"})
	}
	key := keyOf(conv)
	if got := unreadOf(t, s, conv); got != 3 {
		t.Fatalf("标之前应有 3 条未读，实际 %d", got)
	}
	// 读到 12：只剩 13 一条
	if e := s.MarkRead(context.Background(), key, 12); e != nil {
		t.Fatal(e)
	}
	if at, _ := readAtOf(t, qq, conv); at != 12 {
		t.Fatalf("read_at=%d，应为 12", at)
	}
	if got := unreadOf(t, s, conv); got != 1 {
		t.Fatalf("读到 12 以后应剩 1 条，实际 %d", got)
	}
	// 往回标：不动
	if e := s.MarkRead(context.Background(), key, 11); e != nil {
		t.Fatal(e)
	}
	if at, _ := readAtOf(t, qq, conv); at != 12 {
		t.Fatalf("往回标把 read_at 改成了 %d", at)
	}
	// 传得比真有的最新一条还大（界面出错或伪造）：只记到 13，将来才到的消息不会被提前算成已读
	if e := s.MarkRead(context.Background(), key, 1<<50); e != nil {
		t.Fatal(e)
	}
	if at, _ := readAtOf(t, qq, conv); at != 13 {
		t.Fatalf("超大的 upTo 应夹到最新一条 13，实际 %d", at)
	}
	add(t, qq, conv, "message", "human", 14, []string{"a"})
	if got := unreadOf(t, s, conv); got != 1 {
		t.Fatalf("标完以后到的新消息应算未读，实际 %d", got)
	}
	// 0、负数：不写
	for _, bad := range []int64{0, -5} {
		if e := s.MarkRead(context.Background(), key, bad); e != nil {
			t.Fatal(e)
		}
	}
	if at, _ := readAtOf(t, qq, conv); at != 13 {
		t.Fatalf("0 和负数改了 read_at：%d", at)
	}
}

// 没有已读行（本进程基线）的会话，标一次就建行；之后的新消息从这里算
func TestMarkReadCreatesRow(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	s.WithReadMark(true)
	qq, _ := set.Platform("qq")
	conv := "qq:private:90001:90003"
	add(t, qq, conv, "message", "human", 5, []string{"a"})
	if got := unreadOf(t, s, conv); got != 0 { // 基线：本进程开始前的都算读过
		t.Fatalf("基线应为 0，实际 %d", got)
	}
	if _, ok := readAtOf(t, qq, conv); ok {
		t.Fatal("读路径建了行")
	}
	add(t, qq, conv, "message", "human", 6, []string{"a"})
	add(t, qq, conv, "message", "human", 7, []string{"a"})
	if got := unreadOf(t, s, conv); got != 2 {
		t.Fatalf("应有 2 条新的，实际 %d", got)
	}
	if e := s.MarkRead(context.Background(), keyOf(conv), 7); e != nil {
		t.Fatal(e)
	}
	if at, ok := readAtOf(t, qq, conv); !ok || at != 7 {
		t.Fatalf("应建行 read_at=7，实际 %d %v", at, ok)
	}
	if got := unreadOf(t, s, conv); got != 0 {
		t.Fatalf("标完应为 0，实际 %d", got)
	}
}

// 概览订阅在标完以后自己收到新快照（会话列表刷新靠这个，界面不手动刷）
func TestMarkReadWakesOverview(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	s.WithReadMark(true)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	if _, e := qq.Events.Exec(`INSERT INTO qq_read (conversation, read_at) VALUES (?, ?)`, conv, 1); e != nil {
		t.Fatal(e)
	}
	add(t, qq, conv, "message", "human", 2, []string{"a"})
	sub, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 200})
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	count := func(v api.Snapshot[api.Overview]) int {
		for _, c := range v.Value.Conversations.Items {
			if c.Key.Conversation == conv {
				return c.Unread
			}
		}
		return -1
	}
	if got := count(next(t, sub)); got != 1 {
		t.Fatalf("起点应有 1 条未读，实际 %d", got)
	}
	if e := s.MarkRead(context.Background(), keyOf(conv), 2); e != nil {
		t.Fatal(e)
	}
	if got := count(next(t, sub)); got != 0 {
		t.Fatalf("标完以后推来的快照应为 0，实际 %d", got)
	}
	// 没往前走：不写、不通知（再等一会儿不该有新快照）
	if e := s.MarkRead(context.Background(), keyOf(conv), 2); e != nil {
		t.Fatal(e)
	}
	select {
	case v := <-sub.Updates():
		t.Fatalf("没有变化却推了快照：%+v", v.Value.Conversations.Items)
	case <-time.After(300 * time.Millisecond):
	}
}

// 不标的情形：archive（没打开）、本地会话、别人的 key、没有观察的陌生人会话。库里的表前后一样
func TestMarkReadRefusals(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	local, _ := set.Platform("local")
	conv := "qq:group:90002"
	add(t, qq, conv, "message", "human", 5, []string{"a"})
	stranger := "qq:group:90009"
	add(t, qq, stranger, "message", "human", 5, nil) // 没有任何角色观察过：不在可见范围
	before := dumpTables(t, qq.Events)
	beforeLocal := dumpTables(t, local.Events)

	// archive：没打开，库是只读打开的，约定也不写
	if e := s.MarkRead(context.Background(), keyOf(conv), 5); !errors.Is(e, api.ErrReadMarkUnsupported) {
		t.Fatalf("没打开应回 ErrReadMarkUnsupported，实际 %v", e)
	}
	s.WithReadMark(true)
	// 本地会话没有已读记录
	if e := s.MarkRead(context.Background(), keyOf("console:a"), 5); !errors.Is(e, api.ErrReadMarkUnsupported) {
		t.Fatalf("本地会话应回 ErrReadMarkUnsupported，实际 %v", e)
	}
	// 拿一个对不上的 key（场合写错、平台写错、会话号不认得）
	wrong := keyOf(conv)
	wrong.Scene = "private"
	if e := s.MarkRead(context.Background(), wrong, 5); !errors.Is(e, store.ErrDisplayScope) {
		t.Fatalf("对不上的 key 应报范围错误，实际 %v", e)
	}
	if e := s.MarkRead(context.Background(), api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:"}, 5); e == nil {
		t.Fatal("空群号的会话居然标成功了")
	}
	// 没人观察过的会话：没有可见消息，什么也不写
	if e := s.MarkRead(context.Background(), keyOf(stranger), 5); e != nil {
		t.Fatalf("不可见的会话应静默不写，实际 %v", e)
	}
	if after := dumpTables(t, qq.Events); !reflect.DeepEqual(before, after) {
		t.Fatal("拒绝的情形改了 QQ 库")
	}
	if after := dumpTables(t, local.Events); !reflect.DeepEqual(beforeLocal, after) {
		t.Fatal("拒绝的情形改了本地库")
	}
	// 服务关了以后不再碰库
	s.Close()
	if e := s.MarkRead(context.Background(), keyOf(conv), 5); !errors.Is(e, ErrClosed) {
		t.Fatalf("关了以后应回 ErrClosed，实际 %v", e)
	}
}

// 标已读只改 qq_read 的那一行，别的表一个字不动
func TestMarkReadTouchesOnlyQQRead(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	s.WithReadMark(true)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	add(t, qq, conv, "message", "human", 5, []string{"a"})
	before := dumpTables(t, qq.Events)
	if e := s.MarkRead(context.Background(), keyOf(conv), 5); e != nil {
		t.Fatal(e)
	}
	after := dumpTables(t, qq.Events)
	for name := range after {
		if name != "qq_read" && before[name] != after[name] {
			t.Fatalf("标已读改了表 %s", name)
		}
	}
	if before["qq_read"] == after["qq_read"] {
		t.Fatal("qq_read 没变")
	}
}
