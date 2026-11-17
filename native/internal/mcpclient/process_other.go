//go:build !windows

package mcpclient

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// ChildrenDieWithHost：这个平台的进程组不会随宿主退出而结束（宿主被杀时子进程可能成为孤儿），
// 所以上一个进程留下的本机服务调用不能自动当作“已终止”，保持隔离，等主人解除。
const ChildrenDieWithHost = false

type processGroup struct{ pid int }

func startProcess(cmd *exec.Cmd) (*processGroup, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processGroup{cmd.Process.Pid}, nil
}
func (p *processGroup) kill() error {
	err := syscall.Kill(-p.pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
func (p *processGroup) close() error { return nil }
func (p *processGroup) waitExit(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		err := syscall.Kill(-p.pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-tick.C:
		case <-timer.C:
			return fmt.Errorf("MCP 进程组仍未全部退出")
		}
	}
}

// launchCommand：非 Windows 没有 cmd 垫片的事，程序直接启动。
func launchCommand(program string, args []string) (*exec.Cmd, error) {
	return exec.Command(program, args...), nil
}
