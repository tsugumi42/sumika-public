//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 沿 cmd/e2e-run 的 PROCESS_MEMORY_COUNTERS 布局，读 OS 累计峰值。
func peakMemory(pid int) (uint64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var m struct {
		Size, PageFaultCount                                                                                                                       uint32
		PeakWorkingSet, WorkingSet, QuotaPeakPagedPool, QuotaPagedPool, QuotaPeakNonPagedPool, QuotaNonPagedPool, PagefileUsage, PeakPagefileUsage uintptr
	}
	m.Size = uint32(unsafe.Sizeof(m))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	ok, _, callErr := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.Size))
	if ok == 0 {
		return 0, fmt.Errorf("GetProcessMemoryInfo: %w", callErr)
	}
	return uint64(m.PeakWorkingSet), nil
}

func runnerCPU() (float64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	ticks := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	ticks += uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)
	return float64(ticks) / 1e7, nil
}
