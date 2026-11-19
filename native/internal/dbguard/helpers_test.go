package dbguard

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func execAll(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s：%v", q, err)
		}
	}
}

// gooseDDL 是 goose 在 SQLite 上建版本表的形状。
const gooseDDL = `CREATE TABLE goose_db_version (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	version_id INTEGER NOT NULL,
	is_applied INTEGER NOT NULL,
	tstamp TIMESTAMP DEFAULT (datetime('now'))
)`

func schemaDDL() string {
	return `CREATE TABLE ` + SchemaTable + ` (
		track TEXT PRIMARY KEY,
		version INTEGER NOT NULL,
		min_reader INTEGER NOT NULL,
		migrated_by TEXT NOT NULL,
		migrated_at TEXT NOT NULL
	)`
}

func mapTrack(name string, floor int64, files map[int64]string) Track {
	m := fstest.MapFS{}
	for v, body := range files {
		m[fmt.Sprintf("%04d_test.sql", v)] = &fstest.MapFile{Data: []byte(body)}
	}
	return Track{Name: name, FS: m, Floor: floor}
}

func fileSHA(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func sidecars(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			out = append(out, filepath.Base(path+suffix))
		}
	}
	return out
}
