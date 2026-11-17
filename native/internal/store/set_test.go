package store

import (
	"database/sql"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/paths"
)

// goSet 开一组 local + qq 的库（临时目录），用完关掉
func goSet(t *testing.T) (*Set, paths.Paths) {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func TestPlatformOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ channel, want string }{{"console", "local"}, {"qq", "qq"}} {
		if got, err := PlatformOf(c.channel); err != nil || got != c.want {
			t.Fatalf("%s 的平台：%q %v", c.channel, got, err)
		}
	}
	// 没登记的渠道报错，不许"取不到就当本地"；错误文字要说清是哪个渠道
	for _, c := range []string{"telegram", "console2", ""} {
		_, err := PlatformOf(c)
		if err == nil {
			t.Fatalf("渠道 %q 没登记，却给了平台", c)
		}
		if c != "" && !strings.Contains(err.Error(), c) {
			t.Fatalf("错误文字没说清渠道 %q：%v", c, err)
		}
	}
}

func TestPlatformOfConversation(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ conv, want string }{
		{"console:tester", "local"},
		{"qq:group:400", "qq"},
		{"qq:private:100:200", "qq"},
	} {
		if got, err := PlatformOfConversation(c.conv); err != nil || got != c.want {
			t.Fatalf("%s 的平台：%q %v", c.conv, got, err)
		}
	}
	for _, conv := range []string{"telegram:group:1", "nocolon", ":group:1", ""} {
		if got, err := PlatformOfConversation(conv); err == nil {
			t.Fatalf("会话 %q 取不出平台，却给了 %q", conv, got)
		}
	}
}

func TestValidPlatform(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"local", "qq", "telegram", "we-chat", "x1", "a", strings.Repeat("a", 32)} {
		if !ValidPlatform(name) {
			t.Fatalf("%q 该能用", name)
		}
	}
	for _, name := range []string{"", "Local", "QQ", "a.b", "a/b", "..", "a b", "a_1", "本地", strings.Repeat("a", 33),
		"chat", "events", "trace", "con", "nul", "aux", "prn", "com1", "lpt9"} { // 后两行：迁移目录名、Windows 设备名
		if ValidPlatform(name) {
			t.Fatalf("%q 不该能用", name)
		}
	}
}

// tablesOf 是一个库里的表名（不含 sqlite_ 开头的）
func tablesOf(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOpenGoLayout(t *testing.T) {
	t.Parallel()
	s, p := goSet(t)
	for _, f := range []string{p.ChatDB("local"), p.ChatDB("qq"), p.GoTraceDB()} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("没建出 %s：%v", f, err)
		}
	}
	local, _ := s.Platform("local")
	qq, _ := s.Platform("qq")
	// 验平台边界，不固定通用表全集；新增通用迁移不改变隔离约束。
	for name, db := range map[string]*sql.DB{"local": local.Events, "qq": qq.Events} {
		for table := range tablesOf(t, db) {
			if name == "local" && strings.HasPrefix(table, "qq_") || name == "qq" && strings.HasPrefix(table, "local_") {
				t.Fatalf("%s 库混入别的平台专表 %s", name, table)
			}
		}
	}
	// PRAGMA 和 OpenFresh 一样，trace 也是（auto_vacuum 要在建表之前设，量的是同一个文件；busy_timeout、foreign_keys 在连接串里）
	for name, db := range map[string]*sql.DB{"local": local.Events, "qq": qq.Events, "trace": s.Trace()} {
		var mode string
		var av, limit, fk, busy int
		for q, dst := range map[string]any{"PRAGMA journal_mode": &mode, "PRAGMA auto_vacuum": &av, "PRAGMA journal_size_limit": &limit,
			"PRAGMA foreign_keys": &fk, "PRAGMA busy_timeout": &busy} {
			if err := db.QueryRow(q).Scan(dst); err != nil {
				t.Fatalf("%s：%s：%v", name, q, err)
			}
		}
		if mode != "wal" || av != 2 || limit != 4194304 || fk != 1 || busy != 5000 {
			t.Fatalf("%s 库的 PRAGMA 不对：journal_mode=%s auto_vacuum=%d journal_size_limit=%d foreign_keys=%d busy_timeout=%d", name, mode, av, limit, fk, busy)
		}
	}
}

