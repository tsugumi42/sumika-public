package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/paths"
	"sumika-native/internal/sqlitesnap"
)

// writeOldPlatform 造一个没有 sumika_schema 的旧平台库：chat 和平台轨各只跑到 chatTo、ownTo。
// 用 open + migrateWith，不走护栏，模拟这份规格之前的程序建出来的库。
func writeOldPlatform(t *testing.T, p paths.Paths, platform string, chatTo, ownTo int64) *sql.DB {
	t.Helper()
	file := p.ChatDB(platform)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := open(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateWith(db, "migrations/chat", chatTo, ""); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := migrateWith(db, "migrations/"+platform, ownTo, "goose_db_version_"+platform); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func gooseMax(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var v sql.NullInt64
	if err := db.QueryRow("SELECT max(version_id) FROM " + table + " WHERE is_applied").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.Int64
}

func snapshotsIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sqlite") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func sha256Of(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// 第一次打开没有版本表的旧库：拍快照、迁到最新、写版本表、数据还在。
func TestGuardedOpenMigratesOldPlatformDB(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := writeOldPlatform(t, p, "qq", 14, 6)
	if _, err := db.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at)
VALUES ('old-1', 'qq', 'qq:group:1', 'human', '1', 'message', '旧消息', 1);
INSERT INTO qq_people (uid, nickname) VALUES ('u1', '甲')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	s, err := OpenGo(p, "qq")
	if err != nil {
		t.Fatalf("旧库该由护栏迁移打开：%v", err)
	}
	defer s.Close()
	qq, ok := s.Platform("qq")
	if !ok {
		t.Fatal("qq 库没打开")
	}
	chatLatest, err := latestMigration("migrations/chat")
	if err != nil {
		t.Fatal(err)
	}
	qqLatest, err := latestMigration("migrations/qq")
	if err != nil {
		t.Fatal(err)
	}
	if got := gooseMax(t, qq.Events, "goose_db_version"); got != chatLatest {
		t.Fatalf("chat 轨在 %d，要 %d", got, chatLatest)
	}
	if got := gooseMax(t, qq.Events, "goose_db_version_qq"); got != qqLatest {
		t.Fatalf("qq 轨在 %d，要 %d", got, qqLatest)
	}
	rows, err := qq.Events.Query(`SELECT track, version, min_reader, migrated_by FROM sumika_schema ORDER BY track`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]struct {
		version, minReader int64
		by                 string
	}{}
	for rows.Next() {
		var name, by string
		var version, minReader int64
		if err := rows.Scan(&name, &version, &minReader, &by); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got[name] = struct {
			version, minReader int64
			by                 string
		}{version, minReader, by}
	}
	rows.Close()
	if len(got) != 2 || got["chat"].version != chatLatest || got["chat"].minReader == 0 || got["chat"].by == "" ||
		got["qq"].version != qqLatest || got["qq"].minReader == 0 || got["qq"].by == "" {
		t.Fatalf("版本表不对：%+v", got)
	}
	var events, people int
	if err := qq.Events.QueryRow(`SELECT count(*) FROM events WHERE id = 'old-1'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("旧消息没了：%d %v", events, err)
	}
	if err := qq.Events.QueryRow(`SELECT count(*) FROM qq_people WHERE uid = 'u1'`).Scan(&people); err != nil || people != 1 {
		t.Fatalf("旧 QQ 人没了：%d %v", people, err)
	}
	snaps := snapshotsIn(p.UpgradeSnapshotDir())
	if len(snaps) != 1 {
		t.Fatalf("迁移前该拍一份快照，实际 %v", snaps)
	}
	if strings.Contains(snaps[0], "-failed") {
		t.Fatalf("迁移成功了，快照不该带 -failed：%s", snaps[0])
	}
	rep, err := sqlitesnap.Inspect(context.Background(), snaps[0])
	if err != nil || !rep.OK() {
		t.Fatalf("快照不通过：%+v %v", rep, err)
	}
}

// migrationsWithExtra 复制内嵌迁移，再放几份注入的文件：只有测试这样造迁移文件系统。
func migrationsWithExtra(t *testing.T, extra map[string]string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	err := fs.WalkDir(migrations, "migrations", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(migrations, name)
		if err != nil {
			return err
		}
		out[name] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range extra {
		out[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

// 平台轨迁移失败：只停这个平台（Set 记下 *dbguard.Unavailable），chat 轨也回到迁移前，快照带 -failed。
func TestGuardedOpenPlatformFailureStopsOnlyThatPlatform(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := writeOldPlatform(t, p, "qq", 16, 7)
	if _, err := db.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at)
VALUES ('keep-1', 'qq', 'qq:group:1', 'human', '1', 'message', '要留住', 2)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	old := migrationFS
	migrationFS = migrationsWithExtra(t, map[string]string{
		"migrations/qq/0009_guard_failure.sql": `-- sumika: breaking
-- +goose Up
CREATE TABLE qq_guard_failure(x INTEGER);
INSERT INTO no_such_table(x) VALUES (1);
`,
	})
	defer func() { migrationFS = old }()

	set, err := OpenGo(p, "qq")
	if err != nil {
		t.Fatalf("平台轨迁移失败不该让整组起不来：%v", err)
	}
	defer set.Close()
	if got := set.Platforms(); len(got) != 0 {
		t.Fatalf("迁移失败的 qq 还在打开的平台里：%v", got)
	}
	if _, err := set.For("qq"); err == nil {
		t.Fatal("迁移失败的 qq 还能取到库")
	} else {
		var u *dbguard.Unavailable
		if !errors.As(err, &u) {
			t.Fatalf("For(qq) 的错误里取不到 *dbguard.Unavailable：%v", err)
		}
		if u.Reason != dbguard.ReasonMigrateFailed {
			t.Fatalf("原因 %q，要 %q：%v", u.Reason, dbguard.ReasonMigrateFailed, err)
		}
		if u.Snapshot == "" || !strings.Contains(u.Snapshot, "-failed") {
			t.Fatalf("失败快照路径不对：%q", u.Snapshot)
		}
		if _, err := os.Stat(u.Snapshot); err != nil {
			t.Fatalf("失败快照不在：%v", err)
		}
	}

	rw, err := open(p.ChatDB("qq"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if got := gooseMax(t, rw, "goose_db_version"); got != 16 {
		t.Fatalf("chat 轨在 %d，换回后要 16", got)
	}
	if got := gooseMax(t, rw, "goose_db_version_qq"); got != 7 {
		t.Fatalf("qq 轨在 %d，换回后要 7", got)
	}
	for _, table := range []string{"ref_blocks", "qq_guard_failure"} {
		var n int
		if err := rw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("换回后还留着迁移建的表 %s：%d %v", table, n, err)
		}
	}
	var acked int
	if err := rw.QueryRow(`SELECT count(*) FROM pragma_table_info('channel_gaps') WHERE name = 'acknowledged_ms'`).Scan(&acked); err != nil || acked != 0 {
		t.Fatalf("换回后还留着 qq 第 8 号迁移加的列：%d %v", acked, err)
	}
	var n int
	if err := rw.QueryRow(`SELECT count(*) FROM events WHERE id = 'keep-1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("换回后数据不对：%d %v", n, err)
	}
	if err := rw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'sumika_schema'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("迁移失败不该留下版本表：%d %v", n, err)
	}
}

