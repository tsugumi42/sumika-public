//go:build !windows

package engine

// cpuName 只有 Windows 有实现；别的系统上读不到，依赖参照 CPU 的真模型测试跳过。
func cpuName() string { return "" }
