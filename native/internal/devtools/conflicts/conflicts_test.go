package conflicts

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/devtools/gitrun"
)

// 测试里起 git：环境里的 GIT_* 一律清掉（LESSONS：钩子里继承的 GIT_DIR 会让 git 改到真仓库）；Run 自己继承 os.Environ()，所以要在进程环境里清。
func clearGitEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir())) // 不让 git 往临时目录上面找到别的仓库
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

func run(t *testing.T, dir string) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(nil, dir, &out, &errb)
	return out.String(), code
}

func TestStrongMarkersAreReportedWithLineNumbers(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "a.md", "头\n<<<<<<< HEAD\n甲\n=======\n乙\n>>>>>>> feat/x\n尾\n")
	git(t, dir, "add", "a.md")
	out, code := run(t, dir)
	want := "这些地方还有没解决的合并冲突：\na.md:2: <<<<<<< HEAD\na.md:4: =======\na.md:6: >>>>>>> feat/x\n"
	if code != 1 || out != want {
		t.Fatalf("code=%d\n%q\n要\n%q", code, out, want)
	}
}

func TestWeakMarkersAloneAreFineAndStrongNeedsSpaceOrEnd(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "title.md", "标题\n=======\n正文\n|||||||\n")
	put(t, dir, "eight.txt", "<<<<<<<<\n>>>>>>>x\n  <<<<<<< 缩进的不算\n")
	git(t, dir, "add", ".")
	if out, code := run(t, dir); code != 0 || out != "" {
		t.Fatalf("没有真标记应通过：code=%d %q", code, out)
	}
}

func TestOnlyAddedLinesCountAndCRLFMarkersCount(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "old.txt", "<<<<<<< 早就在里面\n")
	git(t, dir, "add", "old.txt")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "init")
	put(t, dir, "old.txt", "<<<<<<< 早就在里面\n新加一行\n")
	put(t, dir, "crlf.txt", "x\r\n>>>>>>>\r\n")
	git(t, dir, "add", ".")
	out, code := run(t, dir)
	// 旧行不报；CRLF 的标记报出来，行尾的 \r 被去掉
	if code != 1 || out != "这些地方还有没解决的合并冲突：\ncrlf.txt:2: >>>>>>>\n" {
		t.Fatalf("code=%d %q", code, out)
	}
}

func TestUnstagedAndDeletedAreIgnored(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "gone.txt", "<<<<<<< a\n")
	git(t, dir, "add", "gone.txt")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "init")
	git(t, dir, "rm", "-q", "gone.txt")
	put(t, dir, "work.txt", "<<<<<<< 只在工作区\n") // 没暂存
	if out, code := run(t, dir); code != 0 || out != "" {
		t.Fatalf("code=%d %q", code, out)
	}
}

func TestContentLinesStartingWithPlusPlusAreNotFileHeaders(t *testing.T) {
	diff := "diff --git a/x b/x\nnew file mode 100644\n--- /dev/null\n+++ b/x\n@@ -0,0 +1,2 @@\n+++ 内容里以 ++ 开头的行\n+<<<<<<< 真标记\n"
	got := Scan(diff)
	if len(got) != 1 || got[0] != "x:2: <<<<<<< 真标记" {
		t.Fatalf("%q", got)
	}
}

func TestNotARepoFailsClosed(t *testing.T) {
	clearGitEnv(t)
	out, code := run(t, t.TempDir())
	if code != 1 || !strings.HasPrefix(out, "读不到暂存区的改动：") {
		t.Fatalf("code=%d %q", code, out)
	}
}

// 钩子里 GIT_INDEX_FILE 指向这次要提交的索引（git commit <文件>、-a 时是临时索引）：必须读它，不读主索引（2026-11-19 第 4 步复核）。
// 主索引里是好的，临时索引里是坏的，进程环境指向临时索引时应拦下。
func TestHonorsInheritedGitIndexFile(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "a.md", "x\n")
	git(t, dir, "add", "a.md")
	tmpIdx := filepath.Join(t.TempDir(), "idx")
	env := append(os.Environ(), "GIT_INDEX_FILE="+tmpIdx)
	put(t, dir, "a.md", "x\n"+strings.Repeat("<", 7)+" HEAD\ny\n"+strings.Repeat("=", 7)+"\nz\n"+strings.Repeat(">", 7)+" b\n")
	if _, code, err := gitrun.Run(dir, env, nil, "add", "a.md"); err != nil || code != 0 {
		t.Fatalf("往临时索引里暂存失败：%d %v", code, err)
	}
	put(t, dir, "a.md", "x\n") // 工作区也是好的：只有临时索引里是坏的
	if _, code := run(t, dir); code != 0 {
		t.Fatalf("没设 GIT_INDEX_FILE 时读主索引，应通过：code=%d", code)
	}
	t.Setenv("GIT_INDEX_FILE", tmpIdx)
	if out, code := run(t, dir); code != 1 {
		t.Fatalf("GIT_INDEX_FILE 指向临时索引时应拦下：code=%d %q", code, out)
	}
}
