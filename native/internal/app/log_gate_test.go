package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/applog"
	"sumika-native/internal/filelog"
	"sumika-native/internal/paths"
)

type formatCounter struct{ n *int }

func (c formatCounter) String() string { *c.n++; return "x" }

func logRootFor(t *testing.T) (*applog.Root, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := paths.Init(dir); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 10, 2, 31, 0, time.Local)
	now := func() time.Time { return at }
	root := applog.New(filelog.New(dir, "desk", nil, now), now)
	t.Cleanup(func() { root.Close() })
	return root, filepath.Join(dir, "logs", filelog.FileName("desk", at))
}

// 规格 A3：装配出来的 debugFor、warnFor、errorFor 也是“先判级别、再格式化和打码”。
// 级别没开时参数的 String 不被调用（以前每次都先 Sprintf 再跑一遍打码正则，才交给 applog 判级别）。
func TestAssembledLevelFuncsDoNotFormatWhenDisabled(t *testing.T) {
	t.Parallel()
	root, file := logRootFor(t)
	a := &App{opt: Options{Logs: root}, tokens: []string{"TESTTOKEN-SECRET-XYZ"}}

	n := 0
	a.debugFor(applog.Turn)("每步 %v", formatCounter{&n})
	if n != 0 {
		t.Fatalf("默认 INFO 下 debugFor 不该格式化参数，String 被调了 %d 次", n)
	}
	root.SetLevel(slog.LevelWarn)
	a.logfFor(applog.Turn)("信息 %v", formatCounter{&n})
	if n != 0 {
		t.Fatalf("全局 WARN 下 INFO 不该格式化，String 被调了 %d 次", n)
	}
	a.warnFor(applog.Turn)("警告 %v 令牌 %s", formatCounter{&n}, "TESTTOKEN-SECRET-XYZ")
	a.errorFor(applog.Turn)("错误 %v", formatCounter{&n})
	if n != 2 {
		t.Errorf("WARN、ERROR 该各格式化一次，String 共调 %d 次", n)
	}
	root.SetModules(map[string]slog.Level{"turn": slog.LevelDebug})
	a.debugFor(applog.Turn)("每步 %v", formatCounter{&n})
	if n != 3 {
		t.Errorf("模块覆盖成 debug 后该写，String 共调 %d 次", n)
	}

	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Split(strings.TrimRight(string(b), "\r\n"), "\n"), "\n")
	want := "2026-11-13 10:02:31 WARN turn 警告 x 令牌 ***\n2026-11-13 10:02:31 ERROR turn 错误 x\n2026-11-13 10:02:31 DEBUG turn 每步 x"
	if strings.ReplaceAll(got, "\r", "") != want {
		t.Errorf("实际：\n%s", got)
	}
}

// 没有 Logs（测试里用 opt.Logf）时没有级别可判：所有级别的出口都照写，行为和以前一样
func TestLevelFuncsWithoutRootFallBackToLogf(t *testing.T) {
	t.Parallel()
	var lines []string
	a := &App{opt: Options{Logf: func(f string, args ...any) { lines = append(lines, f) }}}
	a.debugFor(applog.Turn)("d")
	a.warnFor(applog.Turn)("w")
	a.errorFor(applog.Turn)("e")
	if len(lines) != 3 {
		t.Errorf("三个级别都该落到 Logf，实际 %q", lines)
	}
}
