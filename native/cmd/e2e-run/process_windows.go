//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"time"
	"unsafe"
)

// 沿 soak 的 PROCESS_MEMORY_COUNTERS_EX 布局；直接读 OS 峰值，不靠低频采样猜峰值。
type memoryCounters struct {
	cb, pageFaultCount                                 uint32
	peakWorkingSetSize, workingSetSize                 uintptr
	quotaPeakPagedPoolUsage, quotaPagedPoolUsage       uintptr
	quotaPeakNonPagedPoolUsage, quotaNonPagedPoolUsage uintptr
	pagefileUsage, peakPagefileUsage, privateUsage     uintptr
}

var getMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

func processStats() (time.Duration, uint64, error) {
	h := windows.CurrentProcess()
	var m memoryCounters
	m.cb = uint32(unsafe.Sizeof(m))
	if ok, _, err := getMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.cb)); ok == 0 {
		return 0, 0, fmt.Errorf("GetProcessMemoryInfo: %w", err)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, 0, err
	}
	ticks := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	ticks += uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)
	return time.Duration(ticks) * 100 * time.Nanosecond, uint64(m.peakWorkingSetSize), nil
}
