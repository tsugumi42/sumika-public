package tools

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"sumika-native/internal/toolchain"
)

// Windows 上的剪贴板和默认程序打开。剪贴板函数直接调 user32 / kernel32（x/sys/windows 这一版没导出 Global* 和剪贴板），
// OpenClipboard 和 CloseClipboard 必须在同一个系统线程上，所以先 LockOSThread。

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	kernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard          = user32.NewProc("OpenClipboard")
	procCloseClipboard         = user32.NewProc("CloseClipboard")
	procEmptyClipboard         = user32.NewProc("EmptyClipboard")
	procIsClipboardFormatAvail = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData       = user32.NewProc("GetClipboardData")
	procSetClipboardData       = user32.NewProc("SetClipboardData")
	procGlobalAlloc            = kernel32.NewProc("GlobalAlloc")
	procGlobalFree             = kernel32.NewProc("GlobalFree")
	procGlobalLock             = kernel32.NewProc("GlobalLock")
	procGlobalUnlock           = kernel32.NewProc("GlobalUnlock")
	procGlobalSize             = kernel32.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// openClipboard 别的程序占着剪贴板时短暂重试
func openClipboard() error {
	var lastErr error
	for i := 0; i < 10; i++ {
		r, _, e := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		lastErr = e
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("剪贴板被别的程序占着：%w", lastErr)
}

// readClipboardText 有文字就返回 (文字, true, nil)；剪贴板里没有文字格式（图片、文件等）返回 (空, false, nil)
func readClipboardText() (string, bool, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return "", false, err
	}
	defer procCloseClipboard.Call()
	if r, _, _ := procIsClipboardFormatAvail.Call(cfUnicodeText); r == 0 {
		return "", false, nil
	}
	h, _, e := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false, fmt.Errorf("取剪贴板文字：%w", e)
	}
	size, _, _ := procGlobalSize.Call(h)
	p, _, e := procGlobalLock.Call(h)
	if p == 0 {
		return "", false, fmt.Errorf("锁定剪贴板内存：%w", e)
	}
	defer procGlobalUnlock.Call(h)
	n := int(size) / 2
	units := unsafe.Slice((*uint16)(pointerFrom(&p)), n)
	end := n
	for i, c := range units {
		if c == 0 {
			end = i
			break
		}
	}
	return windows.UTF16ToString(units[:end]), true, nil
}

// writeClipboardText 把文字（UTF-16，带结尾 0）放进剪贴板，替换原来的内容
func writeClipboardText(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	units, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	h, _, e := procGlobalAlloc.Call(gmemMoveable, uintptr(len(units)*2))
	if h == 0 {
		return fmt.Errorf("分配剪贴板内存：%w", e)
	}
	p, _, e := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("锁定剪贴板内存：%w", e)
	}
	copy(unsafe.Slice((*uint16)(pointerFrom(&p)), len(units)), units)
	procGlobalUnlock.Call(h)

	if err := openClipboard(); err != nil {
		procGlobalFree.Call(h)
		return err
	}
	defer procCloseClipboard.Call()
	if r, _, e := procEmptyClipboard.Call(); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("清空剪贴板：%w", e)
	}
	// 成功以后内存归系统，不能再释放
	if r, _, e := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("写剪贴板：%w", e)
	}
	return nil
}

// pointerFrom 把系统调用返回的地址（uintptr）当成指针用。Windows 给的是 GlobalLock 锁住的内存，Go 的 GC 不会动它。
func pointerFrom(addr *uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(addr))
}

// openDefault 用系统默认程序打开。直接调 rundll32 的 FileProtocolHandler，不经过 shell，参数分开传。
func openDefault(target string) error {
	r, ok := toolchain.Lookup("rundll32")
	if !ok {
		return errors.New("没找到 rundll32，打不开默认程序")
	}
	cmd := exec.Command(r.Path, "url.dll,FileProtocolHandler", target)
	hideWindow(cmd)
	return cmd.Run()
}
