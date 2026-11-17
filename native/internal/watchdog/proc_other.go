//go:build !windows

package watchdog

import (
	"os"
	"syscall"
	"time"
)

func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func SystemShuttingDown() bool { return false }

// SessionStart 只有 Windows 取得到；别的系统返回零值，对账就不按登录时间过滤
func SessionStart() time.Time { return time.Time{} }
