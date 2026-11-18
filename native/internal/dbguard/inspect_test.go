package dbguard

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Inspect 只读：文件不存在给新建，太新 / 认不出 / 探查失败给带固定原因词的错误，一个字节都不写。
func TestInspectReadOnlyDecision(t *testing.T) {
	t.Run("文件不存在", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "new.db")
		dec, err := Inspect(context.Background(), file, []Track{readyTrack()})
		if err != nil {
			t.Fatal(err)
		}
		if dec.Action != ActionCreate || !dec.NeedSchema {
			t.Fatalf("Decision = %+v，要 create 且补写版本表", dec)
		}
		if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Inspect 建出了文件：%v", err)
		}
	})

	t.Run("太新", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "chat.db")
		db := openTestDB(t, file)
		execAll(t, db,
			gooseDDL,
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, 1)`,
			schemaDDL(),
			`INSERT INTO sumika_schema VALUES ('chat', 1, 5, 'newprog', '2026-11-16T01:02:03Z')`,
		)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		before := fileSHA(t, file)
		dec, err := Inspect(context.Background(), file, []Track{readyTrack()})
		un := mustUnavailable(t, err)
		if un.Reason != ReasonTooNew {
			t.Fatalf("Reason = %s，要 too_new", un.Reason)
		}
		if dec.Action != ActionTooNew {
			t.Fatalf("Decision = %v，要 too_new", dec.Action)
		}
		if after := fileSHA(t, file); after != before {
			t.Fatal("Inspect 改了主文件")
		}
	})

	t.Run("认不出", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "alien.db")
		db := openTestDB(t, file)
		execAll(t, db, `CREATE TABLE t1 (id INTEGER PRIMARY KEY)`)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		before := fileSHA(t, file)
		dec, err := Inspect(context.Background(), file, []Track{chatTrack(2)})
		un := mustUnavailable(t, err)
		if un.Reason != ReasonUnknownSchema || dec.Action != ActionUnknownSchema {
			t.Fatalf("Reason/Action = %s/%v，要 unknown_schema", un.Reason, dec.Action)
		}
		if after := fileSHA(t, file); after != before {
			t.Fatal("Inspect 改了认不出的库")
		}
	})

	t.Run("探查失败", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not.db")
		if err := os.WriteFile(file, []byte(strings.Repeat("not sqlite. ", 50)), 0o600); err != nil {
			t.Fatal(err)
		}
		before := fileSHA(t, file)
		dec, err := Inspect(context.Background(), file, []Track{chatTrack(1)})
		un := mustUnavailable(t, err)
		if un.Reason != ReasonProbeFailed || dec.Action != ActionNone {
			t.Fatalf("Reason/Action = %s/%v，要 probe_failed / none", un.Reason, dec.Action)
		}
		if un.Err == nil || errors.Unwrap(un) == nil {
			t.Fatal("probe_failed 要包住底层错误")
		}
		if after := fileSHA(t, file); after != before {
			t.Fatal("Inspect 改了不是 SQLite 的文件")
		}
	})

	t.Run("有待跑", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "qq.db")
		legacyTwoTrackDB(t, file)
		dec, err := Inspect(context.Background(), file, twoTrackTracks(goodQQFile()))
		if err != nil {
			t.Fatal(err)
		}
		if dec.Action != ActionMigrate || len(dec.Pending) != 2 {
			t.Fatalf("Decision = %+v，要两条轨待跑", dec)
		}
		if good, failed := snapshotFiles(t, filepath.Join(filepath.Dir(file), "upgrade-snapshots")); len(good)+len(failed) != 0 {
			t.Fatalf("Inspect 拍了快照：%v / %v", good, failed)
		}
	})
}

// 只有 sumika_schema 不算“有用户表”（规格第 3 条 3b）。
func TestProbeUserTablesExcludeSchema(t *testing.T) {
	file := filepath.Join(t.TempDir(), "only-schema.db")
	db := openTestDB(t, file)
	execAll(t, db, schemaDDL(),
		`INSERT INTO sumika_schema VALUES ('memory-index', 1, 1, 'by', 'at')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Probe(context.Background(), file, []Track{{Name: "memory-index", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !st.HasSchema {
		t.Fatal("HasSchema 要 true")
	}
	if len(st.UserTables) != 0 {
		t.Fatalf("UserTables = %v，要空（sumika_schema 不算）", st.UserTables)
	}

	db = openTestDB(t, file)
	execAll(t, db, `CREATE TABLE vectors (id INTEGER PRIMARY KEY)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Probe(context.Background(), file, []Track{{Name: "memory-index", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.UserTables) != 1 || st.UserTables[0] != "vectors" {
		t.Fatalf("UserTables = %v，要只有 vectors", st.UserTables)
	}
}

// Probe 用的只读连接写不进去：mode=ro 和 query_only 两层都在。
func TestProbeConnectionRejectsWrites(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ro.db")
	db := openTestDB(t, file)
	execAll(t, db, `CREATE TABLE t (id INTEGER PRIMARY KEY)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileSHA(t, file)

	ro, err := openReadOnly(file)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec(`CREATE TABLE injected (id INTEGER)`); err == nil {
		t.Fatal("探查连接能建表")
	}
	if _, err := ro.Exec(`INSERT INTO t (id) VALUES (1)`); err == nil {
		t.Fatal("探查连接能写用户表")
	}
	if after := fileSHA(t, file); after != before {
		t.Fatal("探查连接改了主文件")
	}

	check, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var n int
	if err := check.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='injected'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("探查连接建出了 injected 表")
	}
}
