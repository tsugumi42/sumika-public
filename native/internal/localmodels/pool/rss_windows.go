//go:build windows

package pool

import (
	"fmt"
	"syscall"
	"unsafe"
)

// ProcessRSS 是本进程现在的工作集（字节）：Windows 任务管理器里"内存（活动的专用工作集）"那一类的近似，
// 池用它在加载前后对账，/health 用它报 rss_bytes。
//
// 只用标准库的 syscall，不用 golang.org/x/sys/windows：后者会把 net 带进这个包的依赖闭包，
// 而这几个包的依赖里不许有 net（archcheck 的 TestLocalModelsImportWhitelist，这个进程能看到所有送去嵌入的文本）。
// 调的是 kernel32 里的 K32GetProcessMemoryInfo（和 psapi.dll 的 GetProcessMemoryInfo 同一个函数，Windows 7 起转发到 kernel32）：
// kernel32 在 KnownDLLs 里，加载器只从系统目录取，不会被同目录放的同名 dll 顶替。
func ProcessRSS() (uint64, error) {
	var c processMemoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, err
	}
	r, _, callErr := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.cb))
	if r == 0 {
		return 0, fmt.Errorf("GetProcessMemoryInfo：%w", callErr)
	}
	return uint64(c.workingSetSize), nil
}

// PROCESS_MEMORY_COUNTERS，只用到 WorkingSetSize。
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

var procGetProcessMemoryInfo = syscall.NewLazyDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
