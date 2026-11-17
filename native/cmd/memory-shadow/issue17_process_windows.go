//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"time"
	"unsafe"
)

// 沿 cmd/e2e-run 的 OS 计数口径：当前进程用户态加内核态、OS 工作集峰值和私有提交。
type issue17Counters struct {
	cb, pageFaultCount                                 uint32
	peakWorkingSetSize, workingSetSize                 uintptr
	quotaPeakPagedPoolUsage, quotaPagedPoolUsage       uintptr
	quotaPeakNonPagedPoolUsage, quotaNonPagedPoolUsage uintptr
	pagefileUsage, peakPagefileUsage, privateUsage     uintptr
}

var issue17MemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

func issue17ProcessStats() (time.Duration, uint64, uint64, error) {
	h := windows.CurrentProcess()
	var m issue17Counters
	m.cb = uint32(unsafe.Sizeof(m))
	if ok, _, err := issue17MemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.cb)); ok == 0 {
		return 0, 0, 0, fmt.Errorf("GetProcessMemoryInfo: %w", err)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, 0, 0, err
	}
	ticks := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	ticks += uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)
	return time.Duration(ticks) * 100 * time.Nanosecond, uint64(m.peakWorkingSetSize), uint64(m.privateUsage), nil
}
