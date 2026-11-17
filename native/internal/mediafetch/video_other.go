//go:build !windows

package mediafetch

import (
	"os/exec"
)

func lowVideoPriority(cmd *exec.Cmd) {
	// 非 Windows 用系统 nice，仍然是 argv 调用，不经过 shell。
	cmd.Args = append([]string{"nice", "-n", "19", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/usr/bin/nice"
}
