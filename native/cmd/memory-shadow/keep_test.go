package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// -keep 的两个边角：标记 .keep 记不上时日志不能自相矛盾；读不清 .keep 时清理要按"不清"处理

// -keep 的运行结束时 .keep 记不上：先说记不上、副本以后可能被清；收尾那一行不能再说"以后的运行不会清它"（读的人会信错的那句）。
// 对照是记得上的那一次：只有收尾那一句，没有"记不上"
func TestKeepMarkFailureDoesNotClaimItWillSurvive(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		breakMark bool
	}{{"标记记得上", false}, {"标记记不上", true}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := richFixture(t)
			work := filepath.Join(r.Data, "dev", "memory-shadow", "keep-mark")
			var once sync.Once
			g := &recGen{reply: func(prompt string) string {
				if c.breakMark {
					// 运行中途（-work 要求一开始是空目录，只能这时候放）在 .keep 的位置放一个目录：收尾写 .keep 就写不进去
					once.Do(func() {
						if err := os.MkdirAll(filepath.Join(work, keepName), 0o755); err != nil {
							t.Error(err)
						}
					})
				}
				rep, _ := demoGen{}.Call(context.Background(), "", prompt, "")
				return rep.Text
			}}
			var logs []string
			cfg := r.cfg(g, modeFake)
			cfg.Work, cfg.Keep = work, true
			cfg.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
			res, err := runShadow(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if res.Kept == "" {
				t.Error("副本还在，res.Kept 该有路径")
			}
			var failed, survive []string
			for _, l := range logs {
				if strings.Contains(l, "记不上 -keep 的标记") {
					failed = append(failed, l)
				}
				if strings.Contains(l, "不会清它") {
					survive = append(survive, l)
				}
			}
			if len(failed) > 0 && len(survive) > 0 {
				t.Fatalf("两句不能同时出现：%q 和 %q", failed, survive)
			}
			if c.breakMark && (len(failed) != 1 || len(survive) != 0) {
				t.Errorf("记不上时该有一句“记不上”、没有“不会清它”：%q", logs)
			}
			if !c.breakMark && (len(failed) != 0 || len(survive) != 1) {
				t.Errorf("记得上时该只有“不会清它”那一句：%q", logs)
			}
			// 收尾那一行总要说副本在哪
			if !strings.Contains(strings.Join(logs, "\n"), res.Kept) {
				t.Errorf("日志里该写副本的路径 %s：%q", res.Kept, logs)
			}
		})
	}
}

// 清理时 .keep 查不清（查询出错，不是“不存在”）：删了拿不回来，当它是 -keep 留下的，不清，记一行。
// 对照：同样形状、.keep 正常不存在的那份（崩溃遗留）照清；测试里换掉 lstat，因为 Windows 上造不出“不是不存在的错”
func TestCleanStaleKeepsCopyWhenKeepMarkCannotBeRead(t *testing.T) {
	f := newFixture(t)
	f.closeStores()
	parent := defaultWorkParent(f.Data)
	mk := func(name string) string {
		dir := filepath.Join(parent, name)
		f.write(filepath.Join(dir, "copy", "go", "chat", "qq.db"), "x")
		f.write(filepath.Join(dir, "copy", "go", "backend.lock"), "") // 锁文件在、没人持
		f.write(filepath.Join(dir, "out", "summary.json"), "{}")
		f.write(filepath.Join(dir, markerName), "{}")
		return dir
	}
	control, unreadable := mk("control"), mk("unreadable")

	orig := lstat
	t.Cleanup(func() { lstat = orig })
	lstat = func(name string) (fs.FileInfo, error) {
		if filepath.Base(name) == keepName && filepath.Base(filepath.Dir(name)) == "unreadable" {
			return nil, &fs.PathError{Op: "lstat", Path: name, Err: errors.New("拒绝访问")}
		}
		return os.Lstat(name)
	}

	var logs []string
	if n := cleanStale(f.Data, time.Now(), func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }); n != 1 {
		t.Fatalf("只该清 control 一份：%d（%q）", n, logs)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if exists(filepath.Join(control, "copy")) {
		t.Error("对照那份（.keep 正常不存在）该清掉")
	}
	if !exists(filepath.Join(unreadable, "copy")) {
		t.Error(".keep 查不清的那份不该清")
	}
	if !exists(filepath.Join(unreadable, "out", "summary.json")) {
		t.Error("报告不该动")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "读不了 unreadable 的 .keep") {
		t.Errorf("该记一行读不了 .keep：%q", logs)
	}
}