func TestOpenGoEventsPerPlatform(t *testing.T) {
	t.Parallel()
	s, _ := goSet(t)
	local, _ := s.Platform("local")
	qq, _ := s.Platform("qq")
	for _, x := range []struct {
		st           *Store
		channel      string
		conversation string
		text         string
	}{
		{local, "console", "console:tester", "本地的话"},
		{qq, "qq", "qq:group:400", "群里的话"},
	} {
		seq, err := x.st.AppendEvent(NewEvent{
			Channel: x.channel, Conversation: x.conversation, ActorKind: "human", ActorID: "owner",
			Origin: "owner", Kind: "message", Text: x.text, CreatedAt: 1000,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if seq != 1 { // 每个库各从 1 起
			t.Fatalf("%s 的第一条 seq 是 %d", x.conversation, seq)
		}
	}
	for _, x := range []struct {
		st   *Store
		conv string
		text string
	}{
		{local, "console:tester", "本地的话"},
		{qq, "qq:group:400", "群里的话"},
	} {
		r, ok, err := x.st.EventRow(x.conv, 1)
		if err != nil || !ok || r.Text != x.text {
			t.Fatalf("%s 按（会话, seq）取：%+v %v %v", x.conv, r, ok, err)
		}
	}
	// 拿 QQ 的会话去本地库查：seq 也有 1，但不是这个会话的
	if _, ok, err := local.EventRow("qq:group:400", 1); err != nil || ok {
		t.Fatalf("本地库上查 QQ 会话：ok=%v err=%v", ok, err)
	}
}

func TestSetFor(t *testing.T) {
	t.Parallel()
	s, _ := goSet(t)
	local, _ := s.Platform("local")
	qq, _ := s.Platform("qq")
	if st, err := s.For("console"); err != nil || st != local {
		t.Fatalf("console 该给本地库：%v", err)
	}
	if st, err := s.For("qq"); err != nil || st != qq {
		t.Fatalf("qq 该给 QQ 库：%v", err)
	}
	if _, err := s.For("telegram"); err == nil {
		t.Fatal("没登记的平台该报错")
	}
	if st, err := s.ForConversation("qq:group:400"); err != nil || st != qq {
		t.Fatalf("按会话取：%v", err)
	}
	if local.Trace != qq.Trace || local.Trace != s.Trace() {
		t.Fatal("两个平台该共用同一个 trace 连接")
	}
	if got := s.Platforms(); !reflect.DeepEqual(got, []string{"local", "qq"}) {
		t.Fatalf("平台列表：%v", got)
	}
	var st Stores = s
	if one, err := st.For("qq"); err != nil || one != qq {
		t.Fatalf("Set 该实现 Stores：%v", err)
	}
}

func TestSetForUnopenedPlatform(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.For("qq"); err == nil {
		t.Fatal("这组只开了本地，QQ 该报错")
	}
	if one, err := s.For("console"); err != nil || one == nil {
		t.Fatalf("本地该能用：%v", err)
	}
}

func TestSetClose(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	local, _ := s.Platform("local")
	qq, _ := s.Platform("qq")
	// 成员关不掉任何东西：共用的 trace 只能由 Set.Close 关
	local.Close()
	qq.Close()
	if err := s.Trace().Ping(); err != nil {
		t.Fatalf("成员的 Close 动了共用的 trace：%v", err)
	}
	if _, err := local.AppendEvent(NewEvent{Channel: "console", Conversation: "console:a", ActorKind: "human", ActorID: "owner",
		Origin: "owner", Kind: "message", Text: "还在", CreatedAt: 1}, nil); err != nil {
		t.Fatalf("成员 Close 以后平台库该还能写：%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关两次该不报错：%v", err)
	}
	// 真的关了：每个平台库、共用的 trace 都不能再用
	for name, db := range map[string]*sql.DB{"local": local.Events, "qq": qq.Events, "trace": local.Trace} {
		if err := db.Ping(); err == nil {
			t.Fatalf("Set.Close 以后 %s 库还能用：没关", name)
		}
	}
	// 关了以后不再给出库（给出去也是关掉的连接）
	if _, err := s.For("console"); err == nil {
		t.Fatal("关了以后 For 还给出了库")
	}
	if _, err := s.ForConversation("qq:group:400"); err == nil {
		t.Fatal("关了以后 ForConversation 还给出了库")
	}
	if _, ok := s.Platform("local"); ok {
		t.Fatal("关了以后 Platform 还给出了库")
	}
}

// 关机时还有协程在取库：取库和关同时进行不能有数据竞争。这个测试没有断言，只在 -race 下有意义
// （合并前 native/ 里跑 go test -race ./internal/store/ 一次）
func TestSetCloseWhileFor(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = s.For("qq")
				_, _ = s.Platform("local")
			}
		}()
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestOpenGoReopen(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	qq, _ := s.Platform("qq")
	if _, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: "qq:group:400", ActorKind: "human", ActorID: "owner",
		Origin: "owner", Kind: "message", Text: "先写的", CreatedAt: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("再开一次该不报错（迁移不重跑）：%v", err)
	}
	defer s2.Close()
	qq2, _ := s2.Platform("qq")
	var n int
	if err := qq2.Events.QueryRow("SELECT count(*) FROM events").Scan(&n); err != nil || n != 1 {
		t.Fatalf("之前写的事件没了：%d %v", n, err)
	}
	if r, ok, err := qq2.EventRow("qq:group:400", 1); err != nil || !ok || r.Text != "先写的" {
		t.Fatalf("再开一次取不到那条：%+v %v %v", r, ok, err)
	}
}

