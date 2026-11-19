package lineend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGit 清掉环境里所有 GIT_*，再指一份空的全局配置、不读系统配置：
// 钩子里继承来的 GIT_DIR、GIT_INDEX_FILE 会让 git 去查别的仓库，本机的 autocrlf 也不该影响结果
func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") {
			old, had := os.LookupEnv(k)
			os.Unsetenv(k)
			if had {
				t.Cleanup(func() { os.Setenv(k, old) })
			}
		}
	}
	empty := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, empty, "")
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v：%v\n%s", args, err, out)
	}
}

// newRepo 建一个临时 git 仓库，写进 .gitattributes（attrs 为空就不写）
func newRepo(t *testing.T, attrs string) string {
	t.Helper()
	isolateGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if attrs != "" {
		writeFile(t, filepath.Join(dir, ".gitattributes"), attrs)
	}
	return dir
}

func TestGitEolAttributeBeatsSetting(t *testing.T) {
	dir := newRepo(t, "*.win.txt eol=crlf\n*.unix.txt eol=lf\n")
	useSetting(t, "lf")
	if got := Final(filepath.Join(dir, "a.win.txt"), nil, "x\ny"); got != "x\r\ny" {
		t.Errorf("eol=crlf：%q", got)
	}
	useSetting(t, "crlf")
	if got := Final(filepath.Join(dir, "sub", "a.unix.txt"), nil, "x\r\ny"); got != "x\ny" {
		t.Errorf("eol=lf（目录还没建）：%q", got)
	}
}

func TestGitMinusTextWritesAsGiven(t *testing.T) {
	dir := newRepo(t, "*.bin -text\n")
	useSetting(t, "crlf")
	if got := Final(filepath.Join(dir, "a.bin"), nil, "x\ny\r\nz"); got != "x\ny\r\nz" {
		t.Fatalf("-text 原样写：%q", got)
	}
	if got := Final(filepath.Join(dir, "a.txt"), nil, "x\ny"); got != "x\r\ny" {
		t.Fatalf("别的路径不受影响：%q", got)
	}
}

func TestGitAutocrlfAndCoreEol(t *testing.T) {
	dir := newRepo(t, "")
	abs := filepath.Join(dir, "a.txt")
	useSetting(t, "lf")
	if got := Final(abs, nil, "x\ny"); got != "x\ny" {
		t.Fatalf("没有任何 git 线索时落到配置：%q", got)
	}
	git(t, dir, "config", "core.autocrlf", "true")
	if got := Final(abs, nil, "x\ny"); got != "x\r\ny" {
		t.Errorf("autocrlf=true：%q", got)
	}
	git(t, dir, "config", "core.autocrlf", "false")
	git(t, dir, "config", "core.eol", "crlf")
	if got := Final(abs, nil, "x\ny"); got != "x\r\ny" {
		t.Errorf("core.eol=crlf：%q", got)
	}
	useSetting(t, "crlf")
	git(t, dir, "config", "core.eol", "lf")
	if got := Final(abs, nil, "x\r\ny"); got != "x\ny" {
		t.Errorf("core.eol=lf：%q", got)
	}
}

// eol 属性压过 autocrlf；已有文件和 .editorconfig 排在 git 前面
func TestGitIsAfterExistingAndEditorconfig(t *testing.T) {
	dir := newRepo(t, "*.txt eol=crlf\n")
	useSetting(t, "lf")
	abs := filepath.Join(dir, "a.txt")
	git(t, dir, "config", "core.autocrlf", "input")
	if got := Final(abs, []byte("旧\n"), "x\ny"); got != "x\ny" {
		t.Errorf("已有 LF 文件排在 git 前：%q", got)
	}
	writeFile(t, filepath.Join(dir, ".editorconfig"), "root = true\n[*]\nend_of_line = lf\n")
	if got := Final(abs, nil, "x\r\ny"); got != "x\ny" {
		t.Errorf(".editorconfig 排在 git 前：%q", got)
	}
}

// git 不在、不在仓库里：当没有这一档，落到配置
func TestGitMissingOrOutsideRepoFallsThrough(t *testing.T) {
	dir := newRepo(t, "*.txt eol=crlf\n")
	useSetting(t, "lf")
	t.Setenv("PATH", "")
	if got := Final(filepath.Join(dir, "a.txt"), nil, "x\ny"); got != "x\ny" {
		t.Errorf("找不到 git：%q", got)
	}
	outside := tmp(t)
	if got := Final(filepath.Join(outside, "a.txt"), nil, "x\r\ny"); got != "x\ny" {
		t.Errorf("不在仓库里：%q", got)
	}
}

// worktree 里 .git 是个文件：照样认
func TestGitWorktree(t *testing.T) {
	main := newRepo(t, "*.txt eol=crlf\n")
	git(t, main, "add", ".gitattributes")
	git(t, main, "commit", "-q", "-m", "x")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, main, "worktree", "add", "-q", "-b", "side", wt)
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("worktree 的 .git 该是文件：%v", err)
	}
	useSetting(t, "lf")
	if got := Final(filepath.Join(wt, "a.txt"), nil, "x\ny"); got != "x\r\ny" {
		t.Fatalf("worktree 里 eol=crlf：%q", got)
	}
}

// 钩子里继承来的 GIT_DIR 不能让查询跑到别的仓库去
func TestGitIgnoresInheritedGitDir(t *testing.T) {
	dir := newRepo(t, "*.txt eol=crlf\n")
	other := newRepo(t, "*.txt eol=lf\n")
	useSetting(t, "lf") // 配置给 LF：要是查询跑偏到别的仓库或失败，结果会是 LF
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	if got := Final(filepath.Join(dir, "a.txt"), nil, "x\ny"); got != "x\r\ny" {
		t.Fatalf("要照目标自己所在的仓库：%q", got)
	}
}
