//go:build !windows

package main

import "time"

// 非 Windows 没有 QueryPerformanceCounter：用单调时钟的纳秒数代替，量法和 Windows 版一致（Linux CI 上编译、照跑）。
var clockStart = time.Now()

func qpcNow() int64 { return int64(time.Since(clockStart)) }

func qpcMs(from, to int64) float64 { return float64(to-from) / 1e6 }
