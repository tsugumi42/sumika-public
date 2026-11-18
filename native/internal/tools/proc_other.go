//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// 别的系统上 pi 的 powershell 工具直接报"只在 Windows 上有"；Sumika 现在只在 Windows 上跑，这里只求编译得过
func hideWindow(cmd *exec.Cmd) {}

func killTree(pid int) { syscall.Kill(-pid, syscall.SIGKILL) }

type shellProcess struct{ pid int }

func startShell(cmd *exec.Cmd) (*shellProcess, error) {
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &shellProcess{pid: cmd.Process.Pid}, nil
}

func (p *shellProcess) killTree() error { killTree(p.pid); return nil }
func (p *shellProcess) close() error    { return nil }