// 太新：可写打开和归档只读都拒绝；主文件 SHA-256 不变。
func TestGuardedOpenRejectsTooNew(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rw, err := open(p.ChatDB("qq"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`UPDATE sumika_schema SET min_reader = 999 WHERE track = 'qq'`); err != nil {
		rw.Close()
		t.Fatal(err)
	}
	if _, err := rw.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		rw.Close()
		t.Fatal(err)
	}
	rw.Close()
	before := sha256Of(t, p.ChatDB("qq"))

	set, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("qq 太新只该停 qq：%v", err)
	}
	if got := set.Platforms(); len(got) != 1 || got[0] != "local" {
		t.Fatalf("太新的 qq 还在打开的平台里：%v", got)
	}
	if _, err := set.For("qq"); err == nil {
		t.Fatal("太新的 qq 还能取到库")
	} else {
		var u *dbguard.Unavailable
		if !errors.As(err, &u) || u.Reason != dbguard.ReasonTooNew {
			t.Fatalf("For(qq) 没有按太新拒绝：%v", err)
		}
		if !strings.Contains(err.Error(), "被较新的 Sumika") {
			t.Fatalf("错误没说清被较新的程序升级过：%v", err)
		}
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := OpenGoReadOnlyWith(context.Background(), p, DefaultRegistry)
	if err != nil {
		t.Fatalf("归档只读不该被一个太新的平台挡住：%v", err)
	}
	defer archive.Close()
	if got := archive.Platforms(); len(got) != 1 || got[0] != "local" {
		t.Fatalf("归档打开的平台是 %v，要只有 local", got)
	}
	if got := archive.Unavailable()["qq"]; got == nil || got.Reason != dbguard.ReasonTooNew {
		t.Fatalf("归档没把太新的 qq 记成不可用：%+v", archive.Unavailable())
	}
	var found bool
	for _, row := range archive.DBStatus() {
		if row.Name == "qq" {
			found = true
			if row.Status != "unavailable" || row.Reason != dbguard.ReasonTooNew || row.Path == "" {
				t.Fatalf("归档 db status 里 qq：%+v", row)
			}
		}
		if row.Name == "trace" && (row.Status != "ok" || len(row.Tracks) == 0 || row.Tracks[0].Latest == 0) {
			t.Fatalf("归档 db status 里 trace：%+v", row)
		}
	}
	if !found {
		t.Fatal("归档 db status 里没有 qq")
	}
	if after := sha256Of(t, p.ChatDB("qq")); after != before {
		t.Fatalf("拒绝前后主文件变了：%s → %s", before, after)
	}
}

