package sqlitesnap

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 没有人占着写锁：拿到写锁很快；探一下什么数据也没写（行数、user_version、文件内容都没变）
func TestWriteLockWaitWritesNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.db")
	db := openRW(t, file)
	mustExec(t, db, `CREATE TABLE t (n INTEGER)`)
	mustExec(t, db, `INSERT INTO t VALUES (1), (2)`)
	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		d, err := WriteLockWait(context.Background(), db)
		if err != nil || d < 0 || d > 2*time.Second {
			t.Fatalf("没人占着时该很快拿到：%v %v", d, err)
		}
	}
	var n, ver2 int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("探测不该改数据：%d %v", n, err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver2); err != nil || ver2 != ver {
		t.Fatalf("探测不该改 user_version：%d %d %v", ver, ver2, err)
	}
	// 探完连接还回池里了，事务没留着：池里这条连接还能正常写
	mustExec(t, db, `INSERT INTO t VALUES (3)`)
}

// 别的连接占着写锁时，探测要等到它放手，量到的就是这段等待
func TestWriteLockWaitSeesAnotherWriter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.db")
	db := openRW(t, file)
	mustExec(t, db, `CREATE TABLE t (n INTEGER)`)
	other := openRW(t, file)
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	got := make(chan time.Duration, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		d, err := WriteLockWait(context.Background(), db)
		if err != nil {
			t.Errorf("别人放手后该拿到：%v", err)
		}
		got <- d
	}()
	<-started
	hold := 150 * time.Millisecond
	time.Sleep(hold) // 这里的睡眠是在“造”被量的那段等待，不是在等什么事件
	if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if d := <-got; d < hold-30*time.Millisecond {
		t.Fatalf("别的连接占了 %v 的写锁，探测该量到差不多这么久：%v", hold, d)
	}
}

// 连不上、被取消、只读连接拿不了写锁：返回错误，不挂住，也不把连接留在事务里
func TestWriteLockWaitFailures(t *testing.T) {
	if _, err := WriteLockWait(context.Background(), nil); err == nil {
		t.Error("没有连接该报错")
	}
	file := filepath.Join(t.TempDir(), "a.db")
	db := openRW(t, file)
	mustExec(t, db, `CREATE TABLE t (n INTEGER)`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WriteLockWait(ctx, db); err == nil {
		t.Error("已经取消的 context 该报错")
	}
	mustExec(t, db, `INSERT INTO t VALUES (1)`) // 池没被卡住
	// query_only 的连接（archive 模式的写法）：拿不了写锁，如实报错
	mustExec(t, db, `PRAGMA query_only = ON`)
	if _, err := WriteLockWait(context.Background(), db); err == nil {
		t.Error("query_only 的连接拿不了写锁，该报错而不是假装量到了")
	}
}

// 备份结束时采样器会取消探测：取消落在哪一步都不能把开着的事务还给池。
// 各库的池只有一条连接，留着一个不提交的事务，后面所有入库写入都会写进它里面、对别的连接不可见。
// 这个竞争是概率性的（取消要正好落在 BEGIN 生效、驱动还没返回的那一瞬间）：多轮、每轮几千次，改坏版本（BEGIN 跟着 ctx、不无条件 ROLLBACK）
// 单轮约三成会漏出事务，十轮下来九成七以上会红（mutation-12）
func TestWriteLockWaitCancelNeverLeavesTransaction(t *testing.T) {
	for round := 0; round < 10; round++ {
		file := filepath.Join(t.TempDir(), "a.db")
		db := openRW(t, file)
		mustExec(t, db, `CREATE TABLE t (n INTEGER)`)
		other := openRW(t, file)
		stop := make(chan struct{})
		writerDone := make(chan struct{})
		go func() { // 入库的写入一直在跑，和探测抢同一条连接
			defer close(writerDone)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := db.Exec(`INSERT INTO t VALUES (?)`, i); err != nil {
					return
				}
			}
		}()
		for i := 0; i < 2000; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%120)*8*time.Microsecond)
			_, _ = WriteLockWait(ctx, db)
			cancel()
		}
		close(stop)
		<-writerDone
		mustExec(t, db, `INSERT INTO t VALUES (-42)`)
		var n int
		if err := other.QueryRow(`SELECT count(*) FROM t WHERE n = -42`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("第 %d 轮：池里那条连接留着没提交的事务：另一条连接读不到刚写的行（%d %v）", round, n, err)
		}
		if _, err := WriteLockWait(context.Background(), db); err != nil {
			t.Fatalf("取消那么多次以后还能正常探：%v", err)
		}
	}
}
