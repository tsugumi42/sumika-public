package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/datalock"
	"sumika-native/internal/memory"
)

// Bun 管本地私聊时它也写 memory.db（remember、memory_forget）。Go 不在、Bun 拿着 go/local.lock，迁移要拒绝，不碰任何记忆库（复核 R3）
func TestRunRefusesWhileBunHoldsLocalLock(t *testing.T) {
	data := t.TempDir()
	first, err := datalock.LockData(data)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	memDir := filepath.Join(data, "go", "characters", "编造甲")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memFile := filepath.Join(memDir, "memory.db")
	if err := os.WriteFile(memFile, []byte("不是库，迁移一碰就会报错"), 0o600); err != nil {
		t.Fatal(err)
	}
	bun, err := datalock.LockLocal(data)
	if err != nil {
		t.Fatal(err)
	}
	defer bun.Close()

	err = run(context.Background(), data, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Bun") {
		t.Fatalf("Bun 在写，该拒绝并说清：%v", err)
	}
	if b, _ := os.ReadFile(memFile); string(b) != "不是库，迁移一碰就会报错" {
		t.Fatal("拒绝时不该碰记忆库")
	}

	// 从没起过 Bun（local.lock 还不存在）：迁移照常开工，并且把 local.lock 建出来拿住——写的时候没人能起 Bun 去写
	fresh := t.TempDir()
	g0, err := datalock.LockData(fresh)
	if err != nil {
		t.Fatal(err)
	}
	_ = g0.Close()
	if err := os.MkdirAll(filepath.Join(fresh, "go", "characters", "编造乙"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = run(context.Background(), fresh, io.Discard) // 没有 memory.db，报“下面没有记忆库”；要的只是它拿过 local.lock
	if _, err := os.Stat(filepath.Join(fresh, "go", datalock.LocalLockName)); err != nil {
		t.Fatalf("迁移要写记忆库，该拿过 local.lock（没有就建）：%v", err)
	}

	// Sumika 还开着：照旧的提示
	_ = bun.Close()
	g, err := datalock.LockData(data)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := run(context.Background(), data, io.Discard); err == nil || !strings.Contains(err.Error(), "Sumika 还开着") {
		t.Fatalf("%v", err)
	}
}

// 三个角色里第二个的迁移会失败：第一、三个迁完，第二个换回快照并带 -failed，命令最后列出它、退出码 1。
func TestRunMigratesOthersAfterOneFails(t *testing.T) {
	data := t.TempDir()
	first, err := datalock.LockData(data)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	chars := []string{"编造甲", "编造乙", "编造丙"}
	for _, char := range chars {
		dir := filepath.Join(data, "go", "characters", char)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeBunMemoryDB(t, filepath.Join(dir, "memory.db"), char == "编造乙")
	}
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"-data", data}, &stdout, &stderr); code != 1 {
		t.Fatalf("退出码是 %d，要 1；stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, char := range []string{"编造甲", "编造丙"} {
		if !strings.Contains(stdout.String(), "迁好了："+char) {
			t.Fatalf("没迁 %s：%s", char, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "迁好了：编造乙") {
		t.Fatalf("失败的编造乙被算成迁好了：%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "编造乙") || !strings.Contains(stderr.String(), "migrate_failed") {
		t.Fatalf("最后没列出失败的角色和原因：%s", stderr.String())
	}
	snaps, err := filepath.Glob(filepath.Join(data, "go", "upgrade-snapshots", "*-failed.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || !strings.Contains(snaps[0], "编造乙") {
		t.Fatalf("失败快照是 %v，要一份编造乙的 -failed", snaps)
	}
	// 换回后是迁移前的库：没有 goose 表，facts 行还在
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(data, "go", "characters", "编造乙", "memory.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var goose int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'goose_db_version'`).Scan(&goose); err != nil {
		t.Fatal(err)
	}
	if goose != 0 {
		t.Fatal("换回后还留着 goose 表，库没回到迁移前")
	}
	var facts int
	if err := db.QueryRow(`SELECT count(*) FROM facts`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("换回后 facts 有 %d 行，要 1", facts)
	}
}

// writeBunMemoryDB 造一个 Bun 版样子的 memory.db；conflict 为真时多一张 item_scopes，
// 让 0001 号迁移的 CREATE TABLE item_scopes 失败（迁移本身在同一个事务里回滚，护栏再换回快照）。
func writeBunMemoryDB(t *testing.T, file string, conflict bool) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := memory.BaseSchema + `
INSERT INTO facts (id, kind, text, visibility, created_at) VALUES ('F1', 'fact', '编的事实', 'private', 1);
`
	if conflict {
		schema += `CREATE TABLE item_scopes (ref TEXT PRIMARY KEY, scope TEXT);`
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
}

// 编造乙的 goose 说三个迁移都跑过，写入侧的表和列其实没有：MigrateGuarded 照旧返回，CheckSchema 揭出来。
// 命令接着迁编造丙，最后列出编造乙、退出码 1（复核 K16）。
func TestRunContinuesAfterCheckSchemaFails(t *testing.T) {
	data := t.TempDir()
	first, err := datalock.LockData(data)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	for _, char := range []string{"编造甲", "编造乙", "编造丙"} {
		dir := filepath.Join(data, "go", "characters", char)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeBunMemoryDB(t, filepath.Join(dir, "memory.db"), false)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(data, "go", "characters", "编造乙", "memory.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY AUTOINCREMENT, version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL, tstamp TIMESTAMP DEFAULT (datetime('now')));
		INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (1, 1), (2, 1), (3, 1);`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"-data", data}, &stdout, &stderr); code != 1 {
		t.Fatalf("退出码是 %d，要 1；stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, char := range []string{"编造甲", "编造丙"} {
		if !strings.Contains(stdout.String(), "迁好了："+char) {
			t.Fatalf("编造乙检查失败以后没接着迁 %s：%s", char, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "迁好了：编造乙") {
		t.Fatalf("检查失败的编造乙被算成迁好了：%s", stdout.String())
	}
	for _, want := range []string{"编造乙", "迁移后检查", "memory-migrate"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("最后没列出失败的角色、阶段和去处（要含 %q）：%s", want, stderr.String())
		}
	}
}
