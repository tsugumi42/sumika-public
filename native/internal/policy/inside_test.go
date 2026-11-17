package policy

// IsInside 用本机的路径跑（不写死 Windows 的盘符和反斜杠）：Windows、Linux 上都该对。
// golden_test.go 用的是 Windows 的字面路径，只在 Windows 上对拍 Bun；这一份进 Linux 的 CI（.github/workflows/race.yml）

import (
	"path/filepath"
	"testing"
)

func TestIsInsideNativePaths(t *testing.T) {
	d := t.TempDir()
	cases := []struct {
		target string
		want   bool
	}{
		{filepath.Join(d, "a.txt"), true},            // 目录里的文件
		{filepath.Join(d, "sub", "b.txt"), true},     // 下一级目录里的
		{d, true},                                    // 目录本身
		{"a.txt", true},                              // 相对路径按工作目录算
		{filepath.Join(d, "..", "x.txt"), false},     // 走出去的
		{d + "x", false},                             // 名字开头一样的兄弟目录
		{filepath.Join(filepath.Dir(d), "y"), false}, // 上一级的
	}
	for _, c := range cases {
		if got := IsInside(c.target, []string{d}, d); got != c.want {
			t.Errorf("IsInside(%q, [%q]) = %v，应该是 %v", c.target, d, got, c.want)
		}
	}
}
