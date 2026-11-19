//go:build !windows

package ort

import "errors"

// 内存测量只有 Windows 实现；别的系统上相关测试跳过。

type procMem struct{ WorkingSet, PeakWorkingSet, Private, PeakPagefile uint64 }

func readMem() (procMem, bool)              { return procMem{}, false }
func limitProcessMemory(bytes uint64) error { return errors.New("只有 Windows 有") }
func cpuName() string                       { return "" }
