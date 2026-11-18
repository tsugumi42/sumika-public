//go:build !windows

package linkexpand

import "os/exec"

// 其他平台沿现有媒体工具的进程优先级策略。
func mediaPriority(cmd *exec.Cmd) {}
