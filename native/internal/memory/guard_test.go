package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"sumika-native/internal/dbguard"
)

// 主人共用记忆库第一次建：走护栏建基础表、跑 memory 轨、写版本表。
func TestOwnerPoolGuardCreatesVersionTable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "shared", "owner-memory.db")
	pool, err := OpenOwnerPool(ctx, file, nil, nil, Options{
		Writable: true, SnapshotDir: filepath.Join(dir, "go", "upgrade-snapshots"), SnapshotRel: "shared/owner-memory.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	version, minReader, by := schemaRow(t, file, "memory")
	if version != 3 || minReader != 3 || by == "" {
		t.Fatalf("版本表是 version=%d min_reader=%d migrated_by=%q，要 3/3/非空", version, minReader, by)
	}
	if got := gooseVersion(t, file); got != 3 {
		t.Fatalf("goose 版本是 %d，要 3", got)
	}
}

// Bun 时代留下的库（有基础表、没有 goose 表）第一次以可写方式打开：拍快照、迁移、写版本表，行数不变。
func TestOwnerPoolGuardMigratesBunEraDB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "shared", "owner-memory.db")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	seedOwnerPool(t, file)
	before := tableCounts(t, file, "facts")
	snapDir := filepath.Join(dir, "go", "upgrade-snapshots")
	pool, err := OpenOwnerPool(ctx, file, nil, nil, Options{
		Writable: true, SnapshotDir: snapDir, SnapshotRel: "shared/owner-memory.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if got := tableCounts(t, file, "facts"); got != before {
		t.Fatalf("迁移后 facts 有 %d 行，迁移前 %d", got, before)
	}
	version, minReader, _ := schemaRow(t, file, "memory")
	if version != 3 || minReader != 3 {
		t.Fatalf("版本表是 version=%d min_reader=%d，要 3/3", version, minReader)
	}
	snaps := snapshotsIn(t, snapDir, "shared-owner-memory.db-memory-v0-to-v3-")
	if len(snaps) != 1 || strings.Contains(snaps[0], "-failed") {
		t.Fatalf("迁移前快照是 %v，要一份不带 -failed 的", snaps)
	}
}

