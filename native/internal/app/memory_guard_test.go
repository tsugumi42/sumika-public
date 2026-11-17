package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 角色的记忆库被较新的 Sumika 升级过（sumika_schema 的 min_reader 比程序认识的新）：
// App 整个不启动，错误说清是记忆库、被较新的 Sumika 升级过（和“文件在却打不开”同一个规则）。
func TestMemoryDBTooNewStopsOpen(t *testing.T) {
	e := newEnv(t)
	file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	// 故意的旧形状加一个太新的版本行：只建裁剪过的 facts 和 sumika_schema，不改成 BaseSchema。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT,
			created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`,
		`CREATE TABLE sumika_schema (track TEXT PRIMARY KEY, version INTEGER NOT NULL, min_reader INTEGER NOT NULL,
			migrated_by TEXT NOT NULL, migrated_at TEXT NOT NULL)`,
		`INSERT INTO sumika_schema VALUES ('memory', 3, 999, 'future', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()

	a, err := Open(context.Background(), e.options())
	if a != nil {
		a.cleanup()
	}
	if err == nil {
		t.Fatal("太新的记忆库没让 App 停下来")
	}
	if !strings.Contains(err.Error(), "较新的 Sumika") || !strings.Contains(err.Error(), "记忆") {
		t.Fatalf("错误没说清是记忆库被较新的 Sumika 升级过：%v", err)
	}
}
