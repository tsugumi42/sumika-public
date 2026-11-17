package resstat

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"sumika-native/internal/procreg"
)

// 读进程的 Windows 实现（规格-运行页 第四节第 1 条）。全程只读：用最小的查询权限（PROCESS_QUERY_LIMITED_INFORMATION）
// 打开进程，不结束、不改动任何进程。打不开（没权限、已退出）就报错，调用方把那一行的数值留空，不写 0。

type winSystem struct{}

func newSystem() system { return winSystem{} }

// list 用 Toolhelp 快照列出所有进程的 pid、父 pid 和 exe 名
func (winSystem) list() ([]procEntry, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var out []procEntry
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out = append(out, procEntry{pid: int(e.ProcessID), ppid: int(e.ParentProcessID), name: windows.UTF16ToString(e.ExeFile[:])})
	}
	return out, nil
}

func (winSystem) creation(pid int) int64 { return procreg.CreationTime(pid) }

// processMemoryCountersEx2 是 PROCESS_MEMORY_COUNTERS_EX2（x/sys 没封装）：专用工作集在 PrivateWorkingSetSize
type processMemoryCountersEx2 struct {
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
	PrivateWorkingSetSize      uintptr
	SharedCommitUsage          uint64
}

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

func (winSystem) info(pid int) (procInfo, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) { // pid 已经没有进程
			return procInfo{}, errGone
		}
		return procInfo{}, err
	}
	defer windows.CloseHandle(h)
	var in procInfo
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil {
		return procInfo{}, errors.New("读不到进程时间")
	}
	in.created = creation.Nanoseconds()
	in.cpu = time.Duration(filetimeTicks(kernel)+filetimeTicks(user)) * 100 // FILETIME 是 100 纳秒
	in.hasCPU = true
	var c processMemoryCountersEx2
	c.CB = uint32(unsafe.Sizeof(c))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.CB)); r != 0 {
		in.privateWS, in.hasPrivate = int64(c.PrivateWorkingSetSize), true
		in.workingSet, in.hasWS = int64(c.WorkingSetSize), true
		in.commit, in.hasCommit = int64(c.PrivateUsage), true
		return in, nil
	}
	// 老系统不认 EX2：退回基础结构，只有工作集；调用方会把整列改成"工作集"，不静默换口径
	var b struct {
		CB, PageFaultCount                                                                         uint32
		PeakWorkingSetSize, WorkingSetSize, QPPP, QPP, QPNPP, QNPP, PagefileUsage, PeakPagefileUse uintptr
	}
	b.CB = uint32(unsafe.Sizeof(b))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&b)), uintptr(b.CB)); r != 0 {
		in.workingSet, in.hasWS = int64(b.WorkingSetSize), true
	}
	return in, nil
}

func filetimeTicks(f windows.Filetime) int64 {
	return int64(uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime))
}
