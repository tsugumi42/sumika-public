package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/toolchain"
)

// 桌面程序没有 pi 的 bin 目录：资源根的 vendor/bin 要进 PATH，grep、find 才找得到 rg、fd。
func TestUseVendorBinMakesSearchToolsFindable(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "vendor", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"rg.exe", "fd.exe"} {
		if err := os.WriteFile(filepath.Join(bin, n), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Cleanup(func() { toolchain.Refresh(toolchain.Env{}) }) // 先注册：PATH 还原以后再重查
	t.Setenv("PATH", t.TempDir())
	toolchain.Refresh(toolchain.Env{})
	if toolPath("rg") != "" || toolPath("fd") != "" {
		t.Fatal("前提不成立：PATH 里不该已经有 rg、fd")
	}
	UseVendorBin(root)
	UseVendorBin(root) // 重复调用不重复加
	if got := os.Getenv("PATH"); strings.Count(got, bin) != 1 || !strings.HasPrefix(got, bin) {
		t.Fatalf("PATH 应以 vendor/bin 打头且只有一份：%q", got)
	}
	toolchain.Refresh(toolchain.Env{}) // 登记表按新 PATH 重查
	for _, n := range []string{"rg", "fd"} {
		if p := toolPath(n); !strings.EqualFold(filepath.Dir(p), bin) {
			t.Fatalf("%s 没找到 vendor/bin 里的：%q", n, p)
		}
	}
}

func TestUseVendorBinWithoutDirLeavesPathAlone(t *testing.T) {
	t.Setenv("PATH", "X")
	UseVendorBin(t.TempDir())
	if os.Getenv("PATH") != "X" {
		t.Fatalf("没有 vendor/bin 时不该动 PATH：%q", os.Getenv("PATH"))
	}
}
