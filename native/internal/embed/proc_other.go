//go:build !windows

package embed

import "os/exec"

// Sumika 现在只在 Windows 上跑，这里只求编译得过（照 internal/tools/proc_other.go）
func hideWindow(cmd *exec.Cmd) {}
