package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

// 第二个新建的库来自模板：表结构、迁移记录和自己迁移出来的一样，两个库互不影响。
func TestFreshDatabaseFromTemplateMatchesMigrated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := openGo(filepath.Join(dir, "a.db"), "migrations/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if migratedTemplate("migrations/events", "") == nil {
		t.Fatal("第一个新建的库迁移完该留下模板")
	}
	second, err := openGo(filepath.Join(dir, "b.db"), "migrations/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	a, err := ColumnsOf(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ColumnsOf(second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("模板拷出来的库和自己迁移的库表结构不一样")
	}
	var v1, v2 int64
	if err := first.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&v1); err != nil {
		t.Fatal(err)
	}
	if err := second.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&v2); err != nil || v1 != v2 || v1 == 0 {
		t.Fatalf("迁移记录不一致：%d %d %v", v1, v2, err)
	}
	var mode string
	if err := second.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("模板拷出来的库该是 WAL：%q %v", mode, err)
	}
	if _, err := second.Exec(`CREATE TABLE only_in_second(x)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := first.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'only_in_second'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("两个库不该互相影响：%d %v", n, err)
	}
}