func TestSingle(t *testing.T) {
	t.Parallel()
	s, _ := goSet(t)
	qq, _ := s.Platform("qq")
	st := Single(qq)
	for _, ch := range []string{"qq", "console"} {
		if got, err := st.For(ch); err != nil || got != qq {
			t.Fatalf("Single.For(%q) 该给那一个库：%v", ch, err)
		}
	}
	if got, err := st.ForConversation("console:tester"); err != nil || got != qq {
		t.Fatalf("Single.ForConversation 该给那一个库：%v", err)
	}
	// 没登记的渠道、会话照样报错，和 Set 一样（不然用 Single 的测试连"渠道没登记"都抓不到）
	if _, err := st.For("telegram"); err == nil {
		t.Fatal("Single.For 对没登记的渠道该报错")
	}
	for _, conv := range []string{"telegram:group:1", "nocolon"} {
		if _, err := st.ForConversation(conv); err == nil {
			t.Fatalf("Single.ForConversation(%q) 该报错", conv)
		}
	}
	if e := st.Each(); len(e) != 1 || e[0].Store != qq {
		t.Fatalf("Single.Each = %+v，要只有那一个库", e)
	}
	if st.TraceStore() != qq {
		t.Fatal("Single.TraceStore 该就是那一个库")
	}
}

// Each 按平台名排好序给出每个库；TraceStore 只带共用的 trace，不借哪个平台的库
func TestSetEachAndTraceStore(t *testing.T) {
	t.Parallel()
	s, _ := goSet(t)
	local, _ := s.Platform("local")
	qq, _ := s.Platform("qq")
	if e := s.Each(); len(e) != 2 || e[0] != (Named{"local", local}) || e[1] != (Named{"qq", qq}) {
		t.Fatalf("Each = %+v，要 local、qq 两个，按名字排", e)
	}
	ts := s.TraceStore()
	if ts == nil || ts.Trace != s.Trace() || ts.Events != nil {
		t.Fatalf("TraceStore 该只带共用的 trace：%+v", ts)
	}
	if ts == local || ts == qq {
		t.Fatal("TraceStore 不该是某个平台的库")
	}
	ts.Close() // 它也是成员：Close 什么都不关
	if err := s.Trace().Ping(); err != nil {
		t.Fatalf("TraceStore 的 Close 动了共用的 trace：%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if e := s.Each(); len(e) != 0 {
		t.Fatalf("关了以后 Each 还给出了 %d 个库", len(e))
	}
}

func TestOpenGoRejectsBadArgumentsAndStopsBadPlatform(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGo(p); err == nil {
		t.Fatal("一个平台都没给该报错")
	}
	if _, err := OpenGo(p, "../etc"); err == nil {
		t.Fatal("非法平台名该报错")
	}
	if _, err := OpenGo(p, "local", "local"); err == nil {
		t.Fatal("重复的平台该报错")
	}
	if _, err := os.Stat(p.ChatDir()); err == nil {
		t.Fatal("参数还没过就建了目录")
	}
	// 让 QQ 库打不开（把 go/chat/qq.db 先建成一个目录）：这是 *dbguard.Unavailable（探查失败），
	// 只停 qq，本地和 trace 照常打开。
	if err := os.MkdirAll(p.ChatDB("qq"), 0o755); err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("qq.db 是个目录只该停 qq：%v", err)
	}
	if got := set.Platforms(); len(got) != 1 || got[0] != "local" {
		t.Fatalf("qq.db 是目录时打开的平台是 %v，该只有 local", got)
	}
	if u, ok := set.Unavailable()["qq"]; !ok || u.Reason != dbguard.ReasonProbeFailed {
		t.Fatalf("qq 没记成探查失败：%+v", u)
	}
	// 关干净了才删得掉：Windows 上留着打开的库文件就删不动
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p.ChatDB("local"), p.GoTraceDB()} {
		if err := os.Remove(f); err != nil {
			t.Fatalf("关掉以后还留着打开的 %s：%v", f, err)
		}
	}
}
