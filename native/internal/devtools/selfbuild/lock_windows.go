//go:build windows

package selfbuild

import (
	"os"

	"golang.org/x/sys/windows"
)

// 锁的是文件偏移 4 GiB 处的 1 个字节（超出文件末尾也能锁）：被锁的区域读写会被别的句柄拒绝，
// 锁在文件开头那几个字节上的话，超时提示读 pid 就会读不出来。
func lockRegion() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 1} }

// tryLock 非阻塞地取独占锁：取到返回 true；被别的句柄（含别的进程）占着返回 false。
func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRegion())
	if err == nil {
		return true, nil
	}
	if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
		return false, nil
	}
	return false, err
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRegion())
}
