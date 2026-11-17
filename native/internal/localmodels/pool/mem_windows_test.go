//go:build windows

package pool

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// peakRSS 是进程到现在为止的工作集峰值（字节），量激活余量用。
func peakRSS() uint64 {
	var c processMemoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	h, err := windows.GetCurrentProcess()
	if err != nil {
		return 0
	}
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.cb))
	if r == 0 {
		return 0
	}
	return uint64(c.peakWorkingSetSize)
}
