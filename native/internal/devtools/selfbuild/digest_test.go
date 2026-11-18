package selfbuild

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ② 等锁上限不小于编译超时。
func TestLockWaitCoversBuildTimeout(t *testing.T) {
	if defaultLockWait < buildTimeout+buildWaitDelay {
		t.Fatalf("等锁上限 %v 小于一次编译最长时间 %v", defaultLockWait, buildTimeout+buildWaitDelay)
	}
}

// ④ Go 版本读 GOROOT/VERSION：换了 Go（VERSION 变了）摘要要变。
func fakeGoRoot(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	name := "go"
	if filepath.Separator == '\\' {
		name = "go.exe"
	}
	os.WriteFile(filepath.Join(root, "bin", name), []byte("x"), 0o755)
	os.WriteFile(filepath.Join(root, "VERSION"), []byte(version+"\ntime 2026-01-01T00:00:00Z\n"), 0o644)
	return filepath.Join(root, "bin", name)
}

func TestDigestFollowsGoRootVersion(t *testing.T) {
	mod := mkModule(t, false)
	t.Setenv(EnvGo, fakeGoRoot(t, "go1.27.4"))
	a, _ := Digest(mod)
	if a2, _ := Digest(mod); a2 != a {
		t.Fatal("同样的 Go 摘要不稳定")
	}
	t.Setenv(EnvGo, fakeGoRoot(t, "go1.28.0"))
	if b, _ := Digest(mod); b == a {
		t.Fatal("换了 Go 版本摘要没变，升级 Go 后不会重编")
	}
	if got := goVersion(); got != "go1.28.0" {
		t.Fatalf("goVersion = %q", got)
	}
}

func TestGoVersionFallsBackToRuntime(t *testing.T) {
	t.Setenv(EnvGo, filepath.Join(t.TempDir(), "没有"))
	if goVersion() == "" {
		t.Fatal("找不到 go 时应退回 runtime.Version()")
	}
	if v := versionAt(t.TempDir()); v == "" {
		t.Fatal("没有 VERSION 文件应退回 runtime.Version()")
	}
}

// ⑥ GOWORK、GOTOOLCHAIN 的值进摘要。
func TestDigestCoversGoworkAndToolchain(t *testing.T) {
	mod := mkModule(t, false)
	t.Setenv(EnvGo, fakeGoRoot(t, "go1.27.4"))
	t.Setenv("GOWORK", "")
	t.Setenv("GOTOOLCHAIN", "")
	base, _ := Digest(mod)
	t.Setenv("GOWORK", filepath.Join(t.TempDir(), "go.work"))
	w, _ := Digest(mod)
	if w == base {
		t.Fatal("GOWORK 变了摘要没变")
	}
	t.Setenv("GOWORK", "")
	t.Setenv("GOTOOLCHAIN", "local")
	if tc, _ := Digest(mod); tc == base {
		t.Fatal("GOTOOLCHAIN 变了摘要没变")
	}
}

// ⑤ 编译命令设了 WaitDelay：超时杀掉 go 以后不会被还攥着管道的子进程拖住。
func TestBuildCmdHasWaitDelay(t *testing.T) {
	cmd := buildCmd(context.Background(), "go", "mod", "out", "d")
	if cmd.WaitDelay != buildWaitDelay || cmd.WaitDelay <= 0 {
		t.Fatalf("WaitDelay = %v", cmd.WaitDelay)
	}
}
