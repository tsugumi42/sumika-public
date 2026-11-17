package dbguard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const qqGooseDDL = `CREATE TABLE goose_db_version_qq (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	version_id INTEGER NOT NULL,
	is_applied INTEGER NOT NULL,
	tstamp TIMESTAMP DEFAULT (datetime('now'))
)`

func failingQQFile() string {
	return markerBreaking + "\n-- +goose Up\nCREATE TABLE q_bad (x INTEGER);\nINSERT INTO no_such_table (x) VALUES (1);\n"
}

func goodQQFile() string {
	return markerBreaking + "\n-- +goose Up\nCREATE TABLE q2 (id INTEGER PRIMARY KEY, v TEXT);\nINSERT INTO q2 (v) VALUES ('new');\n"
}

func twoTrackTracks(qq2 string) []Track {
	chat := mapTrack("chat", 1, map[int64]string{
		1: "-- +goose Up\nCREATE TABLE c (id INTEGER PRIMARY KEY, v TEXT);\n",
		2: markerCompatible + "\n-- +goose Up\nCREATE TABLE c2 (id INTEGER PRIMARY KEY, v TEXT);\nINSERT INTO c2 (v) VALUES ('x');\n",
	})
	qq := mapTrack("qq", 1, map[int64]string{
		1: "-- +goose Up\nCREATE TABLE q (id INTEGER PRIMARY KEY, v TEXT);\n",
		2: qq2,
	})
	qq.Table = "goose_db_version_qq"
	return []Track{chat, qq}
}

