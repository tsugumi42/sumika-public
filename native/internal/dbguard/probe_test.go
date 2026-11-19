package dbguard

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProbeMissingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nope.db")
	st, err := Probe(context.Background(), file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Exists {
		t.Fatal("文件不存在却报了 Exists")
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("探查建出了文件：%v", err)
	}
}

// 库文件的上级目录位置被一个普通文件占着：这个库就是不存在（Windows 报路径不存在，Linux 报 ENOTDIR），
// 不是探查出错；是否建得出目录交给后面的建库去报
func TestProbeParentIsFileMeansMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("占住目录名"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Probe(context.Background(), filepath.Join(dir, "go", "memory.db"), nil)
	if err != nil {
		t.Fatalf("上级是文件应当当成不存在：%v", err)
	}
	if st.Exists {
		t.Fatal("上级是文件却报了 Exists")
	}
}

func TestProbeReadsVersionsAndSchemaWithoutWriting(t *testing.T) {
	file := filepath.Join(t.TempDir(), "local.db")
	db := openTestDB(t, file)
	execAll(t, db,
		"PRAGMA journal_mode = WAL",
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, 1), (2, 1), (3, 0)`,
		`CREATE TABLE goose_db_version_local (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT (datetime('now'))
		)`,
		`INSERT INTO goose_db_version_local (version_id, is_applied) VALUES (7, 1), (8, 1)`,
		schemaDDL(),
		`INSERT INTO sumika_schema VALUES ('chat', 2, 1, 'abcdef123', '2026-11-16T01:02:03Z')`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileSHA(t, file)
	beforeSide := sidecars(t, file)

	tracks := []Track{
		{Name: "chat", FS: mapTrack("chat", 1, map[int64]string{1: "-- old", 2: "-- old"}).FS, Floor: 1},
		{Name: "local", FS: mapTrack("local", 1, map[int64]string{7: "-- old", 8: "-- old"}).FS, Table: "goose_db_version_local", Floor: 1},
		{Name: "memory-index", Version: 1},
	}
	st, err := Probe(context.Background(), file, tracks)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || !st.HasSchema {
		t.Fatalf("Exists/HasSchema = %v/%v，要 true/true", st.Exists, st.HasSchema)
	}
	if len(st.Tracks) != 3 {
		t.Fatalf("轨数 = %d", len(st.Tracks))
	}
	chat := st.Tracks[0]
	if !chat.HasGooseTable || chat.Applied != 2 {
		t.Errorf("chat 表/版本 = %v/%d，要 true/2（3 号没应用）", chat.HasGooseTable, chat.Applied)
	}
	if chat.Schema == nil || chat.Schema.Version != 2 || chat.Schema.MinReader != 1 || chat.Schema.MigratedBy != "abcdef123" {
		t.Errorf("chat 的版本表行 = %+v", chat.Schema)
	}
	local := st.Tracks[1]
	if !local.HasGooseTable || local.Applied != 8 {
		t.Errorf("local 表/版本 = %v/%d，要 true/8", local.HasGooseTable, local.Applied)
	}
	if local.Schema != nil {
		t.Errorf("local 不该有版本表行：%+v", local.Schema)
	}
	index := st.Tracks[2]
	if index.HasGooseTable || index.Applied != 1 || index.Schema != nil {
		t.Errorf("memory-index = %+v，要没有 goose 表、版本 1、没有版本表行", index)
	}

	if after := fileSHA(t, file); after != before {
		t.Fatal("探查改了主文件")
	}
	// 只读连接在 WAL 库上会建 -wal/-shm；主文件哈希不变即可，旁文件留给 SQLite 自己管理。
	if after := sidecars(t, file); !reflect.DeepEqual(after, beforeSide) {
		t.Logf("只读探查生成了旁文件：探查前 %v，探查后 %v（主文件哈希未变）", beforeSide, after)
	}
}

func TestProbeWithoutSchemaTable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "old.db")
	db := openTestDB(t, file)
	execAll(t, db,
		"PRAGMA journal_mode = WAL",
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, 1)`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileSHA(t, file)
	beforeSide := sidecars(t, file)
	tracks := []Track{{Name: "chat", FS: mapTrack("chat", 1, map[int64]string{1: "-- old"}).FS, Floor: 1}}
	st, err := Probe(context.Background(), file, tracks)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.HasSchema {
		t.Fatalf("Exists/HasSchema = %v/%v，要 true/false", st.Exists, st.HasSchema)
	}
	if st.Tracks[0].Applied != 1 || st.Tracks[0].Schema != nil {
		t.Fatalf("chat = %+v，要版本 1、没有版本表行", st.Tracks[0])
	}
	if after := fileSHA(t, file); after != before {
		t.Fatal("探查改了主文件")
	}
	// 只读连接在 WAL 库上会建 -wal/-shm；主文件哈希不变即可，旁文件留给 SQLite 自己管理。
	if after := sidecars(t, file); !reflect.DeepEqual(after, beforeSide) {
		t.Logf("只读探查生成了旁文件：探查前 %v，探查后 %v（主文件哈希未变）", beforeSide, after)
	}

	// 再确认没有偷偷建版本表。
	db2, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var n int
	if err := db2.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, SchemaTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("探查建了 sumika_schema")
	}
}
