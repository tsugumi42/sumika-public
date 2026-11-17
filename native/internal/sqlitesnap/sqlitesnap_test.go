package sqlitesnap

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func openRW(t *testing.T, file string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+uriPath(file)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // 和各库一样的单连接池
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s：%v", q, err)
	}
}

func noSidecars(t *testing.T, file string) {
	t.Helper()
	for _, s := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(file + s); err == nil {
			t.Errorf("副本旁边不该有 %s", file+s)
		}
	}
}

func TestIntoAndInspect(t *testing.T) {
	dir := t.TempDir()
	db := openRW(t, filepath.Join(dir, "a.db"))
	mustExec(t, db, `CREATE TABLE events (seq INTEGER PRIMARY KEY, body TEXT)`)
	mustExec(t, db, `CREATE TABLE empty_t (x INTEGER)`)
	for i := 0; i < 50; i++ {
		mustExec(t, db, `INSERT INTO events (body) VALUES (?)`, "x")
	}
	dst := filepath.Join(dir, "snap", "a.db")
	if err := Into(context.Background(), db, dst); err != nil {
		t.Fatal(err)
	}
	rep, err := Inspect(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.Tables["events"] != 50 || rep.Tables["empty_t"] != 0 {
		t.Fatalf("检查结果不对：%+v", rep)
	}
	noSidecars(t, dst)
}

func TestIntoRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	db := openRW(t, filepath.Join(dir, "a.db"))
	mustExec(t, db, `CREATE TABLE t (x INTEGER)`)
	dst := filepath.Join(dir, "x.db")
	if err := os.WriteFile(dst, []byte("别的东西"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Into(context.Background(), db, dst); err == nil {
		t.Fatal("目标已经存在时应该拒绝，不能盖掉别的文件")
	}
	if b, _ := os.ReadFile(dst); string(b) != "别的东西" {
		t.Fatal("已有的文件被改了")
	}
}

func TestIntoInMemoryDB(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	mustExec(t, db, `CREATE TABLE t (x INTEGER)`)
	mustExec(t, db, `INSERT INTO t VALUES (1), (2)`)
	dst := filepath.Join(t.TempDir(), "m.db")
	if err := Into(context.Background(), db, dst); err != nil {
		t.Fatal(err)
	}
	if rep, err := Inspect(context.Background(), dst); err != nil || rep.Tables["t"] != 2 {
		t.Fatalf("内存库的快照不对：%+v %v", rep, err)
	}
}

// 快照时池里唯一的那条连接不被占着：另一个协程照常写，写入不用等快照做完
func TestIntoDoesNotHoldThePool(t *testing.T) {
	dir := t.TempDir()
	db := openRW(t, filepath.Join(dir, "a.db"))
	mustExec(t, db, `CREATE TABLE t (n INTEGER)`)
	mustExec(t, db, `INSERT INTO t SELECT 1 FROM (WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 20000) SELECT x FROM c)`)
	var writes atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := db.Exec(`INSERT INTO t VALUES (2)`); err != nil {
				return
			}
			writes.Add(1)
		}
	}()
	for writes.Load() == 0 { // 等写入真的开始
		time.Sleep(time.Millisecond)
	}
	before := writes.Load()
	if err := Into(context.Background(), db, filepath.Join(dir, "s.db")); err != nil {
		t.Fatal(err)
	}
	close(stop)
	<-done
	if writes.Load() < before {
		t.Fatal("计数倒退？")
	}
	rep, err := Inspect(context.Background(), filepath.Join(dir, "s.db"))
	if err != nil || !rep.OK() || rep.Tables["t"] < 20000 {
		t.Fatalf("快照不对：%+v %v", rep, err)
	}
}

// 带了本驱动没有模块的虚表（Bun 记忆库的 vec0）：VACUUM INTO、integrity_check、数行数三步都要能过，虚表不数
func TestUnknownModuleVirtualTable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "v.db")
	db := openRW(t, file)
	// 哑表：这里只测快照工具，不看记忆库结构，不改成 BaseSchema。
	mustExec(t, db, `CREATE TABLE facts (id INTEGER PRIMARY KEY, text TEXT)`)
	mustExec(t, db, `INSERT INTO facts (text) VALUES ('a'), ('b'), ('c')`)
	mustExec(t, db, `CREATE VIRTUAL TABLE vec_items USING fts5(text)`)
	mustExec(t, db, `INSERT INTO vec_items (text) VALUES ('hello')`)
	// 把模块名改成本驱动没有的 vec0（改 sqlite_master 的文本，库里的页不动，影子表还在）
	mustExec(t, db, `PRAGMA writable_schema = ON`)
	mustExec(t, db, `UPDATE sqlite_master SET sql = replace(sql, 'fts5', 'vec0') WHERE name = 'vec_items'`)
	mustExec(t, db, `PRAGMA writable_schema = OFF`)
	db.Close()

	// 重新开一条连接当持有方的连接（缺模块的库照常能开）
	db2 := openRW(t, file)
	if _, err := db2.Exec(`SELECT * FROM vec_items`); err == nil || !strings.Contains(err.Error(), "vec0") {
		t.Fatalf("前提不成立：这个库里的虚表应该因为缺模块读不了，实际 %v", err)
	}
	dst := filepath.Join(dir, "snap.db")
	if err := Into(context.Background(), db2, dst); err != nil {
		t.Fatalf("带缺模块虚表的库取快照失败：%v", err)
	}
	rep, err := Inspect(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("integrity_check 没过：%q", rep.Integrity)
	}
	if rep.Tables["facts"] != 3 {
		t.Fatalf("普通表行数不对：%+v", rep.Tables)
	}
	if _, ok := rep.Tables["vec_items"]; ok {
		t.Fatalf("虚表不该被数：%+v", rep.Tables)
	}
}

func TestInspectDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	db := openRW(t, filepath.Join(dir, "a.db"))
	mustExec(t, db, `CREATE TABLE t (n INTEGER, pad TEXT)`)
	for i := 0; i < 400; i++ {
		mustExec(t, db, `INSERT INTO t VALUES (?, ?)`, i, strings.Repeat("x", 200))
	}
	dst := filepath.Join(dir, "s.db")
	if err := Into(context.Background(), db, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	for i := 4096 * 2; i < len(b) && i < 4096*3; i++ { // 把中间一页写花
		b[i] = 0xFF
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Inspect(context.Background(), dst)
	if err == nil && rep.OK() {
		t.Fatal("写花的副本不该通过检查")
	}
}
