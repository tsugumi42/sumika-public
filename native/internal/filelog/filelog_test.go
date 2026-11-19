package filelog

// 日志的测试：文件名和每行开头用本地日期（不是 UTC），跨过零点换到新一天的文件，out 里也有一份；
// 前缀区分诊断入口和桌面程序；数据目录里没有身份证就不建 logs/；out 是 nil 时只写文件；Close 以后不再开文件。
// 日期都是编的，目录用 t.TempDir()。

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/paths"
)

// initDir 给临时目录写身份证，当作一个已经初始化的数据目录
func initDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := paths.Init(dir); err != nil {
		t.Fatalf("初始化临时数据目录：%v", err)
	}
	return dir
}

func linesOf(t *testing.T, dir, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "logs", name))
	if err != nil {
		t.Fatalf("读 %s：%v", name, err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestLoggerLocalDateAndRollover(t *testing.T) {
	dir := initDir(t)
	// 东九区：本地 10-29 00:30 时 UTC 还是 10-28，用 UTC 写日期就会落到前一天的文件里
	zone := time.FixedZone("试验时区", 9*3600)
	now := time.Date(2026, 10, 29, 0, 30, 0, 0, zone)
	var out bytes.Buffer
	l := New(dir, "go", &out, func() time.Time { return now })
	defer l.Close()
	l.Printf("第一行 %d", 1)
	now = time.Date(2026, 10, 30, 0, 0, 1, 0, zone)
	l.Printf("第二行 %d", 2)

	first := linesOf(t, dir, "go-2026-10-29.log")
	if len(first) != 1 {
		t.Fatalf("go-2026-10-29.log 该只有一行（跨零点以后不再写它），实际 %d 行：%q", len(first), first)
	}
	if !strings.HasPrefix(first[0], "2026-10-29 00:30:00 ") || !strings.HasSuffix(first[0], "第一行 1") {
		t.Errorf("第一行是 %q，该是本地时间 2026-10-29 00:30:00 加正文", first[0])
	}
	second := linesOf(t, dir, "go-2026-10-30.log")
	if len(second) != 1 || !strings.HasPrefix(second[0], "2026-10-30 00:00:01 ") || !strings.HasSuffix(second[0], "第二行 2") {
		t.Errorf("跨零点以后该写进 go-2026-10-30.log，实际 %q", second)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "go-2026-10-28.log")); err == nil {
		t.Error("出现了 go-2026-10-28.log：日期按 UTC 算了（本地 10-29 00:30 是 UTC 10-28 15:30）")
	}
	if got := out.String(); !strings.Contains(got, "第一行 1") || !strings.Contains(got, "第二行 2") {
		t.Errorf("out 里该有同样两行，实际 %q", got)
	}
}

// 桌面程序没有控制台：out 传 nil，文件名用 desk- 前缀，和诊断入口的 go- 并存不混
func TestLoggerNilOutAndPrefix(t *testing.T) {
	dir := initDir(t)
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local)
	l := New(dir, "desk", nil, func() time.Time { return now })
	l.Printf("桌面第一行")
	if err := l.Close(); err != nil {
		t.Fatalf("Close：%v", err)
	}
	got := linesOf(t, dir, FileName("desk", now))
	if len(got) != 1 || got[0] != "2026-11-01 09:00:00 桌面第一行" {
		t.Errorf("desk-2026-11-01.log 里是 %q", got)
	}
	if FileName("desk", now) != "desk-2026-11-01.log" || FileName("go", now) != "go-2026-11-01.log" {
		t.Errorf("文件名前缀不对：%s %s", FileName("desk", now), FileName("go", now))
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "go-2026-11-01.log")); err == nil {
		t.Error("桌面程序写出了 go- 前缀的文件")
	}
}

// 数据目录还没有身份证（不存在，或者是个空目录）：不建 logs/ 和文件，只打 out；
// 之后目录有了身份证（receive 的 Init）再写的行就进文件
func TestLoggerWaitsForIdentity(t *testing.T) {
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local)
	for _, name := range []string{"不存在", "空目录"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "数据")
			if name == "空目录" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			l := New(dir, "desk", &out, func() time.Time { return now })
			defer l.Close()
			l.Printf("还没有身份证")
			if _, err := os.Stat(filepath.Join(dir, "logs")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("没有身份证的数据目录里被建出了 logs/：%v", err)
			}
			if name == "不存在" {
				if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("不存在的数据目录被建出来了：%v", err)
				}
			}
			if !strings.Contains(out.String(), "还没有身份证") {
				t.Errorf("没有身份证时 out 里也该有这一行：%q", out.String())
			}
			if _, err := paths.Init(dir); err != nil {
				t.Fatal(err)
			}
			l.Printf("有身份证了")
			got := linesOf(t, dir, FileName("desk", now))
			if len(got) != 2 || !strings.HasSuffix(got[0], "还没有身份证") || !strings.HasSuffix(got[1], "有身份证了") {
				t.Errorf("有身份证以后，身份证出现前缓冲的行和之后的行该按顺序都写进文件（规格-日志分级 4.3），实际 %q", got)
			}
		})
	}
}

// Close 以后不再开文件（退出收尾里还会有迟到的行），这些行只打 out
func TestLoggerAfterClose(t *testing.T) {
	dir := initDir(t)
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local)
	var out bytes.Buffer
	l := New(dir, "desk", &out, func() time.Time { return now })
	l.Printf("关之前")
	l.Close()
	l.Printf("关之后")
	got := linesOf(t, dir, FileName("desk", now))
	if len(got) != 1 || !strings.HasSuffix(got[0], "关之前") {
		t.Errorf("Close 以后又写进了文件：%q", got)
	}
	if !strings.Contains(out.String(), "关之后") {
		t.Errorf("Close 以后 out 里该还有这一行：%q", out.String())
	}
}