// 迁移失败：库换回迁移前（表结构、每表行数、goose 和版本表都相同），错误里带 -failed 快照路径。
func TestOwnerPoolGuardMigrationFailureRestores(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "shared", "owner-memory.db")
	snapDir := filepath.Join(dir, "go", "upgrade-snapshots")
	pool, err := OpenOwnerPool(ctx, file, nil, nil, Options{
		Writable: true, SnapshotDir: snapDir, SnapshotRel: "shared/owner-memory.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	before := logicalState(t, file)

	old := migrationFS
	migrationFS = fstest.MapFS{"migrations/0004_bad.sql": &fstest.MapFile{
		Data: []byte("-- sumika: compatible\nALTER TABLE no_such_table ADD COLUMN x;\n"),
	}}
	defer func() { migrationFS = old }()

	if _, err := OpenOwnerPool(ctx, file, nil, nil, Options{
		Writable: true, SnapshotDir: snapDir, SnapshotRel: "shared/owner-memory.db",
	}); err == nil {
		t.Fatal("失败的迁移没报错")
	} else {
		var u *dbguard.Unavailable
		if !errors.As(err, &u) || u.Reason != dbguard.ReasonMigrateFailed {
			t.Fatalf("错误是 %v，要 migrate_failed", err)
		}
		if !strings.HasSuffix(u.Snapshot, "-failed.sqlite") {
			t.Fatalf("快照是 %s，要带 -failed", u.Snapshot)
		}
		if !strings.Contains(err.Error(), "快照在") || !strings.Contains(err.Error(), u.Snapshot) {
			t.Fatalf("错误没说清快照在哪：%v", err)
		}
	}
	if after := logicalState(t, file); !equalState(before, after) {
		t.Fatalf("库没换回迁移前：\n前 %v\n后 %v", before, after)
	}
}

// 角色库的版本表说 min_reader 999：以只读方式打开也不行，错误说“较新的 Sumika 升级过”。
func TestOpenTooNewMemoryDB(t *testing.T) {
	file := newBunDB(t, nil)
	mustExec(t, file, `CREATE TABLE sumika_schema (track TEXT PRIMARY KEY, version INTEGER NOT NULL, min_reader INTEGER NOT NULL,
		migrated_by TEXT NOT NULL, migrated_at TEXT NOT NULL);
		INSERT INTO sumika_schema VALUES ('memory', 3, 999, 'future', '2026-01-01T00:00:00Z');`)
	_, err := Open(context.Background(), file, nil, nil, Options{})
	if err == nil {
		t.Fatal("太新的记忆库被打开了")
	}
	var u *dbguard.Unavailable
	if !errors.As(err, &u) || u.Reason != dbguard.ReasonTooNew {
		t.Fatalf("错误是 %v，要 too_new", err)
	}
	if !strings.Contains(err.Error(), "较新的 Sumika") {
		t.Fatalf("错误没说被较新的 Sumika 升级过：%v", err)
	}
}

// 旁路索引的版本表说 memory-index 的 min_reader 999：这个角色的记忆按太新处理，不自动删掉重建。
func TestOpenTooNewSidecar(t *testing.T) {
	file := migratedDB(t, nil)
	side := SidecarPath(file)
	mustExec(t, side, `UPDATE sumika_schema SET min_reader = 999 WHERE track = 'memory-index'`)
	_, err := Open(context.Background(), file, nil, nil, Options{})
	if err == nil {
		t.Fatal("太新的旁路索引被打开了")
	}
	var u *dbguard.Unavailable
	if !errors.As(err, &u) || u.Reason != dbguard.ReasonTooNew {
		t.Fatalf("错误是 %v，要 too_new", err)
	}
	if !strings.Contains(err.Error(), "较新的 Sumika") {
		t.Fatalf("错误没说被较新的 Sumika 升级过：%v", err)
	}
	if _, statErr := os.Stat(side); statErr != nil {
		t.Fatalf("太新的旁路索引被删了：%v", statErr)
	}
}

func seedOwnerPool(t *testing.T, file string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(BaseSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO facts (id, kind, text, visibility, created_at) VALUES ('F1', 'fact', '编的事实', 'private', 1)`); err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, file, q string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func schemaRow(t *testing.T, file, track string) (version, minReader int64, by string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT version, min_reader, migrated_by FROM sumika_schema WHERE track = ?`, track).Scan(&version, &minReader, &by); err != nil {
		t.Fatal(err)
	}
	return version, minReader, by
}

func gooseVersion(t *testing.T, file string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int64
	if err := db.QueryRow(`SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func tableCounts(t *testing.T, file, table string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int64
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// logicalState 是“换回原样”的可比状态：每张用户表的行数，加 goose 和版本表。
func logicalState(t *testing.T, file string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	sort.Strings(tables)
	out := map[string]string{}
	for _, table := range tables {
		var n int64
		if err := db.QueryRow(`SELECT count(*) FROM ` + quoteTable(table)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out["table "+table] = fmt.Sprint(n)
	}
	out["goose"] = fmt.Sprint(gooseVersion(t, file))
	v, minReader, _ := schemaRow(t, file, "memory")
	out["memory.version"] = fmt.Sprint(v)
	out["memory.min_reader"] = fmt.Sprint(minReader)
	return out
}

func equalState(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func quoteTable(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func snapshotsIn(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), ".sqlite") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// 已有的库只有一张外人表：可写打开报错，不拍快照、主文件一个字节不变（复核 K10 同类）。
func TestOwnerPoolRefusesForeignDBWithoutSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "shared", "owner-memory.db")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	mustExec(t, file, `CREATE TABLE foreign_t (a TEXT); INSERT INTO foreign_t VALUES ('别人的')`)
	before := fileHash(t, file)
	snapDir := filepath.Join(dir, "go", "upgrade-snapshots")
	pool, err := OpenOwnerPool(ctx, file, nil, nil, Options{
		Writable: true, SnapshotDir: snapDir, SnapshotRel: "shared/owner-memory.db",
	})
	if pool != nil {
		pool.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "不像 Bun") {
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

// 共用库的迁移轨比程序新、但还能读：照兼容版本打开，日志说一行。
func TestOwnerPoolNewerTrackLogs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "shared", "owner-memory.db")
	opt := Options{Writable: true, SnapshotDir: filepath.Join(dir, "go", "upgrade-snapshots"), SnapshotRel: "shared/owner-memory.db"}
	pool, err := OpenOwnerPool(ctx, file, nil, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	// 较新的程序迁到 4、最低可读仍写 3：本程序 3 能读，不迁移、不改版本表
	mustExec(t, file, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (4, 1);
		UPDATE sumika_schema SET version = 4, min_reader = 3 WHERE track = 'memory'`)

	var logs []string
	opt.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	pool, err = OpenOwnerPool(ctx, file, nil, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	var newer []string
	for _, line := range logs {
		if strings.Contains(line, "比程序新") {
			newer = append(newer, line)
		}
	}
	if len(newer) != 1 || !strings.Contains(newer[0], "memory") || !strings.Contains(newer[0], "按兼容版本打开") {
		t.Fatalf("较新能读的日志是 %v，要一行说清 memory 轨、按兼容版本打开", newer)
	}
	if version, minReader, _ := schemaRow(t, file, "memory"); version != 4 || minReader != 3 {
		t.Fatalf("较新能读时改了版本表：version=%d min_reader=%d", version, minReader)
	}
}
