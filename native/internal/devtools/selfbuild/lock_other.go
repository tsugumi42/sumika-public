//go:build !windows

package selfbuild

import (
	"errors"
	"os"
	"syscall"
)

// tryLock 非阻塞地取独占锁（flock）：取到返回 true；被别的打开文件描述占着返回 false。
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
