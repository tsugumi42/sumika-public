//go:build windows

package ort

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// 内存测量的 Windows 实现：真 dll 的内存测试（GM2 验收 9）用。

type procMem struct {
	WorkingSet     uint64 // 当前工作集
	PeakWorkingSet uint64 // 进程到现在为止的工作集峰值
	Private        uint64 // 私有提交（PrivateUsage）
	PeakPagefile   uint64 // 提交峰值
}

// PROCESS_MEMORY_COUNTERS_EX
type pmcEx struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

func readMem() (procMem, bool) {
	var c pmcEx
	c.CB = uint32(unsafe.Sizeof(c))
	h, err := windows.GetCurrentProcess()
	if err != nil {
		return procMem{}, false
	}
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.CB))
	if r == 0 {
		return procMem{}, false
	}
	return procMem{
		WorkingSet:     uint64(c.WorkingSetSize),
		PeakWorkingSet: uint64(c.PeakWorkingSetSize),
		Private:        uint64(c.PrivateUsage),
		PeakPagefile:   uint64(c.PeakPagefileUsage),
	}, true
}

// limitProcessMemory 把当前进程放进一个 Job Object，单进程提交上限设成 bytes：
// 8192 token 的前向要多少内存没量过，上限是给这台电脑兜底的（超了是 ORT 申请失败，不是把整机拖死）。
func limitProcessMemory(bytes uint64) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("CreateJobObject：%w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
	info.ProcessMemoryLimit = uintptr(bytes)
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fmt.Errorf("SetInformationJobObject：%w", err)
	}
	self, err := windows.GetCurrentProcess()
	if err != nil {
		return err
	}
	if err := windows.AssignProcessToJobObject(job, self); err != nil {
		return fmt.Errorf("AssignProcessToJobObject：%w", err)
	}
	return nil
}

// cpuName 读注册表里的处理器名，参考数据的 ref-meta.json 里记的是同一个字段。
func cpuName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}
