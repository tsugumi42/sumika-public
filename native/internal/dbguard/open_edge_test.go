package dbguard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func pendingChatDB(t *testing.T, file string) {
	t.Helper()
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO c (id, v) VALUES (1, 'a')`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mutateSQLite(t *testing.T, file string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s：%v", q, err)
		}
	}
}

// 设了快照目录却不给 RelName：直接报错，不开库、不碰文件（快照前缀靠它区分同名的库）。
func TestOpenRequiresRelNameWithSnapshotDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	before := fileSHA(t, file)
	calls := 0
	o := testOptions(file, &calls)
	o.RelName = ""
	db, rep, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
	if db != nil {
		db.Close()
		t.Fatal("配置不全不该交出句柄")
	}
	if err == nil || !strings.Contains(err.Error(), "RelName") {
		t.Fatalf("错误 = %v，要说明缺 RelName", err)
	}
	if rep.Action != ActionNone {
		t.Fatalf("Action = %v，要 none", rep.Action)
	}
	if calls != 0 {
		t.Fatalf("OpenDB 调了 %d 次，要 0", calls)
	}
	if after := fileSHA(t, file); after != before {
		t.Fatal("配置不全时改了库文件")
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("配置不全时拍了快照：%v / %v", good, failed)
	}
}

// 同一进程对同一个文件的 Open 互斥：两个 goroutine 同时开有待跑迁移的库，
// 后一个等前一个迁完，看到“无待跑”，只拍一份快照。
func TestOpenSameFileSerializedInProcess(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	calls := 0
	o := testOptions(file, &calls)
	inner := o.OpenDB
	var once sync.Once
	migrating := make(chan struct{})
	secondStarted := make(chan struct{})
	o.OpenDB = func(f string, fresh bool) (*sql.DB, error) {
		if !fresh {
			once.Do(func() {
				close(migrating)
				<-secondStarted
			})
		}
		return inner(f, fresh)
	}

	type result struct {
		db  *sql.DB
		rep Report
		err error
	}
	results := make(chan result, 2)
	go func() {
		db, rep, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
		results <- result{db, rep, err}
	}()
	select {
	case <-migrating:
	case <-time.After(10 * time.Second):
		close(secondStarted)
		t.Fatal("第一个 Open 没走到可写连接")
	}
	go func() {
		close(secondStarted)
		db, rep, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
		results <- result{db, rep, err}
	}()

	var got []result
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatalf("并发 Open 失败：%v", r.err)
		}
		got = append(got, r)
	}
	for _, r := range got {
		defer r.db.Close()
	}
	actions := map[Action]int{}
	for _, r := range got {
		actions[r.rep.Action]++
	}
	if actions[ActionMigrate] != 1 || actions[ActionOpen] != 1 {
		t.Fatalf("两条路线的次数 = %v，要一次 migrate、一次 open", actions)
	}
	good, failed := snapshotFiles(t, o.SnapshotDir)
	if len(good) != 1 || len(failed) != 0 {
		t.Fatalf("快照 = good %v / failed %v，要只拍一份", good, failed)
	}
	var applied int64
	if err := got[0].db.QueryRow(`SELECT coalesce(max(version_id), 0) FROM goose_db_version_qq WHERE is_applied`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("qq 迁移号 = %d，要 2", applied)
	}
}

func pendingWALDB(t *testing.T, file string) *sql.DB {
	t.Helper()
	db := openTestDB(t, file)
	execAll(t, db,
		"PRAGMA journal_mode = WAL",
		"PRAGMA wal_autocheckpoint = 0",
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY)`,
		qqGooseDDL,
		`INSERT INTO goose_db_version_qq (version_id, is_applied) VALUES (0, 1), (1, 1)`,
		`CREATE TABLE q (id INTEGER PRIMARY KEY)`,
	)
	return db
}

