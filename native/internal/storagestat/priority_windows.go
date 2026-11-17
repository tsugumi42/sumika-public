//go:build windows

package storagestat

import (
	"runtime"

	"golang.org/x/sys/windows"
)

var procSetThreadPriority = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority")

const (
	threadModeBackgroundBegin = 0x00010000
	threadModeBackgroundEnd   = 0x00020000
)

// lowerPriority 把当前线程降到后台优先级（CPU 和磁盘读写都让给别人），返回恢复函数。
// 扫描协程在整个扫描期间锁在这个线程上；设不成功不算错，只是没降
func lowerPriority() (restore func()) {
	runtime.LockOSThread()
	h, _ := windows.GetCurrentThread()
	r, _, _ := procSetThreadPriority.Call(uintptr(h), threadModeBackgroundBegin)
	if r == 0 {
		return runtime.UnlockOSThread
	}
	return func() {
		procSetThreadPriority.Call(uintptr(h), threadModeBackgroundEnd)
		runtime.UnlockOSThread()
	}
}
