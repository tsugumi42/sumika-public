//go:build windows

package linkcard

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateBytes 是本进程的私有内存（GetProcessMemoryInfo 的 PrivateUsage，含 Go 堆之外的原生内存）。
func privateBytes() (uint64, bool) {
	var m struct {
		Size, PageFaultCount                                                                                                                                     uint32
		PeakWorkingSet, WorkingSet, QuotaPeakPagedPool, QuotaPagedPool, QuotaPeakNonPagedPool, QuotaNonPagedPool, PagefileUsage, PeakPagefileUsage, PrivateUsage uintptr
	}
	m.Size = uint32(unsafe.Sizeof(m))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	ok, _, _ := proc.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&m)), uintptr(m.Size))
	if ok == 0 {
		return 0, false
	}
	return uint64(m.PrivateUsage), true
}
