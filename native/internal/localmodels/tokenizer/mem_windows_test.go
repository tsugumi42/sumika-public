//go:build windows

package tokenizer

import (
	"syscall"
	"time"
	"unsafe"
)

// 进程工作集（PROCESS_MEMORY_COUNTERS.WorkingSetSize）。x/sys/windows 没有 GetProcessMemoryInfo，直接调 psapi。
var procGetProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func workingSetBytes() int64 {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var c processMemoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.cb)); r == 0 {
		return 0
	}
	return int64(c.WorkingSetSize)
}

// processCPU 是进程到现在用掉的 CPU 时间（用户态加内核态）。机器上别的进程在抢 CPU 时，墙钟耗时会虚高，CPU 时间稳得多。
func processCPU() time.Duration {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0
	}
	return time.Duration(kernel.Nanoseconds()) + time.Duration(user.Nanoseconds())
}
