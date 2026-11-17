//go:build !windows

package main

import "time"

var tickBase = time.Now()

// tick 是单调时刻（非 Windows 的 time.Since 精度够用）
func tick() time.Duration { return time.Since(tickBase) }
