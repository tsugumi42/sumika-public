package main

import (
	"os"
	"sync"
	"testing"

	"sumika-native/internal/policy"
)

// sharedAnalyzer 是整个测试进程共用的一个命令解析器（常驻 pwsh）。
// 不设 SharedAnalyzer 时每开一个后台都会在 3 秒后预热一个真 pwsh，并行跑时十几个 pwsh 一起起，
// 又慢又吃杀毒；共用一个，解析本身是串行排队的，结果和各开一个一样。
var sharedAnalyzer = sync.OnceValue(policy.NewAnalyzer)

var analyzerStarted sync.Once

func testAnalyzer() *policy.Analyzer {
	a := sharedAnalyzer()
	analyzerStarted.Do(func() { a.Warmup() })
	return a
}

// TestMain 把能指到真实数据的两个环境变量整个测试进程一次清空。
// 测试只用临时数据目录，不读用户机器上的配置；放在这里而不是每条测试里 t.Setenv，
// 因为 t.Setenv 不能和 t.Parallel 一起用，而这两个变量所有测试都要同一个值（空）。
func TestMain(m *testing.M) {
	os.Setenv("SUMIKA_CONFIG", "")
	os.Setenv("SUMIKA_DATA_DIR", "")
	code := m.Run()
	sharedAnalyzer().Close()
	os.Exit(code)
}
