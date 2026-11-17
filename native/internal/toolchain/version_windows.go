//go:build windows

package toolchain

import (
	"os/exec"
	"syscall"
)

// hideWindow 让取版本的子进程不弹窗（照 watchdog/toast_windows.go 的写法）。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
