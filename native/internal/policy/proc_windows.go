package policy

import (
	"os/exec"
	"syscall"
)

// hideWindow：不弹黑框（Bun.spawn 的 windowsHide）
func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
