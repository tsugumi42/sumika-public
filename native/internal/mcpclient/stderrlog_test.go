package mcpclient

// stdio 服务的 stderr 单独写日志文件（stderrlog.go）：抹凭据、按大小轮转、文件名安全、写不进去不影响。
// 日志目录只用 t.TempDir()。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStderrLogRedactsAndKeepsLines(t *testing.T) {
	dir := t.TempDir()
	cfg := ServerConfig{ID: "demo", Env: map[string]string{"API_KEY": "SECRETVALUE"}, Headers: map[string]string{"X-Auth": "HEADERVALUE"}}
	l := newStderrLog(dir, cfg)
	fmt.Fprint(l, "loading\r\nusing SECRETVALUE and HEADERVALUE here\nAuthorization: Bearer abc123\ntoken=xyz rejected\n半行没换")
	l.Close()
	got := readLog(t, dir, "demo.log")
	for _, bad := range []string{"SECRETVALUE", "HEADERVALUE", "abc123", "xyz"} {
		if strings.Contains(got, bad) {
			t.Fatalf("凭据 %q 写进了日志文件：\n%s", bad, got)
		}
	}
	for _, want := range []string{"=== 启动 ===", "loading", "using *** and *** here", "Authorization: Bearer ***", "token=*** rejected", "半行没换"} {
		if !strings.Contains(got, want) {
			t.Fatalf("日志里该有 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "\r") {
		t.Fatal("回车该去掉")
	}
}

func TestStderrLogRotatesAndKeepsFew(t *testing.T) {
	dir := t.TempDir()
	l := newStderrLog(dir, ServerConfig{ID: "demo"})
	l.max, l.keep = 300, 3
	for i := 0; i < 40; i++ {
		fmt.Fprintf(l, "第 %02d 行：%s\n", i, strings.Repeat("x", 30))
	}
	l.Close()
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		info, _ := e.Info()
		if info.Size() > 400 {
			t.Errorf("%s 有 %d 字节，超过上限太多", e.Name(), info.Size())
		}
	}
	if strings.Join(names, ",") != "demo.log,demo.log.1,demo.log.2" {
		t.Fatalf("该只留当前的加两份旧的：%v", names)
	}
	if !strings.Contains(readLog(t, dir, "demo.log"), "第 39 行") {
		t.Fatal("当前文件该有最新的一行")
	}
	if strings.Contains(readLog(t, dir, "demo.log.2"), "第 00 行") {
		t.Fatal("最老的该被挤掉")
	}
}

// 服务重启接着写同一个文件，不另起
func TestStderrLogAppendsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 2; i++ {
		l := newStderrLog(dir, ServerConfig{ID: "demo"})
		fmt.Fprintf(l, "第 %d 次启动后的话\n", i)
		l.Close()
	}
	got := readLog(t, dir, "demo.log")
	if strings.Count(got, "=== 启动 ===") != 2 || !strings.Contains(got, "第 1 次") || !strings.Contains(got, "第 2 次") {
		t.Fatalf("两次启动该都在同一个文件里：\n%s", got)
	}
}

// 服务 id 来自配置，可能带路径符号：文件名只留安全字符，不能跑出日志目录
func TestStderrLogFileNameIsSafe(t *testing.T) {
	for id, want := range map[string]string{
		"demo":                   "demo.log",
		"../../evil":             "_.._evil.log",
		`a\b/c:d`:                "a_b_c_d.log",
		"..":                     "server.log",
		"":                       "server.log",
		"文件 系统":                  "_____.log",
		"CON":                    "CON.log",
		strings.Repeat("a", 200): strings.Repeat("a", 80) + ".log",
	} {
		got := logFileName(id)
		if id == "../../evil" || id == "文件 系统" {
			if strings.ContainsAny(got, `/\`) || strings.HasPrefix(got, ".") {
				t.Errorf("%q → %q 不安全", id, got)
			}
			continue
		}
		if got != want {
			t.Errorf("%q → %q，想要 %q", id, got, want)
		}
	}
	dir := t.TempDir()
	l := newStderrLog(filepath.Join(dir, "logs"), ServerConfig{ID: "../../evil"})
	fmt.Fprintln(l, "x")
	l.Close()
	if _, err := os.Stat(filepath.Join(dir, "evil.log")); err == nil {
		t.Fatal("跑出了日志目录")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "logs"))
	if len(entries) != 1 {
		t.Fatalf("该只在日志目录里有一个文件：%v", entries)
	}
}

// 写不进去（目录位置被文件占了）、没给目录、一行特别长：都不崩，Write 都报成功
func TestStderrLogBrokenOrOffOrHuge(t *testing.T) {
	if l := newStderrLog("", ServerConfig{ID: "x"}); l != nil {
		t.Fatal("没给目录就不写")
	}
	var off *stderrLog
	if n, err := off.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("nil 的 Write 该是空操作：%d %v", n, err)
	}
	off.Close()

	dir := t.TempDir()
	blocker := filepath.Join(dir, "占位")
	os.WriteFile(blocker, []byte("文件"), 0o644)
	l := newStderrLog(filepath.Join(blocker, "mcp"), ServerConfig{ID: "x"})
	for i := 0; i < 3; i++ {
		if n, err := l.Write([]byte("还在写\n")); n != len("还在写\n") || err != nil {
			t.Fatalf("写不进去也不能让 stderr 另一路断：%d %v", n, err)
		}
	}
	l.Close()

	d2 := t.TempDir()
	l2 := newStderrLog(d2, ServerConfig{ID: "huge"})
	fmt.Fprint(l2, strings.Repeat("长", 5000)+"\n")
	fmt.Fprint(l2, strings.Repeat("z", stderrPending+10)) // 一直不换行
	l2.Close()
	for _, line := range strings.Split(readLog(t, d2, "huge.log"), "\n") {
		if n := len([]rune(line)); n > stderrLineMax+40 {
			t.Fatalf("一行有 %d 个字，该截断", n)
		}
	}
}
