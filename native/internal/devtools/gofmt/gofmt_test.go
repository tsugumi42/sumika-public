package gofmt

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/devtools/gitrun"
)

const (
	good = "package x\n\nfunc F() int { return 1 }\n"
	bad  = "package x\nfunc F()   int {return 1}\n"
)

func clearGitEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, code, err := gitrun.Run(dir, nil, nil, args...); err != nil || code != 0 {
		t.Fatalf("git %v 失败：code=%d err=%v", args, code, err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	clearGitEnv(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "core.autocrlf", "false")
	return dir
}

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, files ...string) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(files, dir, &out, &errb)
	return out.String(), code
}

func TestFormattedPassesAndUnformattedFails(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "native/a.go", good)
	put(t, dir, "native/b.go", bad)
	put(t, dir, "native/c.go", bad)
	git(t, dir, "add", ".")
	if out, code := run(t, dir, "native/a.go"); code != 0 || out != "" {
		t.Fatalf("格式对的应通过：code=%d %q", code, out)
	}
	out, code := run(t, dir, "native/a.go", "native/b.go", "./native/c.go")
	if code != 1 || out != "这些文件没 gofmt：\nnative/b.go\n./native/c.go\n" {
		t.Fatalf("code=%d %q", code, out)
	}
}

func TestChecksStagedContentNotWorkingTree(t *testing.T) {
	dir := newRepo(t)
	// 暂存的是 LF 且格式对；工作区被 autocrlf 之类改成了 CRLF——旧的 gofmt -l 会误报
	put(t, dir, "x.go", good)
	git(t, dir, "add", "x.go")
	put(t, dir, "x.go", strings.ReplaceAll(good, "\n", "\r\n"))
	if out, code := run(t, dir, "x.go"); code != 0 || out != "" {
		t.Fatalf("暂存区格式对、工作区是 CRLF：应通过 code=%d %q", code, out)
	}
	// 反过来：暂存的不对、工作区改好了没再暂存——要拦
	put(t, dir, "y.go", bad)
	git(t, dir, "add", "y.go")
	put(t, dir, "y.go", good)
	if out, code := run(t, dir, "y.go"); code != 1 || out != "这些文件没 gofmt：\ny.go\n" {
		t.Fatalf("暂存区没 gofmt：应拦 code=%d %q", code, out)
	}
}

func TestNotInIndexAndSyntaxErrorsFailImmediately(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "bad.go", "package x\nfunc (\n")
	put(t, dir, "fmt.go", bad)
	put(t, dir, "untracked.go", good)
	git(t, dir, "add", "bad.go", "fmt.go")
	out, code := run(t, dir, "fmt.go", "untracked.go")
	if code != 1 || !strings.HasPrefix(out, "读不到暂存区里的 untracked.go：") {
		t.Fatalf("不在暂存区：code=%d %q", code, out)
	}
	if strings.Contains(out, "没 gofmt") {
		t.Fatalf("中途失败时不再列没 gofmt 的文件：%q", out)
	}
	out, code = run(t, dir, "bad.go")
	if code != 1 || !strings.HasPrefix(out, "gofmt 自己报错了（bad.go，退出码 2）：\n") {
		t.Fatalf("语法错：code=%d %q", code, out)
	}
}

func TestNoArgsIsOK(t *testing.T) {
	if out, code := run(t, t.TempDir()); code != 0 || out != "" {
		t.Fatalf("code=%d %q", code, out)
	}
}

// 钩子里 GIT_INDEX_FILE 指向这次要提交的索引（git commit <文件>、-a 时是临时索引）：必须读它，不读主索引（2026-11-19 第 4 步复核）。
// 主索引里是好的，临时索引里是坏的，进程环境指向临时索引时应拦下。
func TestHonorsInheritedGitIndexFile(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "a.go", good)
	git(t, dir, "add", "a.go")
	tmpIdx := filepath.Join(t.TempDir(), "idx")
	env := append(os.Environ(), "GIT_INDEX_FILE="+tmpIdx)
	put(t, dir, "a.go", bad)
	if _, code, err := gitrun.Run(dir, env, nil, "add", "a.go"); err != nil || code != 0 {
		t.Fatalf("往临时索引里暂存失败：%d %v", code, err)
	}
	put(t, dir, "a.go", good) // 工作区也是好的：只有临时索引里是坏的
	if _, code := run(t, dir, "a.go"); code != 0 {
		t.Fatalf("没设 GIT_INDEX_FILE 时读主索引，应通过：code=%d", code)
	}
	t.Setenv("GIT_INDEX_FILE", tmpIdx)
	if out, code := run(t, dir, "a.go"); code != 1 {
		t.Fatalf("GIT_INDEX_FILE 指向临时索引时应拦下：code=%d %q", code, out)
	}
}