func walFootprint(t *testing.T, file string) uint64 {
	t.Helper()
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	wi, err := os.Stat(file + "-wal")
	if err != nil {
		t.Fatalf("要造一个带 -wal 的库：%v", err)
	}
	if wi.Size() == 0 {
		t.Fatal("-wal 是空的，测不到计入")
	}
	return uint64(fi.Size()) + uint64(wi.Size())
}

// 空间检查同时看库所在盘和快照目录所在盘，大小按库 + -wal 的 2 倍算：
// 正好 2 倍通过，少 1 字节不通过。
func TestOpenSpaceChecksTwiceFootprintOnBothVolumes(t *testing.T) {
	t.Run("正好 2 倍通过", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "qq.db")
		writer := pendingWALDB(t, file)
		defer writer.Close()
		size := walFootprint(t, file)
		snapDir := filepath.Join(t.TempDir(), "snapshots")
		calls := 0
		o := testOptions(file, &calls)
		o.SnapshotDir = snapDir
		var dirs []string
		o.FreeSpace = func(dir string) (uint64, error) {
			dirs = append(dirs, dir)
			return 2 * size, nil
		}
		db, rep, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
		if rep.Action != ActionMigrate {
			t.Fatalf("Action = %v，要 migrate", rep.Action)
		}
		if calls != 1 {
			t.Fatalf("OpenDB 调了 %d 次，要 1", calls)
		}
		wantDirs := []string{filepath.Dir(file), snapDir}
		if !reflect.DeepEqual(dirs, wantDirs) {
			t.Fatalf("FreeSpace 查了 %v，要 %v", dirs, wantDirs)
		}
		if good, failed := snapshotFiles(t, snapDir); len(good) != 1 || len(failed) != 0 {
			t.Fatalf("快照 = %v / %v，要一份", good, failed)
		}
	})

	noSpace := func(t *testing.T, freeDB func(size uint64) uint64, freeSnap func(size uint64) uint64) {
		t.Helper()
		file := filepath.Join(t.TempDir(), "qq.db")
		writer := pendingWALDB(t, file)
		defer writer.Close()
		size := walFootprint(t, file)
		snapDir := filepath.Join(t.TempDir(), "snapshots")
		calls := 0
		o := testOptions(file, &calls)
		o.SnapshotDir = snapDir
		o.FreeSpace = func(dir string) (uint64, error) {
			if dir == snapDir {
				return freeSnap(size), nil
			}
			return freeDB(size), nil
		}
		db, _, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
		if db != nil {
			db.Close()
			t.Fatal("空间不够却交出了句柄")
		}
		un := mustUnavailable(t, err)
		if un.Reason != ReasonNoSpace {
			t.Fatalf("Reason = %s，要 no_space（实际 %v）", un.Reason, err)
		}
		if calls != 0 {
			t.Fatalf("空间不够时 OpenDB 调了 %d 次，要 0", calls)
		}
		if good, failed := snapshotFiles(t, snapDir); len(good)+len(failed) != 0 {
			t.Fatalf("空间不够却拍了快照：%v / %v", good, failed)
		}
	}

	t.Run("库所在盘少 1 字节", func(t *testing.T) {
		noSpace(t,
			func(size uint64) uint64 { return 2*size - 1 },
			func(size uint64) uint64 { return 2 * size },
		)
	})
	t.Run("快照目录所在盘少 1 字节", func(t *testing.T) {
		noSpace(t,
			func(size uint64) uint64 { return 2 * size },
			func(size uint64) uint64 { return 2*size - 1 },
		)
	})
}