// legacyTwoTrackDB 造一个规格第 6 条的存量库：两条 goose 轨都在 1 号，没有 sumika_schema。
func legacyTwoTrackDB(t *testing.T, file string) {
	t.Helper()
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO c (id, v) VALUES (1, 'a'), (2, 'b')`,
		qqGooseDDL,
		`INSERT INTO goose_db_version_qq (version_id, is_applied) VALUES (0, 1), (1, 1)`,
		`CREATE TABLE q (id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO q (id, v) VALUES (1, 'q')`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func testOptions(file string, openCalls *int) Options {
	return Options{
		SnapshotDir: filepath.Join(filepath.Dir(file), "upgrade-snapshots"),
		RelName:     "go/chat/qq.db",
		OpenDB: func(f string, fresh bool) (*sql.DB, error) {
			*openCalls++
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f))
			if err != nil {
				return nil, err
			}
			db.SetMaxOpenConns(1)
			if err := db.Ping(); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC) },
	}
}

func snapshotFiles(t *testing.T, dir string) (good, failed []string) {
	t.Helper()
	good, failed, _ = snapshotFilesByKind(t, dir)
	return good, failed
}

// snapshotFilesByKind 把快照目录里的 .sqlite 分成普通、迁移失败（-failed）、换回失败（-restorefailed）三类。
func snapshotFilesByKind(t *testing.T, dir string) (good, failed, restoreFailed []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sqlite") {
			continue
		}
		switch {
		case strings.HasSuffix(e.Name(), "-restorefailed.sqlite"):
			restoreFailed = append(restoreFailed, e.Name())
		case strings.HasSuffix(e.Name(), "-failed.sqlite"):
			failed = append(failed, e.Name())
		default:
			good = append(good, e.Name())
		}
	}
	return good, failed, restoreFailed
}

func mustUnavailable(t *testing.T, err error) *Unavailable {
	t.Helper()
	var un *Unavailable
	if !errors.As(err, &un) {
		t.Fatalf("错误 = %v，要 *Unavailable", err)
	}
	return un
}

func TestOpenMigrateSecondTrackFailsRestoresFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	before := logicalState(t, file)
	calls := 0
	db, rep, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), testOptions(file, &calls))
	if db != nil {
		db.Close()
		t.Fatal("迁移失败不该交出句柄")
	}
	un := mustUnavailable(t, err)
	if un.Reason != ReasonMigrateFailed || un.Track != "qq" {
		t.Fatalf("Unavailable = %+v，要 qq 轨 migrate_failed", un)
	}
	if !strings.Contains(un.Detail, "第 2 号迁移失败") {
		t.Fatalf("Detail = %q，要带失败的第 2 号迁移", un.Detail)
	}
	if rep.Action != ActionMigrate {
		t.Fatalf("Action = %v，要 migrate", rep.Action)
	}
	if un.Err == nil {
		t.Fatal("migrate_failed 要包住 goose 的底层错误")
	}
	if after := logicalState(t, file); after != before {
		t.Fatalf("换回后和迁移前不相等：\n迁移前：%s\n换回后：%s", before, after)
	}
	good, failed := snapshotFiles(t, filepath.Dir(file)+"/upgrade-snapshots")
	if len(good) != 0 || len(failed) != 1 {
		t.Fatalf("快照 = good %v / failed %v，要 0 / 1", good, failed)
	}
	if un.Snapshot == "" || !strings.HasSuffix(un.Snapshot, "-failed.sqlite") {
		t.Fatalf("Snapshot = %q，要 -failed 的路径", un.Snapshot)
	}
	if _, err := os.Stat(un.Snapshot); err != nil {
		t.Fatalf("失败快照要在：%v", err)
	}
}

// 失败时原库旁有三种旁文件：在换回改名前注入残留的 -wal、-shm、-journal
// （真实情况是写连接没 checkpoint、异常退出留下的），换回要把三种都删掉，不只 -wal。
func TestOpenRestoreRemovesResidualSidecars(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	before := logicalState(t, file)
	o := testOptions(file, new(int))
	wrote := false
	o.Rename = func(old, new string) error {
		if new == file && strings.Contains(filepath.Base(old), ".restore-") {
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if err := os.WriteFile(file+suffix, []byte("residual sidecar from failed migration"), 0o600); err != nil {
					t.Errorf("写残留 %s：%v", suffix, err)
				}
			}
			wrote = true
		}
		return os.Rename(old, new)
	}
	_, _, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	if un := mustUnavailable(t, err); un.Reason != ReasonMigrateFailed {
		t.Fatalf("Reason = %s，要 migrate_failed", un.Reason)
	}
	if !wrote {
		t.Fatal("没有走到换回改名，测试注入没生效")
	}
	if left := sidecars(t, file); len(left) != 0 {
		t.Fatalf("换回后还有旁文件：%v", left)
	}
	if after := logicalState(t, file); after != before {
		t.Fatal("带旁文件换回后和迁移前不相等")
	}
}

// 换回后的验证不通过：注入的改名函数先把换回临时文件里的 qq 迁移记录删掉再覆盖。
func TestOpenRestoreVerificationFailureKeepsSnapshot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	o := testOptions(file, new(int))
	o.Rename = func(old, new string) error {
		if new == file && strings.Contains(filepath.Base(old), ".restore-") {
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(old))
			if err != nil {
				return err
			}
			_, err = db.Exec(`DELETE FROM goose_db_version_qq`)
			db.Close()
			if err != nil {
				return err
			}
		}
		return os.Rename(old, new)
	}
	_, rep, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonRestoreFailed {
		t.Fatalf("Reason = %s，要 restore_failed（实际错误 %v）", un.Reason, err)
	}
	if !strings.Contains(un.Detail, absPath(file)) || !strings.Contains(un.Detail, absPath(un.Snapshot)) {
		t.Fatalf("Detail 要带原库和快照的完整路径：%q", un.Detail)
	}
	if rep.Snapshot != un.Snapshot || un.Snapshot == "" {
		t.Fatalf("Report/Unavailable 快照 = %q/%q", rep.Snapshot, un.Snapshot)
	}
	if !strings.HasSuffix(un.Snapshot, "-restorefailed.sqlite") {
		t.Fatal("换回失败的快照要加 -restorefailed，普通保留和失败快照清理都不碰")
	}
	if _, err := os.Stat(un.Snapshot); err != nil {
		t.Fatalf("快照要还在：%v", err)
	}
}

func TestOpenTooNewDoesNotOpenWritable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, 1)`,
		schemaDDL(),
		`INSERT INTO sumika_schema VALUES ('chat', 1, 5, 'abcdef123', '2026-11-16T01:02:03Z')`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileSHA(t, file)
	calls := 0
	db, rep, err := Open(context.Background(), file, []Track{readyTrack()}, testOptions(file, &calls))
	if db != nil {
		db.Close()
	}
	un := mustUnavailable(t, err)
	if un.Reason != ReasonTooNew {
		t.Fatalf("Reason = %s，要 too_new", un.Reason)
	}
	if calls != 0 {
		t.Fatalf("太新的库不该开可写连接，OpenDB 调了 %d 次", calls)
	}
	if rep.Action != ActionTooNew {
		t.Fatalf("Action = %v，要 too_new", rep.Action)
	}
	if after := fileSHA(t, file); after != before {
		t.Fatal("拒绝前后主文件 SHA-256 不同")
	}
}

func TestOpenReadNewerDoesNotMigrate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, 1), (9, 1)`,
		schemaDDL(),
		`INSERT INTO sumika_schema VALUES ('chat', 9, 2, 'newerprog', '2026-11-16T01:02:03Z')`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	db, rep, err := Open(context.Background(), file, []Track{readyTrack()}, testOptions(file, &calls))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if calls != 1 {
		t.Fatalf("OpenDB 调了 %d 次，要 1", calls)
	}
	if rep.Action != ActionReadNewer {
		t.Fatalf("Action = %v，要 read_newer", rep.Action)
	}
	wantNewer := []NewerTrack{{Track: "chat", Version: 9, Latest: 3}}
	if !reflect.DeepEqual(rep.Newer, wantNewer) {
		t.Fatalf("Report.Newer = %+v，要 %+v", rep.Newer, wantNewer)
	}
	var version, minReader int64
	var by string
	if err := db.QueryRow(`SELECT version, min_reader, migrated_by FROM sumika_schema WHERE track = 'chat'`).Scan(&version, &minReader, &by); err != nil {
		t.Fatal(err)
	}
	if version != 9 || minReader != 2 || by != "newerprog" {
		t.Fatalf("版本表变了：%d/%d/%s", version, minReader, by)
	}
	var applied int64
	if err := db.QueryRow(`SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 9 {
		t.Fatalf("goose 版本 = %d，要 9（没迁移）", applied)
	}
	if _, err := db.Exec(`SELECT * FROM c`); err == nil {
		t.Fatal("没有待跑迁移却建出了程序里的表")
	}
}

