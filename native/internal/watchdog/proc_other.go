//go:build !windows

package watchdog

import (
	"errors"
	"syscall"
	"time"
)

// ProcessAlive：发 0 号信号。成功是活着；EPERM 是进程在、只是属于别的用户，也算活着（只有 ESRCH 才是不在），
// 和 rpcwire.ProcessAlive 一致：宁可不重启，也不起第二个后台去抢数据目录（主人 2026-11-19 定）
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func SystemShuttingDown() bool { return false }

// SessionStart 只有 Windows 取得到；别的系统返回零值，对账就不按登录时间过滤
func SessionStart() time.Time { return time.Time{} }
