//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"unsafe"
)

// PROCESS_MEMORY_COUNTERS_EX；末尾的 PrivateUsage 是提交的私有内存，不能拿 PagefileUsage 冒充。
type memoryCounters struct {
	cb, pageFaultCount                                 uint32
	peakWorkingSetSize, workingSetSize                 uintptr
	quotaPeakPagedPoolUsage, quotaPagedPoolUsage       uintptr
	quotaPeakNonPagedPoolUsage, quotaNonPagedPoolUsage uintptr
	pagefileUsage, peakPagefileUsage, privateUsage     uintptr
}

var kernel32 = windows.NewLazySystemDLL("kernel32.dll")
var getMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
var getHandleCount = kernel32.NewProc("GetProcessHandleCount")

func processStats() (workingSet, private, handles uint64, err error) {
	var m memoryCounters
	m.cb = uint32(unsafe.Sizeof(m))
	h := uintptr(windows.CurrentProcess())
	if ok, _, e := getMemoryInfo.Call(h, uintptr(unsafe.Pointer(&m)), uintptr(m.cb)); ok == 0 {
		return 0, 0, 0, fmt.Errorf("GetProcessMemoryInfo：%w", e)
	}
	var count uint32
	if ok, _, e := getHandleCount.Call(h, uintptr(unsafe.Pointer(&count))); ok == 0 {
		return 0, 0, 0, fmt.Errorf("GetProcessHandleCount：%w", e)
	}
	return uint64(m.workingSetSize), uint64(m.privateUsage), uint64(count), nil
}
