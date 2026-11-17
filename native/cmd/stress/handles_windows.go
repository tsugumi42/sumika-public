//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

var ntquery = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQuerySystemInformation")
var ntobject = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")
var finalPath = kernel32.NewProc("GetFinalPathNameByHandleW")
var getThreadID = kernel32.NewProc("GetThreadId")

// statusInvalidHandle（STATUS_INVALID_HANDLE）表示系统列出这个句柄之后、查询它之前，
// 句柄被别的协程关掉了。快照本来就可能过期，这不是枚举失败，跳过这一项。
const statusInvalidHandle = 0xc0000008

type handleEntry struct {
	Object, PID, Handle  uintptr
	Access               uint32
	Trace, Type          uint16
	Attributes, Reserved uint32
}

func threadCount() (int, error) {
	h, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if e != nil {
		return 0, e
	}
	defer windows.CloseHandle(h)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	e = windows.Thread32First(h, &entry)
	n := 0
	for e == nil {
		if entry.OwnerProcessID == uint32(os.Getpid()) {
			n++
		}
		e = windows.Thread32Next(h, &entry)
	}
	if e != windows.ERROR_NO_MORE_FILES {
		return 0, e
	}
	return n, nil
}
func snapshotHandles() (handleSnapshot, error) {
	s := handleSnapshot{Types: map[string]int{}, ThreadIDs: map[uint32]int{}}
	var err error
	s.Threads, err = threadCount()
	if err != nil {
		return s, err
	}
	// 系统快照包含所有进程，但只查询本进程仍拥有的句柄，不复制、不关闭被测资源。
	size := uint32(1 << 20)
	var buf []byte
	for {
		buf = make([]byte, size)
		var needed uint32
		status, _, _ := ntquery.Call(64, uintptr(unsafe.Pointer(&buf[0])), uintptr(size), uintptr(unsafe.Pointer(&needed)))
		if uint32(status) == 0xc0000004 {
			size = max(size*2, needed)
			continue
		}
		if int32(status) < 0 {
			return s, fmt.Errorf("NtQuerySystemInformation: %#x", status)
		}
		break
	}
	count := *(*uintptr)(unsafe.Pointer(&buf[0]))
	offset := unsafe.Sizeof(uintptr(0)) * 2
	cache := map[uint16]string{}
	for i := range count {
		entry := *(*handleEntry)(unsafe.Pointer(&buf[offset+i*unsafe.Sizeof(handleEntry{})]))
		if entry.PID != uintptr(os.Getpid()) {
			continue
		}
		name := cache[entry.Type]
		if name == "" {
			b := make([]byte, 4096)
			var needed uint32
			status, _, _ := ntobject.Call(entry.Handle, 2, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), uintptr(unsafe.Pointer(&needed)))
			if uint32(status) == statusInvalidHandle {
				continue // 句柄已关，快照过期是正常的
			}
			if int32(status) < 0 {
				return s, fmt.Errorf("NtQueryObject(%x): %#x", entry.Handle, status)
			}
			u := (*windows.NTUnicodeString)(unsafe.Pointer(&b[0]))
			name = u.String()
			cache[entry.Type] = name
		}
		s.Total++
		s.Types[name]++
		if name == "Thread" {
			// 句柄在查询名字之后可能已经关了；GetThreadId 失败返回 0，跳过，
			// 不要把 0 当成线程号记进去。
			id, _, _ := getThreadID.Call(entry.Handle)
			if id != 0 {
				s.ThreadIDs[uint32(id)]++
			}
		}
		if name == "File" {
			kind, e := windows.GetFileType(windows.Handle(entry.Handle))
			if e == windows.ERROR_INVALID_HANDLE {
				continue // 同样是在列出和查询之间关掉了
			}
			if e != nil || kind != windows.FILE_TYPE_DISK {
				s.Files = append(s.Files, fmt.Sprintf("handle=%#x FileType=%d（非磁盘，不查询路径）", entry.Handle, kind))
				continue
			}
			b := make([]uint16, 32768)
			n, _, _ := finalPath.Call(entry.Handle, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0)
			if n > 0 && n < uintptr(len(b)) {
				s.Files = append(s.Files, windows.UTF16ToString(b[:n]))
			} else {
				s.Files = append(s.Files, fmt.Sprintf("handle=%#x (管道/控制台或无文件路径)", entry.Handle))
			}
		}
	}
	return s, nil
}