func TestOpenNoSpaceSkipsSnapshotAndMigration(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	before := logicalState(t, file)
	calls := 0
	o := testOptions(file, &calls)
	o.FreeSpace = func(string) (uint64, error) { return 1, nil }
	_, rep, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonNoSpace {
		t.Fatalf("Reason = %s，要 no_space", un.Reason)
	}
	if calls != 0 {
		t.Fatalf("空间不够时不该开可写连接，OpenDB 调了 %d 次", calls)
	}
	if rep.Action != ActionMigrate {
		t.Fatalf("Action = %v，要 migrate", rep.Action)
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("空间不够却拍了快照：%v / %v", good, failed)
	}
	if after := logicalState(t, file); after != before {
		t.Fatal("空间不够时改了库")
	}
}

func TestOpenSnapshotVerificationFailureDoesNotMigrate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	before := logicalState(t, file)
	calls := 0
	o := testOptions(file, &calls)
	o.Rename = func(old, new string) error {
		if strings.HasSuffix(old, ".sqlite.partial") {
			if err := os.Rename(old, new); err != nil {
				return err
			}
			return os.WriteFile(new, []byte("not a database"), 0o600)
		}
		return os.Rename(old, new)
	}
	_, _, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonSnapshotFailed {
		t.Fatalf("Reason = %s，要 snapshot_failed（实际 %v）", un.Reason, err)
	}
	if calls != 0 {
		t.Fatalf("快照没通过时不该开可写连接，OpenDB 调了 %d 次", calls)
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("坏快照该删掉：%v / %v", good, failed)
	}
	if after := logicalState(t, file); after != before {
		t.Fatal("快照失败时改了库")
	}
}

