package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sumika-native/internal/paths"
)

// 源路径守卫：只拷 Go 的副本。形状 × 入口：入口是 checkMemorySource（记忆库，里面是 W5 的 paths.CheckMemoryPath）
// 和 strictGuard（go/ 下每个文件）。notCopy 的格子要求错误能被 errors.Is(err, paths.ErrMemoryPath) 认出来：
// 拒绝的理由是 W5 的函数说的，不是工具自己另写的一份判断
func TestSourceGuard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		prep    func(f *fixture)
		char    string
		wantErr string // 空 = 通过
		notCopy bool   // 还要 errors.Is(err, paths.ErrMemoryPath)
	}{
		{name: "固定角色记忆库", char: testChar},
		{name: "Go 的记忆库不存在", char: testChar, wantErr: "不存在",
			prep: func(f *fixture) { os.Remove(f.memDB()) }},
		{name: "角色 id 带路径分隔符", char: "../characters/x", wantErr: "角色 id 不对"},
		{name: "角色 id 带冒号", char: "a:b", wantErr: "角色 id 不对"},
		{name: "角色 id 为空", char: "", wantErr: "角色 id 不对"},
		{name: "角色目录是指到别处的联接", char: testChar, wantErr: "过不了副本路径检查", notCopy: true,
			prep: func(f *fixture) {
				other := filepath.Join(t.TempDir(), "elsewhere")
				f.newMemDB(filepath.Join(other, "memory.db"), "elsewhere")
				dir := filepath.Join(f.Data, "go", "characters", testChar)
				f.closeStores()
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				makeLink(t, dir, other)
			}},
		{name: "Go 的记忆库其实是 Bun 原库的硬链接", char: testChar, wantErr: "过不了副本路径检查", notCopy: true,
			prep: func(f *fixture) {
				if runtime.GOOS != "windows" {
					t.Skip("CheckMemoryPath 只在 Windows 上查硬链接数")
				}
				if err := os.Remove(f.memDB()); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(f.Data, "characters", testChar, "memory.db"), f.memDB()); err != nil {
					t.Skipf("建不了硬链接：%v", err)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if c.prep != nil {
				c.prep(f)
			}
			rel, err := checkMemorySource(f.Data, c.char)
			if c.wantErr == "" {
				if err != nil || rel != "go/characters/suzune/memory.db" {
					t.Fatalf("应该通过：%q %v", rel, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("应该拒绝并说 %q：%q %v", c.wantErr, rel, err)
			}
			if c.notCopy && !errors.Is(err, paths.ErrMemoryPath) {
				t.Fatalf("应该是 W5 的 CheckMemoryPath 拒的（errors.Is ErrMemoryPath）：%v", err)
			}
		})
	}
}

// Windows 上大小写不同的写法（-char SUZUNE）不能绕过，也不能被误拒
func TestSourceGuardCaseVariant(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("只有 Windows 的文件系统不分大小写")
	}
	f := newFixture(t)
	rel, err := checkMemorySource(f.Data, strings.ToUpper(testChar))
	if err != nil {
		t.Fatalf("大小写不同但是同一个目录，应该通过：%v", err)
	}
	if !strings.EqualFold(rel, "go/characters/suzune/memory.db") {
		t.Fatalf("rel=%q", rel)
	}
}

// strictGuard 对 go/chat 这类整个目录被换成联接的情形
func TestStrictGuardJunctionedChatDir(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.closeStores()
	chat := filepath.Join(f.Data, "go", "chat")
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(chat, other); err != nil {
		t.Fatal(err)
	}
	makeLink(t, chat, other)
	if err := strictGuard(f.Data, "go/chat/qq.db"); err == nil {
		t.Fatal("go/chat 是联接，应该拒绝")
	}
	if err := strictGuard(f.Data, "go/trace.db"); err != nil {
		t.Fatalf("trace.db 没动过：%v", err)
	}
}

