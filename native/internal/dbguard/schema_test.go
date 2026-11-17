package dbguard

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

type schemaRow struct {
	track, migratedBy, migratedAt string
	version, minReader            int64
}

func readSchema(t *testing.T, db *sql.DB, track string) schemaRow {
	t.Helper()
	var r schemaRow
	if err := db.QueryRow(`SELECT track, version, min_reader, migrated_by, migrated_at FROM `+SchemaTable+` WHERE track = ?`, track).
		Scan(&r.track, &r.version, &r.minReader, &r.migratedBy, &r.migratedAt); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWriteSchemaNewOldAndStale(t *testing.T) {
	files := map[int64]string{
		1: "old",
		2: "old",
		3: markerCompatible + "\nCREATE TABLE c (id INTEGER);\n",
	}
	tr := mapTrack("chat", 2, files)
	now := time.Date(2026, 11, 16, 12, 34, 56, 0, time.FixedZone("CST", 8*3600))
	cases := []struct {
		name  string
		setup []string
	}{
		{"新库", []string{gooseDDL, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (1,1),(2,1),(3,1)`}},
		{"旧库无表", []string{gooseDDL, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (1,1),(2,1),(3,1)`}},
		{"表里行落后", []string{
			gooseDDL,
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1,1),(2,1),(3,1)`,
			schemaDDL(),
			`INSERT INTO sumika_schema VALUES ('chat', 1, 1, 'oldoldold', '2020-01-01T00:00:00Z')`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t, filepath.Join(t.TempDir(), "x.db"))
			execAll(t, db, tc.setup...)
			if err := WriteSchema(context.Background(), db, []Track{tr}, "abcdef123", now); err != nil {
				t.Fatal(err)
			}
			got := readSchema(t, db, "chat")
			want := schemaRow{
				track: "chat", version: 3, minReader: 2,
				migratedBy: "abcdef123", migratedAt: "2026-11-16T04:34:56Z",
			}
			if got != want {
				t.Fatalf("行 = %+v，要 %+v", got, want)
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM ` + SchemaTable).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("行数 = %d，要 1", n)
			}
		})
	}
}

func TestWriteSchemaIdempotent(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "x.db"))
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1,1),(2,1)`,
	)
	tr := mapTrack("chat", 1, map[int64]string{1: "old", 2: markerBreaking + "\nALTER TABLE c ADD COLUMN x INTEGER;\n"})
	now := time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC)
	for i := range 2 {
		if err := WriteSchema(context.Background(), db, []Track{tr}, "abcdef123", now); err != nil {
			t.Fatalf("第 %d 次：%v", i+1, err)
		}
	}
	got := readSchema(t, db, "chat")
	want := schemaRow{track: "chat", version: 2, minReader: 2, migratedBy: "abcdef123", migratedAt: "2026-11-16T01:02:03Z"}
	if got != want {
		t.Fatalf("行 = %+v，要 %+v", got, want)
	}
}

func TestWriteSchemaFixedTrack(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "index.db"))
	tr := Track{Name: "memory-index", Version: 1}
	now := time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC)
	if err := WriteSchema(context.Background(), db, []Track{tr}, "", now); err != nil {
		t.Fatal(err)
	}
	got := readSchema(t, db, "memory-index")
	want := schemaRow{track: "memory-index", version: 1, minReader: 1, migratedBy: "dev", migratedAt: "2026-11-16T01:02:03Z"}
	if got != want {
		t.Fatalf("行 = %+v，要 %+v", got, want)
	}
}

func TestWriteSchemaMultipleTracks(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "local.db"))
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1,1),(2,1)`,
		`CREATE TABLE goose_db_version_local (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT (datetime('now'))
		)`,
		`INSERT INTO goose_db_version_local (version_id, is_applied) VALUES (1,1),(2,1)`,
	)
	chat := mapTrack("chat", 1, map[int64]string{1: "old", 2: markerCompatible + "\nCREATE TABLE c (id INTEGER);\n"})
	local := mapTrack("local", 2, map[int64]string{1: "old", 2: "old"})
	local.Table = "goose_db_version_local"
	now := time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC)
	if err := WriteSchema(context.Background(), db, []Track{chat, local}, "abcdef123", now); err != nil {
		t.Fatal(err)
	}
	got := readSchema(t, db, "chat")
	if got.version != 2 || got.minReader != 1 {
		t.Errorf("chat = %+v，要 2/1", got)
	}
	got = readSchema(t, db, "local")
	if got.version != 2 || got.minReader != 2 {
		t.Errorf("local = %+v，要 2/2", got)
	}
}

func TestWriteSchemaNeedsGooseTable(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "x.db"))
	tr := mapTrack("chat", 0, map[int64]string{1: markerCompatible})
	if err := WriteSchema(context.Background(), db, []Track{tr}, "abcdef123", time.Now()); err == nil {
		t.Fatal("没有 goose 表时写版本表要报错")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, SchemaTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("报错后不该留下 sumika_schema")
	}
}
