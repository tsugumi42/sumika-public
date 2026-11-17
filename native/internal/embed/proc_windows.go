// 照 internal/tools/proc_windows.go 抄的一份（那边的是包内私有的）
package embed

import (
	"os/exec"
	"syscall"
)

// hideWindow：不弹黑框（spawn 的 windowsHide）
func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