// -work 的位置守卫：形状（相对路径、dev 本身、go/ 里面、dev 是联接、被忽略 / 没被忽略 / 不在仓库 / git 出错）
func TestWorkDirGuard(t *testing.T) {
	t.Parallel()
	ignoredYes := func(string) (ignoreVerdict, error) { return verdictIgnored, nil }
	cases := []struct {
		name    string
		work    func(f *fixture) string
		check   ignoreCheck
		prep    func(f *fixture)
		wantErr string
	}{
		{name: "dev 下面、被忽略", work: func(f *fixture) string { return filepath.Join(f.Data, "dev", "memory-shadow", "r1") }, check: ignoredYes},
		{name: "dev 下面、不在任何仓库里", work: func(f *fixture) string { return filepath.Join(f.Data, "dev", "memory-shadow", "r1") },
			check: func(string) (ignoreVerdict, error) { return verdictNoRepo, nil }},
		{name: "dev 下面但是没被 git 忽略", work: func(f *fixture) string { return filepath.Join(f.Data, "dev", "x") },
			check: func(string) (ignoreVerdict, error) { return verdictTracked, nil }, wantErr: "没有被 git 忽略"},
		{name: "git 跑不了：核实不了就不放行", work: func(f *fixture) string { return filepath.Join(f.Data, "dev", "x") },
			check: func(string) (ignoreVerdict, error) { return 0, os.ErrNotExist }, wantErr: "file does not exist"},
		{name: "相对路径落进工作树", work: func(f *fixture) string { return "./x" }, check: ignoredYes, wantErr: "必须在"},
		{name: "dev 本身", work: func(f *fixture) string { return filepath.Join(f.Data, "dev") }, check: ignoredYes, wantErr: "必须在"},
		{name: "数据目录本身", work: func(f *fixture) string { return f.Data }, check: ignoredYes, wantErr: "必须在"},
		{name: "go 里面", work: func(f *fixture) string { return filepath.Join(f.Data, "go", "shadow") }, check: ignoredYes, wantErr: "必须在"},
		{name: "characters 里面", work: func(f *fixture) string { return filepath.Join(f.Data, "characters", testChar, "shadow") }, check: ignoredYes, wantErr: "必须在"},
		{name: "shared 里面", work: func(f *fixture) string { return filepath.Join(f.Data, "shared", "shadow") }, check: ignoredYes, wantErr: "必须在"},
		{name: "dev 下的子目录其实是指向 go 的联接", check: ignoredYes, wantErr: "必须在",
			work: func(f *fixture) string { return filepath.Join(f.Data, "dev", "sneaky", "r1") },
			prep: func(f *fixture) { makeLink(t, filepath.Join(f.Data, "dev", "sneaky"), filepath.Join(f.Data, "go")) }},
		{name: "dev 自己是指向 go 的联接", check: ignoredYes, wantErr: "不能在",
			work: func(f *fixture) string { return filepath.Join(f.Data, "dev", "r1") },
			prep: func(f *fixture) { makeLink(t, filepath.Join(f.Data, "dev"), filepath.Join(f.Data, "go")) }},
		{name: "dev 自己是指向数据目录外面的联接", check: ignoredYes, wantErr: "不在数据目录里",
			work: func(f *fixture) string { return filepath.Join(f.Data, "dev", "r1") },
			prep: func(f *fixture) { makeLink(t, filepath.Join(f.Data, "dev"), t.TempDir()) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.closeStores()
			if c.prep != nil {
				c.prep(f)
			}
			got, err := checkWorkDir(f.Data, c.work(f), c.check)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("应该通过：%v", err)
				}
				if _, err := os.Stat(got); err != nil {
					t.Fatalf("通过以后目录应该建好了：%v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("应该拒绝并说 %q：%q %v", c.wantErr, got, err)
			}
		})
	}
}

// 真的跑一次 git check-ignore：一个临时仓库里，被 .gitignore 管住的路径放行，没管住的拒绝
func TestWorkDirGuardRealGit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v：%v %s", args, err, out)
		}
	}
	run("init", "-q")
	data := filepath.Join(repo, "data")
	if err := os.MkdirAll(filepath.Join(data, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(data, "dev", "memory-shadow", "r1")
	// 没有 .gitignore：在仓库里、没被忽略 → 拒绝
	if _, err := checkWorkDir(data, work, nil); err == nil || !strings.Contains(err.Error(), "没有被 git 忽略") {
		t.Fatalf("没有 .gitignore 应该拒绝：%v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("data/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := checkWorkDir(data, work, nil); err != nil {
		t.Fatalf("data/ 被忽略应该通过：%v", err)
	}
}

// 真 git 在一个根本不是仓库的目录里：128 + 往上没有 .git → 不在仓库里，放行；
// .git 是一份坏文件（git 同样退出 128，和 dubious ownership 一个退出码）→ 核实不了，拒绝
func TestWorkDirGuardRealGitExit128(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	newData := func(t *testing.T) (data, work string) {
		t.Helper()
		data = filepath.Join(t.TempDir(), "data")
		if err := os.MkdirAll(filepath.Join(data, "dev"), 0o755); err != nil {
			t.Fatal(err)
		}
		return data, filepath.Join(data, "dev", "memory-shadow", "r1")
	}
	t.Run("不在任何仓库里：放行", func(t *testing.T) {
		t.Parallel()
		data, work := newData(t)
		if hasGitEntry(data) {
			t.Skip("临时目录自己就在一个仓库里")
		}
		if _, err := checkWorkDir(data, work, nil); err != nil {
			t.Fatalf("不在仓库里应该通过：%v", err)
		}
	})
	t.Run("有 .git 但是坏的：git 退出 128，拒绝", func(t *testing.T) {
		t.Parallel()
		data, work := newData(t)
		if err := os.WriteFile(filepath.Join(filepath.Dir(data), ".git"), []byte("这不是 gitfile"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := checkWorkDir(data, work, nil)
		if err == nil || !strings.Contains(err.Error(), "核实不了") {
			t.Fatalf("坏 .git 应该拒绝：%v", err)
		}
		if _, statErr := os.Stat(work); statErr == nil {
			t.Error("拒绝以后不该留下 -work 目录")
		}
	})
}

func TestHasGitEntry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", ".git"), []byte("gitdir: x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasGitEntry(deep) {
		t.Error(".git 是文件（worktree 那种）也算，上级里有就算")
	}
	if err := os.Mkdir(filepath.Join(root, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !hasGitEntry(root) && hasGitEntry(filepath.Join(root, "c")) {
		t.Error("别的分支里没有 .git，不该判有")
	}
}
