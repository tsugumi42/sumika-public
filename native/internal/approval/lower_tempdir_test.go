package approval

import (
	"os"
	"testing"
)

// lowerTempDir 给一个全小写路径的临时工作目录。
// t.TempDir() 的路径带测试函数名（TestAskRuleInGate 这种大小写混合），而策略层的授权键、IsInside 无条件转小写比较
// （policy.IsInside、policy.go 的 normDir、linkKey）；在大小写敏感的系统上，混合大小写的真实路径和转小写后的键对不上，
// 规则就不命中。这个差别单独挂在 docs/待办.md 第八节 Linux 那条下面；这批用例测的是 ask 压过 allow、deny 不分范围这些顺序，
// 不是大小写，所以工作目录用全小写的，让顺序真的被测到。Windows 上路径不分大小写，行为和 t.TempDir() 一样。
func lowerTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sumika-lc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
