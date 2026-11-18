//go:build unix

package rpcwire

import (
	"errors"
	"syscall"
)

// ProcessAlive 说 run 文件里记的后台进程还在不在。Unix 上 os.FindProcess 永远成功、不能用来判断，
// 照通行做法发 0 号信号：进程在就成功；EPERM 是在但属于别的用户，也算活着；只有 ESRCH 才是不在。
// pid 必须大于 0：kill(0, …) 是发给整个进程组，kill(-n, …) 是发给进程组 n，都不是查进程
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}
