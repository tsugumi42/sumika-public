//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// 别的系统上 pi 的 powershell 工具直接报"只在 Windows 上有"；Sumika 现在只在 Windows 上跑，这里只求编译得过
func hideWindow(cmd *exec.Cmd) {}

func killTree(pid int) {
	// pid 为 0 或负数时 -pid 是“本进程组”或别的组，pid 为 1 时是 init 的组：这些都不是我们起的命令，绝不能发信号
	if pid <= 1 {
		return
	}
	syscall.Kill(-pid, syscall.SIGKILL)
}

type shellProcess struct{ pid int }

func startShell(cmd *exec.Cmd) (*shellProcess, error) {
	hideWindow(cmd)
	// 自成一个进程组：killTree 向 -pid（整组）发 SIGKILL 才找得到目标。不设的话没有以它为组长的进程组，
	// kill 返回 ESRCH 被忽略，命令（和它起的子进程）继续跑
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &shellProcess{pid: cmd.Process.Pid}, nil
}

func (p *shellProcess) killTree() error { killTree(p.pid); return nil }
func (p *shellProcess) close() error    { return nil }
