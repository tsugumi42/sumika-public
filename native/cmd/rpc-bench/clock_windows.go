package main

import (
	"syscall"
	"time"
	"unsafe"
)

// Go 在 Windows 上的 time.Now/Since 粒度约 0.5～1 毫秒，量几十微秒的首字差要用 QueryPerformanceCounter
var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	qpc      = kernel32.NewProc("QueryPerformanceCounter")
	qpf      = kernel32.NewProc("QueryPerformanceFrequency")
	qpcFreq  = func() int64 {
		var f int64
		qpf.Call(uintptr(unsafe.Pointer(&f)))
		return f
	}()
)

// tick 是高精度单调时刻
func tick() time.Duration {
	var c int64
	qpc.Call(uintptr(unsafe.Pointer(&c)))
	return time.Duration(float64(c) / float64(qpcFreq) * float64(time.Second))
}