func chatTrack(latest int64) Track {
	files := map[int64]string{
		1: "-- +goose Up\nCREATE TABLE t1 (id INTEGER PRIMARY KEY);\n",
	}
	for v := int64(2); v <= latest; v++ {
		body := fmt.Sprintf(markerCompatible+"\n-- +goose Up\nCREATE TABLE t%d (id INTEGER PRIMARY KEY);\nINSERT INTO t%d (id) VALUES (%d);\n", v, v, v)
		files[v] = body
	}
	return mapTrack("chat", 1, files)
}

func TestOpenKeepsThreeSnapshotsAndNeverPrunesFailed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`,
		`CREATE TABLE t1 (id INTEGER PRIMARY KEY)`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	o := testOptions(file, &calls)
	base := time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC)

	for i, latest := range []int64{2, 3, 4, 5, 6} {
		o.Now = func() time.Time { return base.Add(time.Duration(i) * time.Hour) }
		got, rep, err := Open(context.Background(), file, []Track{chatTrack(latest)}, o)
		if err != nil {
			t.Fatalf("迁到 %d：%v", latest, err)
		}
		got.Close()
		if rep.Action != ActionMigrate || rep.Snapshot == "" {
			t.Fatalf("迁到 %d 的 Report = %+v", latest, rep)
		}
		good, _ := snapshotFiles(t, o.SnapshotDir)
		want := i + 1
		if want > 3 {
			want = 3
		}
		if len(good) != want {
			t.Fatalf("迁到 %d 后好快照 = %d，要 %d", latest, len(good), want)
		}
		if i == 4 && len(rep.Removed) != 1 {
			t.Fatalf("第 5 次迁移的 Removed = %v，要清 1 份", rep.Removed)
		}
	}

	o.Now = func() time.Time { return base.Add(5 * time.Hour) }
	failing := mapTrack("chat", 1, map[int64]string{
		1: "-- +goose Up\nCREATE TABLE t1 (id INTEGER PRIMARY KEY);\n",
		7: markerCompatible + "\n-- +goose Up\nINSERT INTO no_such_table (x) VALUES (1);\n",
	})
	_, _, err := Open(context.Background(), file, []Track{failing}, o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonMigrateFailed || !strings.HasSuffix(un.Snapshot, "-failed.sqlite") {
		t.Fatalf("失败快照 = %+v", un)
	}
	failedName := filepath.Base(un.Snapshot)

	o.Now = func() time.Time { return base.Add(6 * time.Hour) }
	got, _, err := Open(context.Background(), file, []Track{chatTrack(7)}, o)
	if err != nil {
		t.Fatal(err)
	}
	got.Close()
	good, failed := snapshotFiles(t, o.SnapshotDir)
	if len(good) != 3 {
		t.Fatalf("成功迁移后好快照 = %v，要 3 份", good)
	}
	if len(failed) != 1 || failed[0] != failedName {
		t.Fatalf("失败快照 = %v，要保留 %s", failed, failedName)
	}
}

func TestOpenCreatesNewDBAndWritesVersionTable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "fresh.db")
	o := testOptions(file, new(int))
	db, rep, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if rep.Action != ActionCreate {
		t.Fatalf("Action = %v，要 create", rep.Action)
	}
	if rep.Snapshot != "" {
		t.Fatalf("新库不该拍快照：%q", rep.Snapshot)
	}
	var version, minReader int64
	if err := db.QueryRow(`SELECT version, min_reader FROM sumika_schema WHERE track = 'chat'`).Scan(&version, &minReader); err != nil {
		t.Fatal(err)
	}
	if version != 2 || minReader != 1 {
		t.Fatalf("版本表 = %d/%d，要 2/1", version, minReader)
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("新库拍了快照：%v / %v", good, failed)
	}
}

// logicalState 是规格第 4 条“逻辑相等”的测试实现：sqlite_master 里所有带 SQL 的对象，
// 加上每张表按主键（没有主键按 rowid）排好后的内容 SHA-256。
func logicalState(t *testing.T, file string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file)+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var b strings.Builder
	objs, err := db.Query(`SELECT type, name, coalesce(sql, '') FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	for objs.Next() {
		var typ, name, ddl string
		if err := objs.Scan(&typ, &name, &ddl); err != nil {
			objs.Close()
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s %s\n", typ, name, ddl)
	}
	objs.Close()
	if err := objs.Err(); err != nil {
		t.Fatal(err)
	}

	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			tables.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	tables.Close()
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}

	for _, name := range names {
		cols, err := tableColumns(db, name)
		if err != nil {
			t.Fatal(err)
		}
		order := "rowid"
		if pks, err := primaryKeys(db, name); err != nil {
			t.Fatal(err)
		} else if len(pks) > 0 {
			quoted := make([]string, len(pks))
			for i, pk := range pks {
				quoted[i] = quoteIdent(pk)
			}
			order = strings.Join(quoted, ", ")
		}
		rows, err := db.Query(`SELECT * FROM ` + quoteIdent(name) + ` ORDER BY ` + order)
		if err != nil {
			t.Fatalf("读表 %s：%v", name, err)
		}
		h := sha256.New()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for _, v := range vals {
				switch x := v.(type) {
				case nil:
					h.Write([]byte{0})
				case []byte:
					h.Write(x)
				default:
					fmt.Fprint(h, x)
				}
				h.Write([]byte{0x1f})
			}
			h.Write([]byte{0x1e})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "table %s %s\n", name, hex.EncodeToString(h.Sum(nil)))
	}
	return b.String()
}

