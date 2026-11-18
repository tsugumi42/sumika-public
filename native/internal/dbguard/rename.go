package dbguard

import (
	"errors"
	"syscall"
	"time"
)

// renameRetryWindow 是替换文件被临时拒绝时的重试总时长，照 Go 源码 cmd/internal/robustio
// 的 arbitraryTimeout（约 2 秒），和 internal/secrets 的改名重试一致。
const renameRetryWindow = 2 * time.Second

var renameRetryWait = time.Sleep

// renameReplacing 调 rename 并照 robustio 重试：Windows 上杀毒、索引程序正开着文件时
// 改名会被拒（拒绝访问、共享冲突、临时文件一时找不到），短时间内指数退避再试约 2 秒；
// 其余错误立刻返回。哪些错误码值得重试只在 Windows 上认（rename_windows.go），非 Windows
// 一律不重试（rename_other.go）：errno 数字在两个平台上含义不同。
func renameReplacing(rename func(old, new string) error, oldpath, newpath string) error {
	return retryEphemeral(func() error { return rename(oldpath, newpath) })
}

// retryEphemeral 反复调用 op，直到不返回“等一等会好”的错误或重试窗口用完。
// 改名和删旁文件共用这一套：Windows 上杀毒、索引程序短暂占着文件时，删除也会被同样拒绝。
func retryEphemeral(op func() error) error {
	var (
		bestErr     error
		lowestErrno syscall.Errno
		start       time.Time
		nextSleep   = time.Millisecond
	)
	for {
		err := op()
		if err == nil || !ephemeralRenameErr(err) {
			return err
		}
		// 照 robustio：重试用尽时优先返回 errno 最小的那个（最像根因）。
		if errno, ok := errnoOf(err); ok && (lowestErrno == 0 || errno < lowestErrno) {
			bestErr, lowestErrno = err, errno
		} else if bestErr == nil {
			bestErr = err
		}
		if start.IsZero() {
			start = time.Now()
		} else if time.Since(start)+nextSleep >= renameRetryWindow {
			return bestErr
		}
		renameRetryWait(nextSleep)
		nextSleep += nextSleep
	}
}

// errnoOf 取出错误里的系统错误码（os.Rename 返回 *os.LinkError，包着 Errno）。
func errnoOf(err error) (syscall.Errno, bool) {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno, true
	}
	return 0, false
}
