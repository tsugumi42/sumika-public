package store

// 两个进程同时写 trace.db 和 memory.db，各 1000 行，一行不丢、不报 busy（规格-本地私聊切到Bun 第三节、第九节）。
// 这两个库本来就是两个后台共写的：trace.db 里 Go 写 QQ 的回合、Bun 写本地的回合，memory.db 里 Bun 写本地私聊的第一档写入、Go 写 QQ 的和整理。
// 这里测的是 SQLite WAL 的多进程写在真实的打开方式下（busy_timeout 5000、Go 逐句提交、Bun 的 BEGIN IMMEDIATE）扛得住，
// 不是记忆库的表结构：memory.db 用一张最简单的表，真实表结构的写入由各自的测试管。
// 对端是真的 bun 进程；机器上没有 bun 就跳过。

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTwoProcessesWriteTraceAndMemoryWithoutLosingRows(t *testing.T) {
	t.Parallel()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("机器上没有 bun，跳过两个进程同写的测试")
	}
	const n = 1000
	dir := t.TempDir()
	traceFile, memoryFile := filepath.Join(dir, "trace.db"), filepath.Join(dir, "memory.db")
	trace, err := OpenTraceFile(traceFile)
	if err != nil {
		t.Fatal(err)
	}
	defer trace.Close()
	memory, err := open(memoryFile, false)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	for _, q := range []string{"PRAGMA journal_mode = WAL", "CREATE TABLE dual_rows(who TEXT NOT NULL, n INTEGER NOT NULL)"} {
		if _, err := memory.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(bun, filepath.Join("testdata", "dualwrite.ts"), traceFile, memoryFile, fmt.Sprint(n))
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Go 这边和它同时写：trace 里写 steps，memory 里写 dual_rows，都是一句一提交
	goErr := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			if _, err := trace.Exec(`INSERT INTO steps(id,character_id,conversation,kind,status) VALUES(?,?,?, 'tool','done')`, fmt.Sprintf("go-%d", i), "c1", "qq:group:1"); err != nil {
				goErr <- fmt.Errorf("Go 写 trace 第 %d 行：%w", i, err)
				return
			}
			if _, err := memory.Exec(`INSERT INTO dual_rows(who,n) VALUES('go',?)`, i); err != nil {
				goErr <- fmt.Errorf("Go 写 memory 第 %d 行：%w", i, err)
				return
			}
		}
		goErr <- nil
	}()
	if err := <-goErr; err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Bun 那边出错了：%v\n%s", err, out.String())
	}

	count := func(q string, args ...any) int {
		var c int
		if err := trace.QueryRow(q, args...).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if g, b := count(`SELECT count(*) FROM steps WHERE id LIKE 'go-%'`), count(`SELECT count(*) FROM steps WHERE id LIKE 'bun-%'`); g != n || b != n {
		t.Fatalf("trace.db 两边各该 %d 行：Go %d、Bun %d", n, g, b)
	}
	for _, who := range []string{"go", "bun"} {
		var c int
		if err := memory.QueryRow(`SELECT count(*) FROM dual_rows WHERE who = ?`, who).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != n {
			t.Fatalf("memory.db 里 %s 该写了 %d 行，实际 %d", who, n, c)
		}
	}
	var integrity string
	if err := trace.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("trace.db 完整性检查：%q %v", integrity, err)
	}
}