// 拍快照前清掉这个库残留的 .partial，别的库的不动。
func TestOpenRemovesStaleSnapshotPartials(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	pendingChatDB(t, file)
	calls := 0
	o := testOptions(file, &calls)
	if err := os.MkdirAll(o.SnapshotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(o.SnapshotDir, "go-chat-qq.db-chat-v1-to-v2-20200101-000000.sqlite.partial")
	other := filepath.Join(o.SnapshotDir, "go-chat-local.db-chat-v1-to-v2-20200101-000000.sqlite.partial")
	for _, path := range []string{stale, other} {
		if err := os.WriteFile(path, []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, _, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("这个库的残留 .partial 要清掉：%v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("别的库的 .partial 不该动：%v", err)
	}
}

// 快照里 goose 号和迁移前不一致：核对失败，不迁移、坏快照删掉。
func TestOpenSnapshotGooseNumberMismatchFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	before := logicalState(t, file)
	calls := 0
	o := testOptions(file, &calls)
	mutated := false
	o.Rename = func(old, new string) error {
		if strings.HasSuffix(old, ".sqlite.partial") {
			if err := os.Rename(old, new); err != nil {
				return err
			}
			mutateSQLite(t, new, `DELETE FROM goose_db_version_qq`)
			mutated = true
			return nil
		}
		return os.Rename(old, new)
	}
	_, _, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonSnapshotFailed {
		t.Fatalf("Reason = %s，要 snapshot_failed（实际 %v）", un.Reason, err)
	}
	if !mutated {
		t.Fatal("没有走到快照改名，测试注入没生效")
	}
	if calls != 0 {
		t.Fatalf("快照号不对时不该开可写连接，OpenDB 调了 %d 次", calls)
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("坏快照该删掉：%v / %v", good, failed)
	}
	if after := logicalState(t, file); after != before {
		t.Fatal("快照核对失败时改了库")
	}
}

// 快照能打开、但 integrity_check 不通过：snapshot_failed，不迁移、坏快照删掉。
func TestOpenSnapshotIntegrityFailureFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	db := openTestDB(t, file)
	execAll(t, db, `CREATE INDEX ci ON c(v)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := logicalState(t, file)
	calls := 0
	o := testOptions(file, &calls)
	mutated := false
	o.Rename = func(old, new string) error {
		if strings.HasSuffix(old, ".sqlite.partial") {
			if err := os.Rename(old, new); err != nil {
				return err
			}
			// 索引里存的是 c.v，sqlite_master 却说索引在 c.id 上：integrity_check 报“row missing from index”。
			mutateSQLite(t, new,
				`PRAGMA writable_schema = ON`,
				`UPDATE sqlite_master SET sql = 'CREATE INDEX ci ON c(id)' WHERE name = 'ci'`)
			mutated = true
			return nil
		}
		return os.Rename(old, new)
	}
	_, _, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonSnapshotFailed {
		t.Fatalf("Reason = %s，要 snapshot_failed（实际 %v）", un.Reason, err)
	}
	if !mutated {
		t.Fatal("没有走到快照改名，测试注入没生效")
	}
	if calls != 0 {
		t.Fatalf("快照 integrity 不过时不该开可写连接，OpenDB 调了 %d 次", calls)
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("坏快照该删掉：%v / %v", good, failed)
	}
	if after := logicalState(t, file); after != before {
		t.Fatal("快照 integrity 不过时改了库")
	}
}

// 换回后的 sumika_schema 和迁移前不一致：restore_failed，快照标 -restorefailed 永远留着。
func TestOpenRestoreVerifiesSchemaTable(t *testing.T) {
	cases := []struct {
		name   string
		mutate string
	}{
		{"行内容不同", `UPDATE sumika_schema SET version = version + 1`},
		{"表没了", `DROP TABLE sumika_schema`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "qq.db")
			legacyTwoTrackDB(t, file)
			db := openTestDB(t, file)
			execAll(t, db, schemaDDL(),
				`INSERT INTO sumika_schema VALUES ('chat', 1, 1, 'oldprog', '2026-11-16T01:02:03Z')`)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			o := testOptions(file, new(int))
			mutated := false
			o.Rename = func(old, new string) error {
				if new == file && strings.Contains(filepath.Base(old), ".restore-") {
					mutateSQLite(t, old, tc.mutate)
					mutated = true
				}
				return os.Rename(old, new)
			}
			_, rep, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
			un := mustUnavailable(t, err)
			if un.Reason != ReasonRestoreFailed {
				t.Fatalf("Reason = %s，要 restore_failed（实际 %v）", un.Reason, err)
			}
			if !mutated {
				t.Fatal("没有走到换回改名，测试注入没生效")
			}
			if un.Err == nil {
				t.Fatal("restore_failed 要包住底层错误")
			}
			if !strings.HasSuffix(un.Snapshot, "-restorefailed.sqlite") || rep.Snapshot != un.Snapshot {
				t.Fatalf("快照 = %q / %q，要同一份 -restorefailed", un.Snapshot, rep.Snapshot)
			}
			if _, err := os.Stat(un.Snapshot); err != nil {
				t.Fatalf("失败快照要留着：%v", err)
			}
		})
	}
}

func chatGooseAt(t *testing.T, file string, applied int64) {
	t.Helper()
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		fmt.Sprintf(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (%d, 1)`, applied),
		`CREATE TABLE c (id INTEGER PRIMARY KEY)`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// ActionOpen 要补写版本表：缺表、缺行、落后都写回 goose 的号。
func TestOpenBackfillsSchemaOnOpen(t *testing.T) {
	t.Run("缺表", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "chat.db")
		chatGooseAt(t, file, 2)
		o := testOptions(file, new(int))
		db, rep, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if rep.Action != ActionOpen {
			t.Fatalf("Action = %v，要 open", rep.Action)
		}
		if row := readSchema(t, db, "chat"); row.version != 2 {
			t.Fatalf("版本表行 = %+v，要 version 2", row)
		}
	})
	t.Run("缺行", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "chat.db")
		chatGooseAt(t, file, 2)
		db := openTestDB(t, file)
		execAll(t, db, schemaDDL(),
			`INSERT INTO sumika_schema VALUES ('other', 1, 1, 'by', 'at')`)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		o := testOptions(file, new(int))
		got, rep, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		if rep.Action != ActionOpen {
			t.Fatalf("Action = %v，要 open", rep.Action)
		}
		if row := readSchema(t, got, "chat"); row.version != 2 {
			t.Fatalf("版本表行 = %+v，要 version 2", row)
		}
	})
	t.Run("落后", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "chat.db")
		chatGooseAt(t, file, 2)
		db := openTestDB(t, file)
		execAll(t, db, schemaDDL(),
			`INSERT INTO sumika_schema VALUES ('chat', 1, 1, 'oldprog', '2026-11-16T01:02:03Z')`)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		o := testOptions(file, new(int))
		o.MigratedBy = "testbuild"
		got, rep, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		if rep.Action != ActionOpen {
			t.Fatalf("Action = %v，要 open", rep.Action)
		}
		row := readSchema(t, got, "chat")
		if row.version != 2 || row.migratedBy != "testbuild" {
			t.Fatalf("版本表行 = %+v，要 version 2 / migrated_by testbuild", row)
		}
	})
}

