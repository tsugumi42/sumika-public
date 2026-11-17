package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/paths"
)

// 只看历史：库比程序新、但还在最低可读范围，也记一行“按兼容版本打开”。
func TestArchiveReadNewerLogsOnce(t *testing.T) {
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
	set, err := OpenGoReadOnlyOpts(context.Background(), p, DefaultRegistry, OpenOptions{Logf: func(f string, args ...any) {
		logs = append(logs, fmt.Sprintf(f, args...))
	}})
	if err != nil {
		t.Fatalf("较新能读的历史库不该拒绝打开：%v", err)
	}
	defer set.Close()
	if got := set.Platforms(); len(got) != 2 {
		t.Fatalf("较新能读的 qq 没打开：%v", got)
	}
	n := 0
	for _, line := range logs {
		if strings.Contains(line, "比程序新（库 9、程序 8），按兼容版本打开") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("较新能读的日志要正好一行，实际 %d 行：%v", n, logs)
	}
}

// trace 有待跑迁移：快照落在数据目录的 go/upgrade-snapshots，不是 snapshot_failed（规格第 5、8 条）。
func TestGuardedOpenTraceSnapshotUsesDataDir(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.GoTraceDB()), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := open(p.GoTraceDB(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateWith(db, "migrations/trace", 20, ""); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	set, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("有待跑迁移的 trace 该由护栏迁移打开：%v", err)
	}
	defer set.Close()
	snaps := snapshotsIn(p.UpgradeSnapshotDir())
	if len(snaps) != 1 {
		t.Fatalf("trace 迁移前该拍一份快照，实际 %v", snaps)
	}
	if strings.Contains(snaps[0], "-failed") || !strings.HasSuffix(snaps[0], ".sqlite") || !strings.Contains(snaps[0], "trace") {
		t.Fatalf("trace 的快照名不对：%s", snaps[0])
	}
	var status string
	for _, row := range set.DBStatus() {
		if row.Name == "trace" {
			status = row.Status
		}
	}
	if status != "ok" {
		t.Fatalf("db status 里 trace 是 %q，要 ok", status)
	}
}
