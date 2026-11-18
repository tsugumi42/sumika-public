//go:build !windows

package main

// 当前巡视验收只量 Windows 工作集；其他平台用 0 表示未采集。
func peakMemory(_ int) (uint64, error) { return 0, nil }
func runnerCPU() (float64, error)      { return 0, nil }
