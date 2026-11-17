package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/paths"
)

// 平台库迁移失败：只停这个平台，别的平台照常；For 给出带原因的错误，Unavailable 记下原因和快照；
// 去掉注入再开一次，自动迁移成功、平台回到可用。
func TestPlatformUnavailableStopsOnlyThatPlatform(t *testing.T) {
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

	set, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("qq 库打不开时整组也不该失败：%v", err)
	}
	if got := set.Platforms(); len(got) != 1 || got[0] != "local" {
		t.Fatalf("打开的平台是 %v，应该只有 local", got)
	}
	u, ok := set.Unavailable()["qq"]
	if !ok {
		t.Fatal("Unavailable 里没有 qq")
	}
	if u.Reason != dbguard.ReasonMigrateFailed {
		t.Fatalf("原因 %q，要 %q：%v", u.Reason, dbguard.ReasonMigrateFailed, u)
	}
	if u.Snapshot == "" || !strings.Contains(u.Snapshot, "-failed") {
		t.Fatalf("失败快照路径不对：%q", u.Snapshot)
	}
	if _, err := set.For("qq"); err == nil {
		t.Fatal("For(qq) 给出了打不开的平台")
	} else {
		var got *dbguard.Unavailable
		if !errors.As(err, &got) {
			t.Fatalf("For(qq) 的错误里取不到 *dbguard.Unavailable：%v", err)
		}
		if !strings.Contains(err.Error(), "平台 qq 的库没打开") || !strings.Contains(err.Error(), u.Reason) {
			t.Fatalf("For(qq) 的错误没说清平台和原因：%v", err)
		}
	}
	if _, err := set.ForConversation("qq:group:1"); err == nil {
		t.Fatal("ForConversation(qq) 给出了打不开的平台")
	} else {
		var got *dbguard.Unavailable
		if !errors.As(err, &got) {
			t.Fatalf("ForConversation 的错误里取不到 *dbguard.Unavailable：%v", err)
		}
	}
	if _, ok := set.Platform("qq"); ok {
		t.Fatal("Platform(qq) 说打不开的平台还在")
	}
	for _, n := range set.Each() {
		if n.Store == nil || n.Platform != "local" {
			t.Fatalf("Each 给出了 %+v，应该只有 local", n)
		}
	}
	// 本地照常能写能读。
	local, err := set.For("console")
	if err != nil {
		t.Fatalf("本地库没打开：%v", err)
	}
	if _, err := local.AppendEvent(NewEvent{Channel: "console", Conversation: "console:mio", ActorKind: "owner", ActorID: "owner", Kind: "message", Text: "本地照常", CreatedAt: 3}, nil); err != nil {
		t.Fatalf("本地库不能写：%v", err)
	}
	var text string
	if err := local.Events.QueryRow(`SELECT text FROM events WHERE conversation='console:mio'`).Scan(&text); err != nil || text != "本地照常" {
		t.Fatalf("本地库读回来 %q，%v", text, err)
	}
	// qq.db 回到迁移前：chat 16、qq 7。
	rw, err := open(p.ChatDB("qq"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := gooseMax(t, rw, "goose_db_version"); got != 16 {
		t.Fatalf("chat 轨在 %d，换回后要 16", got)
	}
	if got := gooseMax(t, rw, "goose_db_version_qq"); got != 7 {
		t.Fatalf("qq 轨在 %d，换回后要 7", got)
	}
	rw.Close()

	// db status 的数据也记下了。
	status := set.DBStatus()
	var qqRow *DBFileStatus
	for i := range status {
		if status[i].Name == "qq" {
			qqRow = &status[i]
		}
	}
	if qqRow == nil || qqRow.Status != "unavailable" || qqRow.Reason != dbguard.ReasonMigrateFailed || qqRow.Snapshot != u.Snapshot {
		t.Fatalf("db status 里 qq 不是不可用：%+v", qqRow)
	}
	if len(qqRow.Tracks) == 0 || qqRow.Tracks[0].Latest == 0 {
		t.Fatalf("db status 里 qq 的迁移轨没记下：%+v", qqRow)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	// 去掉注入再开一次：自动迁移成功，qq 回到可用，S14 的数据是空的。
	migrationFS = old
	set2, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("注入去掉后还不能开：%v", err)
	}
	defer set2.Close()
	if got := set2.Platforms(); len(got) != 2 || got[0] != "local" || got[1] != "qq" {
		t.Fatalf("迁移成功后打开的平台是 %v，要 local、qq", got)
	}
	if len(set2.Unavailable()) != 0 {
		t.Fatalf("迁移成功后还有不可用的平台：%v", set2.Unavailable())
	}
	if _, err := set2.For("qq"); err != nil {
		t.Fatalf("迁移成功后 qq 还是取不到：%v", err)
	}
	if got := set2.Trace(); got == nil {
		t.Fatal("trace 没打开")
	}
}

// trace 是核心块：打不开就整组失败，不能像平台库那样只停一块。
func TestTraceUnavailableFailsWholeOpen(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.GoTraceDB()), 0o755); err != nil {
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
	if _, err := OpenGo(p, "local", "qq"); err == nil {
		t.Fatal("trace 太新，OpenGo 却成功了")
	} else {
		var u *dbguard.Unavailable
		if !errors.As(err, &u) {
			t.Fatalf("错误里取不到 *dbguard.Unavailable：%v", err)
		}
		if u.Reason != dbguard.ReasonTooNew {
			t.Fatalf("原因 %q，要 %q：%v", u.Reason, dbguard.ReasonTooNew, err)
		}
	}
}

