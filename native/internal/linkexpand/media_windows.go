package linkexpand

import (
	"os/exec"
	"syscall"
)

func mediaPriority(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000040}
}
