package selfbuild

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mkModule 造一个最小模块：go.mod、go.sum 和两个源码目录各一个 .go。
func mkModule(t *testing.T, crlf bool) string {
	t.Helper()
	root := t.TempDir()
	nl := "\n"
	if crlf {
		nl = "\r\n"
	}
	put := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(body, "\n", nl)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("go.mod", "module m\n\ngo 1.27\n")
	put("go.sum", "")
	put("cmd/sumika-dev/main.go", "package main\n\nfunc main() {}\n")
	put("internal/devtools/a/a.go", "package a\n")
	put("internal/jsjson/j.go", "package jsjson\n")
	put("internal/netsafe/n.go", "package netsafe\n")
	put("internal/other/x.go", "package other\n") // 不在范围内，改它摘要不变
	return root
}

func TestDigestStableAcrossLineEndings(t *testing.T) {
	a, err := Digest(mkModule(t, false))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Digest(mkModule(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("CRLF 和 LF 的摘要不同：%s / %s", a, b)
	}
}

func TestDigestChangesWithSourceButNotOutside(t *testing.T) {
	root := mkModule(t, false)
	base, _ := Digest(root)
	if err := os.WriteFile(filepath.Join(root, "internal/other/x.go"), []byte("package other // 改\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, _ := Digest(root); d != base {
		t.Fatal("范围外的文件变了，摘要不该变")
	}
	for _, rel := range []string{"internal/devtools/a/a.go", "cmd/sumika-dev/main.go", "internal/jsjson/j.go", "internal/netsafe/n.go", "go.mod"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		old, _ := os.ReadFile(p)
		os.WriteFile(p, append(old, []byte("// 改\n")...), 0o644)
		if d, _ := Digest(root); d == base {
			t.Fatalf("改了 %s 摘要没变", rel)
		}
		os.WriteFile(p, old, 0o644)
	}
	// 新增一个文件也算
	os.WriteFile(filepath.Join(root, "internal/devtools/b.go"), []byte("package devtools\n"), 0o644)
	if d, _ := Digest(root); d == base {
		t.Fatal("新增源码文件摘要没变")
	}
}

type fakeEnv struct {
	Env
	builds, execs int
	stderr        bytes.Buffer
}

func newFake(t *testing.T, built string) *fakeEnv {
	t.Helper()
	f := &fakeEnv{}
	exe := filepath.Join(t.TempDir(), "sumika-dev.exe")
	if err := os.WriteFile(exe, []byte("旧程序"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.Env = Env{
		Exe: exe, Args: []string{"version", "-x"}, Stderr: &f.stderr, Built: built,
		Build: func(root, out, digest string) error {
			f.builds++
			return os.WriteFile(out, []byte("新程序 "+digest), 0o755)
		},
		Exec: func(exe string, args []string, digest string) (int, error) {
			f.execs++
			if strings.Join(args, " ") != "version -x" {
				t.Errorf("没带原参数：%v", args)
			}
			return 7, nil
		},
	}
	return f
}

func TestEnsureUnchangedDoesNotRebuild(t *testing.T) {
	root := mkModule(t, false)
	d, _ := Digest(root)
	f := newFake(t, d)
	rebuilt, _, err := Ensure(root, f.Env)
	if err != nil || rebuilt || f.builds != 0 || f.execs != 0 {
		t.Fatalf("没变不该重编：rebuilt=%v err=%v builds=%d execs=%d", rebuilt, err, f.builds, f.execs)
	}
}

func TestEnsureRebuildsReplacesAndPassesExitCode(t *testing.T) {
	root := mkModule(t, false)
	f := newFake(t, "旧摘要")
	rebuilt, code, err := Ensure(root, f.Env)
	if err != nil || !rebuilt || code != 7 || f.builds != 1 || f.execs != 1 {
		t.Fatalf("rebuilt=%v code=%d err=%v builds=%d execs=%d", rebuilt, code, err, f.builds, f.execs)
	}
	got, _ := os.ReadFile(f.Exe)
	if !strings.HasPrefix(string(got), "新程序") {
		t.Fatalf("程序没被换掉：%q", got)
	}
	olds, _ := filepath.Glob(f.Exe + ".old.*")
	if len(olds) != 1 {
		t.Fatalf("旧程序应留成一个 .old.<pid>：%v", olds)
	}
	if old, _ := os.ReadFile(olds[0]); string(old) != "旧程序" {
		t.Fatalf("旧程序内容不对：%q", old)
	}
	if !strings.Contains(f.stderr.String(), "重新编译") {
		t.Fatalf("没有重新编译的提示：%q", f.stderr.String())
	}
	CleanOld(f.Exe)
	if left, _ := filepath.Glob(f.Exe + ".old*"); len(left) != 0 {
		t.Fatalf("CleanOld 没清掉：%v", left)
	}
	if left, _ := filepath.Glob(f.Exe + ".new*"); len(left) != 0 {
		t.Fatalf("临时文件 .new 没清：%v", left)
	}
	// 锁文件常驻不删，但锁要放掉了：马上能再取
	rel, err := acquireLock(f.Exe+".lock", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("重编完锁没放掉：%v", err)
	}
	rel()
}

func TestEnsureBuildFailureBlocksAndKeepsOldExe(t *testing.T) {
	root := mkModule(t, false)
	f := newFake(t, "旧摘要")
	f.Build = func(root, out, digest string) error { return errors.New("语法错误") }
	rebuilt, code, err := Ensure(root, f.Env)
	if err == nil || !rebuilt || code == 0 || f.execs != 0 {
		t.Fatalf("编不过必须拦下：rebuilt=%v code=%d err=%v execs=%d", rebuilt, code, err, f.execs)
	}
	if !strings.Contains(err.Error(), "语法错误") {
		t.Fatalf("原因没带出来：%v", err)
	}
	if got, _ := os.ReadFile(f.Exe); string(got) != "旧程序" {
		t.Fatalf("编不过时旧程序不该被动：%q", got)
	}
}

func TestEnsureReexecGuardStopsLoop(t *testing.T) {
	root := mkModule(t, false)
	f := newFake(t, "旧摘要")
	t.Setenv(envReexec, mustDigest(t, root))
	rebuilt, _, err := Ensure(root, f.Env)
	if err != nil || rebuilt || f.builds != 0 {
		t.Fatalf("重编后又对不上不该再重编：rebuilt=%v err=%v builds=%d", rebuilt, err, f.builds)
	}
}

func TestFindModRoot(t *testing.T) {
	repo := t.TempDir()
	native := filepath.Join(repo, "native")
	os.MkdirAll(filepath.Join(native, "bin"), 0o755)
	os.WriteFile(filepath.Join(native, "go.mod"), []byte("module m\n"), 0o644)
	exe := filepath.Join(native, "bin", "sumika-dev.exe")
	t.Setenv(EnvRoot, "")
	if r, err := FindModRoot(exe); err != nil || r != native {
		t.Fatalf("往上找：%q %v", r, err)
	}
	t.Setenv(EnvRoot, repo) // 仓库根也接受
	if r, err := FindModRoot(filepath.Join(t.TempDir(), "x.exe")); err != nil || r != native {
		t.Fatalf("环境变量指仓库根：%q %v", r, err)
	}
	t.Setenv(EnvRoot, t.TempDir())
	if _, err := FindModRoot(exe); err == nil {
		t.Fatal("环境变量指向没有 go.mod 的目录应报错")
	}
}

func TestGoBinary(t *testing.T) {
	t.Setenv(EnvGo, "go") // 相对路径不接受
	if _, err := goBinary(); err == nil {
		t.Fatal("相对路径应报错")
	}
	t.Setenv(EnvGo, filepath.Join(t.TempDir(), "没有这个go"))
	if _, err := goBinary(); err == nil || !strings.Contains(err.Error(), "找不到 go") {
		t.Fatalf("不存在应报中文原因：%v", err)
	}
	fake := filepath.Join(t.TempDir(), "go")
	os.WriteFile(fake, []byte("x"), 0o755)
	t.Setenv(EnvGo, fake)
	if p, err := goBinary(); err != nil || p != fake {
		t.Fatalf("指定路径：%q %v", p, err)
	}
	t.Setenv(EnvGo, "")
	if p, err := goBinary(); err != nil || !filepath.IsAbs(p) {
		t.Fatalf("默认应取到本机 GOROOT 里的 go：%q %v", p, err)
	}
}

// 并发：lefthook 的 pre-commit 是并行的，好几个进程同时发现源码变了。只许编一次，全部成功，都执行到新程序。
func TestEnsureConcurrentRebuildsOnce(t *testing.T) {
	root := mkModule(t, false)
	exe := filepath.Join(t.TempDir(), "sumika-dev.exe")
	if err := os.WriteFile(exe, []byte("旧程序"), 0o755); err != nil {
		t.Fatal(err)
	}
	var builds, execs atomic.Int32
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var stderr bytes.Buffer
			_, code, err := Ensure(root, Env{
				Exe: exe, Args: []string{"x"}, Stderr: &stderr, Built: "旧摘要", LockWait: 30 * time.Second,
				Build: func(root, out, digest string) error {
					builds.Add(1)
					time.Sleep(200 * time.Millisecond) // 编译要花时间，别的进程这时都在等锁
					return os.WriteFile(out, []byte("新程序 "+digest), 0o755)
				},
				DiskDigest: func(exe string) (string, error) {
					b, err := os.ReadFile(exe)
					if err != nil {
						return "", err
					}
					return strings.TrimPrefix(string(b), "新程序 "), nil
				},
				Exec: func(exe string, args []string, digest string) (int, error) {
					execs.Add(1)
					b, err := os.ReadFile(exe)
					if err != nil || !strings.HasPrefix(string(b), "新程序") {
						return 1, fmt.Errorf("执行时 exe 不是新程序：%q %v", b, err)
					}
					return 0, nil
				},
			})
			if err == nil && code != 0 {
				err = fmt.Errorf("退出码 %d", code)
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("第 %d 个进程失败：%v", i, err)
		}
	}
	if builds.Load() != 1 || execs.Load() != n {
		t.Fatalf("应该只编 1 次、执行 %d 次，实际编 %d 次、执行 %d 次", n, builds.Load(), execs.Load())
	}
}

func TestAcquireLockTimeoutThenReleaseAndReacquire(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	rel, err := acquireLock(lock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(lock, 150*time.Millisecond); err == nil || !strings.Contains(err.Error(), "重编") {
		t.Fatalf("锁被占着应等到上限后报中文原因：%v", err)
	}
	rel()
	// 放锁以后立刻能再取，锁文件还在（常驻、不删）
	rel2, err := acquireLock(lock, time.Second)
	if err != nil {
		t.Fatalf("放锁以后应能再取：%v", err)
	}
	rel2()
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("锁文件应常驻：%v", err)
	}
}

func TestPrepareBadRootEnvIsAnError(t *testing.T) {
	t.Setenv(EnvNoRebuild, "")
	t.Setenv(EnvRoot, filepath.Join(t.TempDir(), "不存在"))
	var stderr bytes.Buffer
	done, code := Prepare(filepath.Join(t.TempDir(), "x.exe"), Env{Stderr: &stderr})
	if !done || code == 0 || !strings.Contains(stderr.String(), EnvRoot) {
		t.Fatalf("写错的 %s 必须报错退出：done=%v code=%d %q", EnvRoot, done, code, stderr.String())
	}
}

func TestPrepareNoRootAnywhereSkipsWithNote(t *testing.T) {
	t.Setenv(EnvNoRebuild, "")
	t.Setenv(EnvRoot, "")
	var stderr bytes.Buffer
	done, code := Prepare(filepath.Join(t.TempDir(), "x.exe"), Env{Stderr: &stderr})
	if done || code != 0 || !strings.Contains(stderr.String(), "不在仓库里，跳过自我重编") {
		t.Fatalf("没设环境变量又找不到仓库：应照旧跑并提示一句：done=%v code=%d %q", done, code, stderr.String())
	}
}

func mustDigest(t *testing.T, root string) string {
	t.Helper()
	d, err := Digest(root)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// 防递归的环境变量要带摘要：别处遗留的 =1 或别的摘要不能让这次的重编被跳过。
func TestEnsureReexecGuardNeedsMatchingDigest(t *testing.T) {
	root := mkModule(t, false)
	for _, stale := range []string{"1", "别的摘要"} {
		f := newFake(t, "旧摘要")
		t.Setenv(envReexec, stale)
		rebuilt, _, err := Ensure(root, f.Env)
		if err != nil || !rebuilt || f.builds != 1 {
			t.Fatalf("环境变量是 %q 时不该跳过重编：rebuilt=%v err=%v builds=%d", stale, rebuilt, err, f.builds)
		}
	}
}

func TestPrepareNoRebuildWarns(t *testing.T) {
	t.Setenv(EnvNoRebuild, "1")
	var stderr bytes.Buffer
	done, code := Prepare(filepath.Join(t.TempDir(), "x.exe"), Env{Stderr: &stderr})
	if done || code != 0 || !strings.Contains(stderr.String(), "警告") || !strings.Contains(stderr.String(), EnvNoRebuild) {
		t.Fatalf("应放行并警告：done=%v code=%d %q", done, code, stderr.String())
	}
}

func TestBuildEnvDropsTargetVars(t *testing.T) {
	got := buildEnv([]string{"PATH=x", "GOOS=linux", "goarch=arm", "GOFLAGS=-mod=mod", "GOROOT=y"})
	if strings.Join(got, ",") != "PATH=x,GOROOT=y" {
		t.Fatalf("环境变量没清干净：%v", got)
	}
}

func TestGoBinaryTrimpathNeedsEnv(t *testing.T) {
	t.Setenv(EnvGo, "")
	if _, err := goBinaryFrom(""); err == nil || !strings.Contains(err.Error(), EnvGo) || !strings.Contains(err.Error(), "-trimpath") {
		t.Fatalf("GOROOT 为空应提示设 %s：%v", EnvGo, err)
	}
}
