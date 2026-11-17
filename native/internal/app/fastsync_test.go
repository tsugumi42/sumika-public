package app

import (
	"sync"

	"sumika-native/internal/policy"
	"sumika-native/internal/store"
)

// 测试的共用加速：库不等落盘、全包共用一个常驻 pwsh（见各自函数的说明）
func init() {
	store.UseFastSyncForTests()
}

var (
	sharedAnalyzer     = policy.NewAnalyzer()
	sharedAnalyzerOnce sync.Once
)

// testAnalyzer 给每个测试的 Options 用；第一次用时在后台预热，之后都是现成的
func testAnalyzer() *policy.Analyzer {
	sharedAnalyzerOnce.Do(func() { sharedAnalyzer.Warmup() })
	return sharedAnalyzer
}