func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func primaryKeys(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// 只有版本号、没有迁移文件的轨（memory-index）：新建库也不让 goose 碰它，只写版本表。
func TestOpenCreatesFixedTrackOnly(t *testing.T) {
	file := filepath.Join(t.TempDir(), "index.db")
	var fresh []bool
	o := testOptions(file, new(int))
	inner := o.OpenDB
	o.OpenDB = func(f string, isFresh bool) (*sql.DB, error) {
		fresh = append(fresh, isFresh)
		return inner(f, isFresh)
	}
	tracks := []Track{{Name: "memory-index", Version: Floors["memory-index"]}}
	db, rep, err := Open(context.Background(), file, tracks, o)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if rep.Action != ActionCreate {
		t.Fatalf("Action = %v，要 create", rep.Action)
	}
	if len(fresh) != 1 || !fresh[0] {
		t.Fatalf("OpenDB fresh = %v，要一次 true", fresh)
	}
	var version, minReader int64
	if err := db.QueryRow(`SELECT version, min_reader FROM sumika_schema WHERE track = 'memory-index'`).Scan(&version, &minReader); err != nil {
		t.Fatal(err)
	}
	if version != Floors["memory-index"] || minReader != Floors["memory-index"] {
		t.Fatalf("版本表 = %d/%d，要 %d/%d", version, minReader, Floors["memory-index"], Floors["memory-index"])
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='goose_db_version'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("没有迁移文件的轨不该建 goose 表")
	}
}

// 已有库里的固定版本轨（memory-v2.db 的样子：有索引自己的表，版本表也有行）：不跑 goose，行原样保留。
func TestOpenExistingFixedTrackOnly(t *testing.T) {
	file := filepath.Join(t.TempDir(), "index.db")
	db := openTestDB(t, file)
	execAll(t, db, `CREATE TABLE memory_vectors (id INTEGER PRIMARY KEY)`, schemaDDL(),
		fmt.Sprintf(`INSERT INTO sumika_schema VALUES ('memory-index', %d, %d, 'abcdef123', '2026-11-16T01:02:03Z')`,
			Floors["memory-index"], Floors["memory-index"]))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var fresh []bool
	o := testOptions(file, &calls)
	inner := o.OpenDB
	o.OpenDB = func(f string, isFresh bool) (*sql.DB, error) {
		fresh = append(fresh, isFresh)
		return inner(f, isFresh)
	}
	got, rep, err := Open(context.Background(), file, []Track{{Name: "memory-index", Version: Floors["memory-index"]}}, o)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if rep.Action != ActionOpen {
		t.Fatalf("Action = %v，要 open", rep.Action)
	}
	if len(fresh) != 1 || fresh[0] {
		t.Fatalf("OpenDB fresh = %v，已有库要 false", fresh)
	}
	row := readSchema(t, got, "memory-index")
	if row.version != Floors["memory-index"] || row.migratedBy != "abcdef123" {
		t.Fatalf("版本表行 = %+v，要原样保留", row)
	}
}

// 迁移途中取消：换回照样做，返回的是取消错误，不是 restore_failed。
func TestOpenCancelDuringMigrationRestores(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	before := logicalState(t, file)
	ctx, cancel := context.WithCancel(context.Background())
	o := testOptions(file, new(int))
	inner := o.OpenDB
	o.OpenDB = func(f string, isFresh bool) (*sql.DB, error) {
		cancel()
		return inner(f, isFresh)
	}
	db, rep, err := Open(ctx, file, twoTrackTracks(goodQQFile()), o)
	if db != nil {
		db.Close()
		t.Fatal("取消后不该交出句柄")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误 = %v，要 errors.Is(context.Canceled)", err)
	}
	var un *Unavailable
	if errors.As(err, &un) {
		t.Fatalf("迁移途中取消不该报 *Unavailable（%s）：%v", un.Reason, err)
	}
	if after := logicalState(t, file); after != before {
		t.Fatal("取消后没有把库换回迁移前")
	}
	good, failed := snapshotFiles(t, o.SnapshotDir)
	if len(good) != 0 || len(failed) != 1 || !strings.HasSuffix(rep.Snapshot, "-failed.sqlite") {
		t.Fatalf("快照 = good %v / failed %v / rep %q", good, failed, rep.Snapshot)
	}
}

// 换回时旁文件删不掉：原因写清楚，快照标 -failed，主文件已经换回。
func TestOpenRestoreSidecarRemoveFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	o := testOptions(file, new(int))
	o.Rename = func(old, new string) error {
		if new == file && strings.Contains(filepath.Base(old), ".restore-") {
			if err := os.WriteFile(file+"-wal", []byte("stale wal"), 0o600); err != nil {
				return err
			}
		}
		return os.Rename(old, new)
	}
	o.Remove = func(path string) error {
		if path == file+"-wal" {
			return errors.New("injected remove failure")
		}
		return os.Remove(path)
	}
	t.Cleanup(func() { _ = os.Remove(file + "-wal") })
	_, rep, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonRestoreFailed {
		t.Fatalf("Reason = %s，要 restore_failed（实际 %v）", un.Reason, err)
	}
	if !strings.Contains(un.Detail, "主文件已换回") || !strings.Contains(un.Detail, absPath(file+"-wal")) {
		t.Fatalf("Detail = %q，要写主文件已换回和残留旁文件路径", un.Detail)
	}
	if !strings.HasSuffix(un.Snapshot, "-restorefailed.sqlite") || rep.Snapshot != un.Snapshot {
		t.Fatalf("快照 = %q / %q，要同一份 -restorefailed", un.Snapshot, rep.Snapshot)
	}
	if _, err := os.Stat(un.Snapshot); err != nil {
		t.Fatalf("失败快照要留着：%v", err)
	}
}