// 库比程序新、但 min_reader 还在能读的范围：照常打开、不迁移、版本表不动（规格第 3 条第 4 步）。
func TestReadNewerPlatformStaysOpen(t *testing.T) {
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
	old := migrationFS
	migrationFS = migrationsWithExtra(t, map[string]string{
		"migrations/qq/0009_compatible_newer.sql": `-- sumika: compatible
-- +goose Up
CREATE TABLE qq_newer(x INTEGER);
`,
	})
	newer, err := OpenGo(p, "local", "qq")
	if err != nil {
		migrationFS = old
		t.Fatalf("先用较新的迁移把库升上去：%v", err)
	}
	if err := newer.Close(); err != nil {
		migrationFS = old
		t.Fatal(err)
	}
	migrationFS = old

	var logs []string
	set, err := OpenGoOpts(p, DefaultRegistry, OpenOptions{Logf: func(f string, args ...any) {
		logs = append(logs, fmt.Sprintf(f, args...))
	}})
	if err != nil {
		t.Fatalf("较新能读的库不该拒绝打开：%v", err)
	}
	defer set.Close()
	n := 0
	for _, line := range logs {
		if strings.Contains(line, "比程序新（库 9、程序 8），按兼容版本打开") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("较新能读的日志要正好一行，实际 %d 行：%v", n, logs)
	}
	if got := set.Platforms(); len(got) != 2 {
		t.Fatalf("较新能读的 qq 没打开：%v", got)
	}
	if len(set.Unavailable()) != 0 {
		t.Fatalf("较新能读被当成不可用：%v", set.Unavailable())
	}
	var qq *DBFileStatus
	for i := range set.dbs {
		if set.dbs[i].Name == "qq" {
			qq = &set.dbs[i]
		}
	}
	if qq == nil {
		t.Fatal("db status 里没有 qq")
	}
	for _, tr := range qq.Tracks {
		if tr.Track == "qq" && (tr.Version != 9 || tr.Latest != 8 || tr.MinReader > tr.Latest) {
			t.Fatalf("较新能读的 qq 轨记成 %+v，要库 9 / 程序 8 / min_reader 不超过 8", tr)
		}
	}
}
