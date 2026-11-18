package mediafetch

import (
	"os/exec"
	"syscall"
)

// 在创建时设优先级，避免普通优先级解码先抢一阵 CPU。
func lowVideoPriority(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000040} // CREATE_NO_WINDOW | IDLE_PRIORITY_CLASS
}
