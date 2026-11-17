package storagestat

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/store"
)

// trace 有待跑迁移：存储统计只探查、跳过这次写，不替主程序迁移、不拍快照，也不报错给调用方。
func TestSaveSkipsTraceWithPendingMigration(t *testing.T) {
	p := newData(t)
	if err := os.MkdirAll(filepath.Dir(p.GoTraceDB()), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenTraceFile(p.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	// 把程序最新的那条 trace 迁移从 goose 表里拿掉：护栏看到“有待跑迁移”。
	if _, err := db.Exec(`DELETE FROM goose_db_version WHERE version_id = (SELECT max(version_id) FROM goose_db_version)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(p.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	sumBefore := fmt.Sprintf("%x", sha256.Sum256(before))

	var logs []string
	ts := TraceStore{File: p.GoTraceDB(), Logf: func(f string, args ...any) {
		logs = append(logs, fmt.Sprintf(f, args...))
	}}
	if err := ts.Save(context.Background(), Snapshot{Cats: map[string]CatSnap{}}, 1000, 1); err != nil {
		t.Fatalf("有待跑迁移时 Save 不该报错给调用方：%v", err)
	}
	after, err := os.ReadFile(p.GoTraceDB())
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(after)); got != sumBefore {
		t.Fatalf("存储统计替主程序改了 trace：%s → %s", sumBefore, got)
	}
	snaps, _ := filepath.Glob(filepath.Join(p.UpgradeSnapshotDir(), "*.sqlite"))
	if len(snaps) != 0 {
		t.Fatalf("存储统计拍出了快照：%v", snaps)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "跳过这次写回") {
		t.Fatalf("跳过时该记一行日志：%v", logs)
	}
}
