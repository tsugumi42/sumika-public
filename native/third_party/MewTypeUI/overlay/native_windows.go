//go:build windows

package overlay

import (
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
)

var overlayUser32 = syscall.NewLazyDLL("user32.dll")
var overlayMonitorFromWindow = overlayUser32.NewProc("MonitorFromWindow")
var overlayGetMonitorInfo = overlayUser32.NewProc("GetMonitorInfoW")
var overlaySetWindowPos = overlayUser32.NewProc("SetWindowPos")

type monitorRect struct{ left, top, right, bottom int32 }
type monitorInfo struct {
	size          uint32
	monitor, work monitorRect
	flags         uint32
}

func placeViewer(window, parent *mygo.Window) {
	// Mygo's public screen rectangles project each monitor through its own DPI;
	// mixing them with a window's DPI misplaces windows on heterogeneous displays.
	// Select the owner's physical monitor and let native fullscreen use that HWND.
	monitor, _, _ := overlayMonitorFromWindow.Call(parent.NativeHandle(), 2)
	info := monitorInfo{size: uint32(unsafe.Sizeof(monitorInfo{}))}
	ok, _, err := overlayGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		panic(err)
	}
	r := info.monitor
	ok, _, err = overlaySetWindowPos.Call(window.NativeHandle(), 0, uintptr(r.left), uintptr(r.top), uintptr(r.right-r.left), uintptr(r.bottom-r.top), 0x0010|0x0004)
	if ok == 0 {
		panic(err)
	}
	window.SetFullScreen(true)
}
