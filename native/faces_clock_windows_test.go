package main

// 小黄脸解码量测共用高精度时钟；旧预排缓存的专用滚动基准已移除。

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32    = windows.NewLazySystemDLL("kernel32.dll")
	procQPC     = kernel32.NewProc("QueryPerformanceCounter")
	procQPCFreq = kernel32.NewProc("QueryPerformanceFrequency")
	qpcFreq     = qpcFrequency()
)

func qpcFrequency() float64 {
	var f int64
	if r, _, _ := procQPCFreq.Call(uintptr(unsafe.Pointer(&f))); r == 0 || f == 0 {
		panic("QueryPerformanceFrequency 取不到")
	}
	return float64(f)
}

func qpcNow() int64 {
	var c int64
	if r, _, _ := procQPC.Call(uintptr(unsafe.Pointer(&c))); r == 0 {
		panic("QueryPerformanceCounter 取不到")
	}
	return c
}

func qpcMs(from, to int64) float64 { return float64(to-from) / qpcFreq * 1000 }
