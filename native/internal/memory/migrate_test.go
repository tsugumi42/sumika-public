package memory

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMigrateBunShaped(t *testing.T) {
	t.Parallel()
	file := newBunDB(t, func(db *sql.DB) {
		for _, q := range []string{
			`INSERT INTO facts (id, kind, text, visibility, status, created_at, scope) VALUES ('F1', 'fact', '甲', 'private', 'active', 1, 'owner')`,
			`INSERT INTO facts (id, kind, text, visibility, status, created_at, scope) VALUES ('F2', 'fact', '乙（极老的数据，没写范围）', 'private', 'active', 1, NULL)`,
			`INSERT INTO facts (id, kind, text, visibility, status, created_at, scope) VALUES ('F3', 'fact', '丙', 'public', 'revoked', 1, 'conv:qq:group:30001')`,
			`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope) VALUES ('E1', 'console:a', '一段经历', 'private', 1, 5, 1, 'owner')`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err := Migrate(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	m, _ := openW(t, file, nil)

	// 范围组从单值的 scope 列回填：所有状态的事实、所有片段；没写范围的按最严的 owner
	got := map[string]string{}
	rows, err := m.w.db.Query(`SELECT ref, scope FROM item_scopes ORDER BY ref`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ref, scope string
		rows.Scan(&ref, &scope)
		got[ref] = scope
	}
	rows.Close()
	want := map[string]string{"f:F1": "owner", "f:F2": "owner", "f:F3": "conv:qq:group:30001", "e:E1": "owner"}
	if len(got) != len(want) {
		t.Fatalf("范围组 %v，要 %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s 的范围是 %q，要 %q", k, got[k], v)
		}
	}
	if g, _ := m.Generation(context.Background()); g != 0 {
		t.Fatalf("世代号起点应该是 0：%d", g)
	}
	// 原来的行一个字没动
	if n := count(t, m, `SELECT count(*) FROM facts`); n != 3 {
		t.Fatalf("事实数变了：%d", n)
	}
	if s := str(t, m, `SELECT text FROM facts WHERE id = 'F2'`); s != "乙（极老的数据，没写范围）" {
		t.Fatalf("正文变了：%q", s)
	}
	m.Close()

	// 重跑不变：不报错，库的内容一个字节都没动（范围组不会再回填一遍）
	before := fileHash(t, file)
	if err := Migrate(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if fileHash(t, file) != before {
		t.Fatal("重跑迁移改了库")
	}

	// Bun 版的写法（只写它认识的列）在迁移以后的库上照样能写：新加的列都有默认值，Bun 切换以前如果还在写这个库不会被拦
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO facts (id, kind, text, subject, visibility, source_seqs, status, created_at, scope, learned_at) VALUES ('F9', 'fact', '丁', 'owner', 'private', '[]', 'active', 2, 'owner', 2)`,
		`INSERT INTO fact_revisions (fact_id, op, at, by, reason) VALUES ('F9', 'add', 2, 'summary', NULL)`,
		`INSERT INTO memory_jobs (conversation, from_seq, to_seq, version, done_at) VALUES ('console:a', 1, 5, 1, 2)`,
		`INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES ('console:a', 5, 2)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("Bun 版的写法在迁移后的库上写不进去：%v\n%s", err, q)
		}
	}
}

func TestMigrateRefusesWhatIsNotBunMemoryDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// 缺 Bun 的表：不迁移，库里一样多出来的东西都没有（goose 自己的版本表也不能建）
	other := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(other))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE notes (id TEXT)`)
	db.Close()
	err = Migrate(ctx, other)
	if err == nil || !strings.Contains(err.Error(), "facts") {
		t.Fatalf("缺表的库应该被拒绝并说缺 facts：%v", err)
	}
	db, _ = sql.Open("sqlite", "file:"+filepath.ToSlash(other))
	var n int
	db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&n)
	db.Close()
	if n != 1 {
		t.Fatalf("拒绝以后库里还剩 %d 张表，应该只有原来的 1 张", n)
	}

	// 缺一列：说明是哪一列
	short := newBunDB(t, nil)
	db, _ = sql.Open("sqlite", "file:"+filepath.ToSlash(short))
	db.Exec(`ALTER TABLE episodes DROP COLUMN status`)
	db.Close()
	if err := Migrate(ctx, short); err == nil || !strings.Contains(err.Error(), "episodes.status") {
		t.Fatalf("缺列应该点名 episodes.status：%v", err)
	}

	// 文件不存在：报错，也不新建（B17 的教训）
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope", "memory.db")
	if err := Migrate(ctx, missing); err == nil {
		t.Fatal("不存在的文件应该报错")
	}
	if _, err := os.Stat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("迁移不该建出目录")
	}
	if err := Migrate(ctx, dir); err == nil {
		t.Fatal("目录应该报错")
	}
}

func TestOpenWritableNeedsMigration(t *testing.T) {
	t.Parallel()
	file := newBunDB(t, nil)
	before := fileHash(t, file)
	_, err := Open(context.Background(), file, nil, nil, Options{Writable: true, Character: "suzune"})
	if !errors.Is(err, ErrNeedsMigration) {
		t.Fatalf("没迁移的库以可写方式打开应该是 ErrNeedsMigration：%v", err)
	}
	if fileHash(t, file) != before {
		t.Fatal("Open 不该自己迁移：库被改了")
	}
	entries, _ := os.ReadDir(filepath.Dir(file))
	for _, e := range entries {
		if strings.Contains(e.Name(), "memory-v2") {
			t.Fatalf("没打开成功就不该留下旁边的文件：%s", e.Name())
		}
	}
	// 只读打开没迁移的库照常（Bun 还在写它，Go 只读召回）
	m, err := Open(context.Background(), file, nil, nil, Options{})
	if err != nil {
		t.Fatalf("只读打开没迁移的库应该可以：%v", err)
	}
	m.Close()
}

func TestOpenCharacterIDB17(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, nil)
	dir := filepath.Dir(file)
	before := dirNames(t, dir)
	for _, id := range []string{"../escape", "..\\escape", "a/b", "C:x", "..", "", "a\x00b"} {
		for _, writable := range []bool{true, false} {
			if id == "" && !writable {
				continue // 只读时角色 id 可以不写
			}
			m, err := Open(context.Background(), file, nil, nil, Options{Writable: writable, Character: id})
			if err == nil {
				m.Close()
				t.Fatalf("角色 id %q（Writable=%v）应该被拒绝", id, writable)
			}
			if !strings.Contains(err.Error(), "角色 id 不对") {
				t.Fatalf("角色 id %q：报错应该说角色 id 不对：%v", id, err)
			}
		}
	}
	if after := dirNames(t, dir); !slices.Equal(before, after) {
		t.Fatalf("被拒绝的 id 不该在别处建文件：之前 %v 之后 %v", before, after)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("../escape 被建出来了")
	}
	m, err := Open(context.Background(), file, nil, nil, Options{Writable: true, Character: "suzune"})
	if err != nil {
		t.Fatalf("正常的 id 应该能开：%v", err)
	}
	m.Close()
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestOpenWritableDoesNotCreateFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "memory.db")
	if _, err := Open(context.Background(), missing, nil, nil, Options{Writable: true, Character: "suzune"}); err == nil {
		t.Fatal("库不存在，以可写方式打开应该报错")
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("不该建出任何文件：%v", names)
	}
}

// Writable 为假：写入口都拒绝，库一个字节都不变（Bun 还在写它，库只能有一个写的人）
func TestWritableOffRefusesEveryWrite(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, nil)
	m, err := Open(context.Background(), file, nil, nil, Options{Character: "suzune"})
	if err != nil {
		t.Fatal(err)
	}
	before := fileHash(t, file)
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["apply"] = m.apply(ctx, summary(ownerFact("不会写进去")))
	_, _, checks["SummaryState"] = m.SummaryState(ctx, "console:a")
	_, checks["JobDone"] = m.JobDone(ctx, "console:a", 1, 2, 1)
	_, checks["InitCursor"] = m.InitCursor(ctx, "console:a", 5)
	_, checks["Generation"] = m.Generation(ctx)
	_, checks["BumpGeneration"] = m.BumpGeneration(ctx)
	_, checks["BackfillVectors"] = m.BackfillVectors(ctx)
	for name, err := range checks {
		if !errors.Is(err, ErrNotWritable) {
			t.Errorf("%s 在 Writable 为假时应该返回 ErrNotWritable：%v", name, err)
		}
	}
	if m.EmbeddingDegraded() {
		t.Error("没开写入侧不该报降级")
	}
	m.Close()
	if fileHash(t, file) != before {
		t.Fatal("Writable 为假时库被改了")
	}
}

// MigrateGuarded 碰到外人的库：不迁移、不拍快照、主文件一个字节不变（复核 K10）。
func TestMigrateGuardedLeavesForeignDBAlone(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "memory.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE notes (id TEXT); INSERT INTO notes VALUES ('别人的')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	before := fileHash(t, file)
	snapDir := filepath.Join(dir, "snaps")
	if _, err := MigrateGuarded(context.Background(), file, snapDir, "characters/编造/memory.db"); err == nil || !strings.Contains(err.Error(), "不像 Bun") {
		t.Fatalf("外人的库该报错并说清：%v", err)
	}
	if after := fileHash(t, file); after != before {
		t.Fatalf("拒绝时改了库：%s → %s", before, after)
	}
	entries, err := os.ReadDir(snapDir)
	switch {
	case err == nil && len(entries) > 0:
		t.Fatalf("拒绝时拍了快照：%v", entries)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		t.Fatal(err)
	}
}