// trace 是核心块：归档只读打开时 trace 太新仍然整组失败（平台库可以只停一个，trace 不行）。
func TestArchiveTraceTooNewFailsWholeOpen(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	tr, err := OpenTraceFile(p.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Exec(`UPDATE sumika_schema SET min_reader = 999`); err != nil {
		tr.Close()
		t.Fatal(err)
	}
	tr.Close()
	var u *dbguard.Unavailable
	if _, err := OpenGoReadOnlyWith(context.Background(), p, DefaultRegistry); err == nil {
		t.Fatal("归档只读打开却没拒绝太新的 trace")
	} else if !errors.As(err, &u) || u.Reason != dbguard.ReasonTooNew {
		t.Fatalf("归档只读没有按太新拒绝 trace：%v", err)
	}
}

// 测试模板在写完版本表之后才存：模板本身就带 sumika_schema，拷模板建出的库也带。
func TestGuardedTemplateCarriesSchema(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ChatDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := OpenChatFile(p.ChatDB("qq"), "qq")
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	tracks, err := platformTracks("qq")
	if err != nil {
		t.Fatal(err)
	}
	tpl := templateBytes(tracksTemplateKey(tracks))
	if tpl == nil {
		t.Fatal("第一个新建的库迁移完该留下测试模板")
	}
	tplFile := filepath.Join(t.TempDir(), "template.db")
	if err := os.WriteFile(tplFile, tpl, 0o644); err != nil {
		t.Fatal(err)
	}
	tdb, err := OpenReadOnlyFile(tplFile)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := tdb.QueryRow(`SELECT count(*) FROM sumika_schema`).Scan(&rows); err != nil || rows != 2 {
		tdb.Close()
		t.Fatalf("模板里没有两条轨的版本行：%d %v", rows, err)
	}
	tdb.Close()

	secondFile := filepath.Join(filepath.Dir(p.ChatDB("qq")), "qq-template-copy.db")
	second, err := OpenChatFile(secondFile, "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.QueryRow(`SELECT count(*) FROM sumika_schema`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("模板拷出来的库没有版本表：%d %v", rows, err)
	}
}