// 迁移成功后版本表是新号（不只是迁移前补写）。
func TestOpenMigrateWritesVersionTable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	chatGooseAt(t, file, 1)
	o := testOptions(file, new(int))
	o.MigratedBy = "testbuild"
	db, rep, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if rep.Action != ActionMigrate || rep.Snapshot == "" {
		t.Fatalf("Report = %+v，要 migrate 且拍过快照", rep)
	}
	row := readSchema(t, db, "chat")
	if row.version != 2 || row.migratedBy != "testbuild" {
		t.Fatalf("版本表行 = %+v，要迁移后写 version 2 / migrated_by testbuild", row)
	}
}

// 一轨较新能读、一轨有待跑：整份不迁、不拍快照，Report.Newer 填上。
func TestOpenNewerWinsOverPending(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (9, 1)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY)`,
		qqGooseDDL,
		`INSERT INTO goose_db_version_qq (version_id, is_applied) VALUES (0, 1), (1, 1)`,
		`CREATE TABLE q (id INTEGER PRIMARY KEY)`,
		schemaDDL(),
		`INSERT INTO sumika_schema VALUES ('chat', 9, 2, 'newerprog', '2026-11-16T01:02:03Z')`,
		`INSERT INTO sumika_schema VALUES ('qq', 1, 1, 'oldprog', '2026-11-16T01:02:03Z')`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	o := testOptions(file, &calls)
	got, rep, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if rep.Action != ActionReadNewer {
		t.Fatalf("Action = %v，要 read_newer", rep.Action)
	}
	want := []NewerTrack{{Track: "chat", Version: 9, Latest: 2}}
	if !reflect.DeepEqual(rep.Newer, want) {
		t.Fatalf("Report.Newer = %+v，要 %+v", rep.Newer, want)
	}
	if calls != 1 {
		t.Fatalf("OpenDB 调了 %d 次，要 1", calls)
	}
	if good, failed := snapshotFiles(t, o.SnapshotDir); len(good)+len(failed) != 0 {
		t.Fatalf("较新能读时不该拍快照：%v / %v", good, failed)
	}
	var applied int64
	if err := got.QueryRow(`SELECT coalesce(max(version_id), 0) FROM goose_db_version_qq WHERE is_applied`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("qq 迁移号 = %d，要仍是 1（没迁）", applied)
	}
	row := readSchema(t, got, "qq")
	if row.version != 1 || row.migratedBy != "oldprog" {
		t.Fatalf("qq 版本表行 = %+v，要原样", row)
	}
}

// 太新的消息：带“被较新的 Sumika”和 %q 过的 migrated_by，截到 64 个字符以内。
func TestOpenTooNewMessageQuotesMigratedBy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	long := strings.Repeat("a", 60) + "\"\n" + strings.Repeat("b", 10)
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`,
		schemaDDL(),
		`INSERT INTO sumika_schema VALUES ('chat', 1, 5, 'placeholder', '2026-11-16T01:02:03Z')`,
	)
	if _, err := db.Exec(`UPDATE sumika_schema SET migrated_by = ? WHERE track = 'chat'`, long); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, _, err := Open(context.Background(), file, []Track{readyTrack()}, testOptions(file, &calls))
	un := mustUnavailable(t, err)
	if un.Reason != ReasonTooNew {
		t.Fatalf("Reason = %s，要 too_new", un.Reason)
	}
	if calls != 0 {
		t.Fatalf("太新的库不该开可写连接，OpenDB 调了 %d 次", calls)
	}
	clipped := string([]rune(long)[:64])
	quoted := fmt.Sprintf("%q", clipped)
	if !strings.Contains(un.Error(), "被较新的 Sumika") {
		t.Fatalf("消息 = %q，要说被较新的 Sumika 升级过", un.Error())
	}
	if !strings.Contains(un.Detail, "（"+quoted+"）") {
		t.Fatalf("Detail = %q，要带 %%q 过的 %q", un.Detail, quoted)
	}
	if strings.Contains(un.Detail, strings.Repeat("b", 10)) {
		t.Fatal("migrated_by 没有截到 64 个字符")
	}
	if strings.Contains(un.Detail, "\"\n") {
		t.Fatalf("migrated_by 没有 %%q 转义：%q", un.Detail)
	}

	t.Run("没有 migrated_by", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "chat.db")
		db := openTestDB(t, file)
		execAll(t, db,
			gooseDDL,
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1)`,
			schemaDDL(),
			`INSERT INTO sumika_schema VALUES ('chat', 1, 5, '', '2026-11-16T01:02:03Z')`,
		)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		_, _, err := Open(context.Background(), file, []Track{readyTrack()}, testOptions(file, new(int)))
		un := mustUnavailable(t, err)
		if un.Reason != ReasonTooNew {
			t.Fatalf("Reason = %s，要 too_new", un.Reason)
		}
		if !strings.Contains(un.Detail, "这个库比本程序新，请用新版本打开") {
			t.Fatalf("Detail = %q，没有 migrated_by 时也要说清楚", un.Detail)
		}
	})
}

// verifyRestored 单独比“sumika_schema 在不在”：Probe 出来的 SchemaRows 在表存在时总是非 nil，
// 正常路径上 DeepEqual 也会拦住表消失；这里直接构造 before，守住这条防线本身（复核 M08c）。
func TestVerifyRestoredChecksSchemaPresence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	chatGooseAt(t, file, 1)
	err := verifyRestored(context.Background(), file, nil, State{File: file, Exists: true, HasSchema: true})
	if err == nil || !strings.Contains(err.Error(), "sumika_schema") {
		t.Fatalf("错误 = %v，要报 sumika_schema 在不在和迁移前不同", err)
	}
}

// 只有 sumika_schema、没有 goose 表和用户表：按新建走，OpenDB 收到 fresh=true。
func TestOpenSchemaOnlyTreatedAsNew(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	db := openTestDB(t, file)
	execAll(t, db, schemaDDL(),
		`INSERT INTO sumika_schema VALUES ('memory-index', 1, 1, 'old', '2026-11-16T01:02:03Z')`)
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
	got, rep, err := Open(context.Background(), file, []Track{chatTrack(1)}, o)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if rep.Action != ActionCreate {
		t.Fatalf("Action = %v，要 create", rep.Action)
	}
	if len(fresh) != 1 || !fresh[0] {
		t.Fatalf("OpenDB fresh = %v，要 true", fresh)
	}
	if row := readSchema(t, got, "chat"); row.version != 1 {
		t.Fatalf("版本表行 = %+v，要 chat 1", row)
	}
}

// 0 字节之外，一张用户表都没有的库（空文件打开又关、只剩 sqlite_sequence）也按新建走。
func TestOpenNoUserTablesTreatedAsNew(t *testing.T) {
	setups := map[string]func(t *testing.T, file string){
		"空文件打开又关": func(t *testing.T, file string) {
			db := openTestDB(t, file)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		},
		"只剩 sqlite_sequence": func(t *testing.T, file string) {
			db := openTestDB(t, file)
			execAll(t, db,
				`CREATE TABLE gone (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`,
				`INSERT INTO gone (v) VALUES ('x')`,
				`DROP TABLE gone`,
			)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "chat.db")
			setup(t, file)
			calls := 0
			var fresh []bool
			o := testOptions(file, &calls)
			inner := o.OpenDB
			o.OpenDB = func(f string, isFresh bool) (*sql.DB, error) {
				fresh = append(fresh, isFresh)
				return inner(f, isFresh)
			}
			db, rep, err := Open(context.Background(), file, []Track{chatTrack(1)}, o)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if rep.Action != ActionCreate {
				t.Fatalf("Action = %v，要 create", rep.Action)
			}
			if len(fresh) != 1 || !fresh[0] {
				t.Fatalf("OpenDB fresh = %v，要 true", fresh)
			}
		})
	}
}

// Unavailable 包住底层错误：errors.Is 能穿过去。
func TestUnavailableUnwrapsCause(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.db")
	pendingChatDB(t, file)
	sentinel := errors.New("injected free space failure")
	o := testOptions(file, new(int))
	o.FreeSpace = func(string) (uint64, error) { return 0, sentinel }
	_, _, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonNoSpace {
		t.Fatalf("Reason = %s，要 no_space", un.Reason)
	}
	if !errors.Is(err, sentinel) || !errors.Is(un, sentinel) {
		t.Fatalf("错误链里没有底层错误：%v", err)
	}
}
