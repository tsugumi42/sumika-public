package store

import (
	"bytes"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/paths"
)

func fresh(t *testing.T) (*Store, paths.Paths) {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, p
}

func TestFreshMatchesBaseline(t *testing.T) {
	t.Parallel()
	s, p := fresh(t)
	if err := s.CheckSchema(); err != nil {
		t.Fatal(err)
	}
	ev, tr, err := Baseline()
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 21 || len(tr) != 10 {
		// 2026-10-25 从真实库导出时 events.db 21 张表、trace.db 10 张（表 + 索引一共 30、16 项）
		t.Fatalf("基线的表数变了：events %d、trace %d", len(ev), len(tr))
	}
	var mode string
	s.Events.QueryRow("PRAGMA journal_mode").Scan(&mode)
	var av int
	s.Events.QueryRow("PRAGMA auto_vacuum").Scan(&av)
	if mode != "wal" || av != 2 {
		t.Fatalf("PRAGMA 和 daemon/util/db.ts 不一样：journal_mode=%s auto_vacuum=%d", mode, av)
	}
	// 再打开一次：迁移已经跑过，不重复建
	s.Close()
	s2, err := OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestCheckSchemaCatchesMissingColumn(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	// 故意弄坏：删掉 events.origin（Bun 版后加的列），检查要报出来
	if _, err := s.Events.Exec("ALTER TABLE events DROP COLUMN origin"); err != nil {
		t.Fatal(err)
	}
	err := s.CheckSchema()
	if err == nil || !strings.Contains(err.Error(), "events.events.origin") {
		t.Fatalf("缺列应该报出 events.events.origin，得到 %v", err)
	}
}

func TestReadOnlyAndCount(t *testing.T) {
	t.Parallel()
	s, p := fresh(t)
	for i, conv := range []string{"console:a", "console:a", "qq:group:1"} {
		if _, err := s.Events.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at, origin) VALUES (?, 'x', ?, 'owner', 'owner', 'message', '你好', 0, 'owner')`, i, conv); err != nil {
			t.Fatal(err)
		}
	}
	s.Events.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at) VALUES ('n', 'x', 'console:a', 'system', 's', 'notice', '', 0)`)
	s.Close()
	ro, err := OpenReadOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Events.Exec(`DELETE FROM events`); err == nil {
		t.Fatal("只读打开却能写")
	}
	c, err := ro.Count()
	if err != nil {
		t.Fatal(err)
	}
	if c.Events != 4 || c.Messages != 3 || c.Conversations != 2 || c.PerConversation["console:a"] != 2 || c.PerConversation["qq:group:1"] != 1 {
		t.Fatalf("条数不对：%+v", c)
	}
}

// 目录名带 # 和 % 时只读也要成立：不转义的话 SQLite 把 # 后面当片段截掉，mode=ro 跟着丢，
// 打开的还是截断后的路径（不存在就新建一个空库）
func TestReadOnlyOddPath(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	p, err := paths.Init(filepath.Join(base, "a#b%41c"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	before := listAll(t, base)
	ro, err := OpenReadOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.CheckSchema(); err != nil {
		t.Fatalf("打开的不是那个库：%v", err)
	}
	if _, err := ro.Events.Exec(`DELETE FROM events`); err == nil {
		t.Fatal("只读打开却能写")
	}
	if _, err := ro.Trace.Exec(`DELETE FROM model_runs`); err == nil {
		t.Fatal("trace.db 只读打开却能写")
	}
	ro.Close()
	if after := listAll(t, base); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("只读打开多出或少了文件：\n之前 %v\n之后 %v", before, after)
	}
}

func listAll(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !strings.HasSuffix(path, "-wal") && !strings.HasSuffix(path, "-shm") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// OpenFresh 只给测试和临时目录用：那里已经有一个不是 Go 建的库（Bun 的，没有 goose 的版本表）就拒绝打开，
// 而且一个字节都不改它；Go 自己建过的（关了再开）照常能开
func TestOpenFreshRefusesBunDB(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.SharedDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	bun, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p.BunEventsDB()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bun.Exec(`CREATE TABLE events (seq INTEGER PRIMARY KEY, text TEXT)`); err != nil {
		t.Fatal(err)
	}
	bun.Close()
	before, err := os.ReadFile(p.BunEventsDB())
	if err != nil {
		t.Fatal(err)
	}
	if st, err := OpenFresh(p); err == nil {
		st.Close()
		t.Fatal("那里已经有 Bun 的库，OpenFresh 却打开了")
	}
	after, err := os.ReadFile(p.BunEventsDB())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("拒绝打开以后 Bun 的库被改了")
	}

	// Go 自己建过的：关了再开照常
	q, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenFresh(q)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = OpenFresh(q)
	if err != nil {
		t.Fatalf("Go 自己建的库关了再开该照常：%v", err)
	}
	st.Close()
}