// 持续失败时，同一个库、同一段“从几到几”的失败快照只留最新一份。
func TestOpenFailedSnapshotsKeepLatestPerSpan(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	calls := 0
	o := testOptions(file, &calls)
	base := time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC)
	var last string
	for i := range 4 {
		o.Now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		_, _, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
		un := mustUnavailable(t, err)
		last = un.Snapshot
	}
	good, failed := snapshotFiles(t, o.SnapshotDir)
	if len(good) != 0 || len(failed) != 1 {
		t.Fatalf("快照 = good %v / failed %v，要只留一份失败快照", good, failed)
	}
	if failed[0] != filepath.Base(last) {
		t.Fatalf("留下的失败快照 = %s，要最新的 %s", failed[0], filepath.Base(last))
	}
}

// 有用户表、却一张 goose 表都没有：不是 0 版老库，拒开且一个字节都不写。
func TestOpenUnknownSchemaRejected(t *testing.T) {
	file := filepath.Join(t.TempDir(), "alien.db")
	db := openTestDB(t, file)
	execAll(t, db, `CREATE TABLE t1 (id INTEGER PRIMARY KEY)`, `INSERT INTO t1 VALUES (1)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := fileSHA(t, file)
	calls := 0
	o := testOptions(file, &calls)
	_, rep, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonUnknownSchema {
		t.Fatalf("Reason = %s，要 unknown_schema（实际 %v）", un.Reason, err)
	}
	if rep.Action != ActionUnknownSchema {
		t.Fatalf("Action = %v", rep.Action)
	}
	if calls != 0 {
		t.Fatalf("认不出的库不该开可写连接，OpenDB 调了 %d 次", calls)
	}
	if after := fileSHA(t, file); after != before {
		t.Fatal("拒开前后主文件 SHA-256 不同")
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("认不出的库不该拍快照：%v / %v", good, failed)
	}
}

// 声明的轨接受没有 goose 表的老库：按 0 版迁移，原有用户表保留。
func TestOpenAcceptsLegacyWithoutGoose(t *testing.T) {
	file := filepath.Join(t.TempDir(), "memory.db")
	db := openTestDB(t, file)
	execAll(t, db, `CREATE TABLE t1 (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO t1 (v) VALUES ('old')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	memory := mapTrack("memory", 0, map[int64]string{
		1: markerBreaking + "\n-- +goose Up\nCREATE TABLE m (id INTEGER PRIMARY KEY);\n",
	})
	memory.AcceptNoGoose = true
	o := testOptions(file, new(int))
	got, rep, err := Open(context.Background(), file, []Track{memory}, o)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if rep.Action != ActionMigrate {
		t.Fatalf("Action = %v，要 migrate", rep.Action)
	}
	var v string
	if err := got.QueryRow(`SELECT v FROM t1 WHERE id = 1`).Scan(&v); err != nil || v != "old" {
		t.Fatalf("老用户表 = %q / %v，要保留 old", v, err)
	}
	if row := readSchema(t, got, "memory"); row.version != 1 {
		t.Fatalf("版本表行 = %+v，要 1", row)
	}
	good, failed := snapshotFiles(t, o.SnapshotDir)
	if len(good) != 1 || len(failed) != 0 {
		t.Fatalf("快照 = %v / %v，迁移前要拍一份", good, failed)
	}
}

// 0 字节文件按新建走：不拍快照，OpenDB 收到 fresh=true。
func TestOpenEmptyFileTreatedAsNew(t *testing.T) {
	file := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var fresh []bool
	o := testOptions(file, &calls)
	inner := o.OpenDB
	o.OpenDB = func(f string, isFresh bool) (*sql.DB, error) {
		fresh = append(fresh, isFresh)
		return inner(f, isFresh)
	}
	db, rep, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if rep.Action != ActionCreate || rep.Snapshot != "" {
		t.Fatalf("Report = %+v，要按新建且不拍快照", rep)
	}
	if len(fresh) != 1 || !fresh[0] {
		t.Fatalf("OpenDB fresh = %v，要 true", fresh)
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("0 字节文件不该拍快照：%v / %v", good, failed)
	}
}

// 探查失败：不是 SQLite 或版本表列类型不对 → probe_failed，错误里不带库里的值。
func TestOpenProbeFailures(t *testing.T) {
	t.Run("不是 SQLite", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not.db")
		text := strings.Repeat("hello world, not a database. ", 100)
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		before := fileSHA(t, file)
		calls := 0
		_, rep, err := Open(context.Background(), file, []Track{chatTrack(1)}, testOptions(file, &calls))
		un := mustUnavailable(t, err)
		if un.Reason != ReasonProbeFailed {
			t.Fatalf("Reason = %s，要 probe_failed（实际 %v）", un.Reason, err)
		}
		if rep.Action != ActionNone || ActionNone.String() != "none" {
			t.Fatalf("Report.Action = %v（%s），提前返回要是 none", rep.Action, rep.Action)
		}
		if un.Err == nil || !errors.Is(err, un.Err) {
			t.Fatal("probe_failed 要包住底层错误")
		}
		if strings.Contains(un.Detail, "hello world") {
			t.Fatalf("Detail 带了库里的内容：%q", un.Detail)
		}
		if calls != 0 {
			t.Fatalf("探查失败不该开可写连接，OpenDB 调了 %d 次", calls)
		}
		if after := fileSHA(t, file); after != before {
			t.Fatal("探查失败改了文件")
		}
	})
	t.Run("min_reader 不是整数", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "bad.db")
		db := openTestDB(t, file)
		execAll(t, db,
			gooseDDL,
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, 1)`,
			schemaDDL(),
			`INSERT INTO sumika_schema VALUES ('chat', 1, 'abc', 'by', 'at')`,
		)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		calls := 0
		_, _, err := Open(context.Background(), file, []Track{chatTrack(1)}, testOptions(file, &calls))
		un := mustUnavailable(t, err)
		if un.Reason != ReasonProbeFailed {
			t.Fatalf("Reason = %s，要 probe_failed（实际 %v）", un.Reason, err)
		}
		if !strings.Contains(un.Detail, "min_reader 不是整数") || strings.Contains(un.Detail, "abc") {
			t.Fatalf("Detail = %q，要只说 min_reader 不是整数且不带值", un.Detail)
		}
	})
}

// 新建中途失败：这次建的文件删掉，原因 create_failed，详情带迁移号。
func TestOpenCreateFailureRemovesNewFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "new.db")
	tracks := []Track{mapTrack("chat", 0, map[int64]string{
		1: markerBreaking + "\n-- +goose Up\nINSERT INTO no_such_table (x) VALUES (1);\n",
	})}
	o := testOptions(file, new(int))
	db, _, err := Open(context.Background(), file, tracks, o)
	if db != nil {
		db.Close()
	}
	un := mustUnavailable(t, err)
	if un.Reason != ReasonCreateFailed {
		t.Fatalf("Reason = %s，要 create_failed（实际 %v）", un.Reason, err)
	}
	if !strings.Contains(un.Detail, "第 1 号迁移失败") {
		t.Fatalf("Detail = %q，要带迁移号", un.Detail)
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("新建失败后文件要删掉：%v", err)
	}
	if left := sidecars(t, file); len(left) != 0 {
		t.Fatalf("新建失败后旁文件要删掉：%v", left)
	}
}

// 文件原来就在（0 字节）：失败只返回，不删它。
func TestOpenCreateFailureKeepsPreexistingEmptyFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{mapTrack("chat", 0, map[int64]string{
		1: markerBreaking + "\n-- +goose Up\nINSERT INTO no_such_table (x) VALUES (1);\n",
	})}
	_, _, err := Open(context.Background(), file, tracks, testOptions(file, new(int)))
	un := mustUnavailable(t, err)
	if un.Reason != ReasonCreateFailed {
		t.Fatalf("Reason = %s，要 create_failed", un.Reason)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("原来就在的文件不该删：%v", err)
	}
}

// OpenDB 自己失败时，这次调用可能已经建出文件：也要清掉。
func TestOpenCreateFailureCleansUpOpenDBError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "new.db")
	o := testOptions(file, new(int))
	o.OpenDB = func(f string, fresh bool) (*sql.DB, error) {
		if err := os.WriteFile(f, []byte("half"), 0o600); err != nil {
			return nil, err
		}
		return nil, errors.New("injected open error")
	}
	_, _, err := Open(context.Background(), file, []Track{chatTrack(1)}, o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonCreateFailed {
		t.Fatalf("Reason = %s，要 create_failed（实际 %v）", un.Reason, err)
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenDB 失败后这次建的文件要删掉：%v", err)
	}
}
