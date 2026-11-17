//go:build windows

package turn

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 影子对比的计时和内存（只在测试里用）：进程什么时候起来的、现在的常驻（工作集）和峰值，单位 MB

type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

func processStart() time.Time {
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &c, &e, &k, &u) != nil {
		return time.Time{}
	}
	return time.Unix(0, c.Nanoseconds())
}

func processMemoryMB() (rss, peak float64) {
	var m processMemoryCounters
	m.cb = uint32(unsafe.Sizeof(m))
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&m)), uintptr(m.cb))
	if r == 0 {
		return 0, 0
	}
	return float64(m.workingSetSize) / (1 << 20), float64(m.peakWorkingSetSize) / (1 << 20)
}
