//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package sendlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Windows 用 LockFileEx、Linux 和 BSD 系用 flock（lock_unix.go）；其余系统（js、wasip1、plan9、solaris、aix）
// 不假装能互斥：抢锁直接报错，发送权接不过来
var errNoLock = errors.New("这个系统没有独占文件锁的实现（只实现了 Windows 的 LockFileEx 和 Unix 的 flock）")

func openLockFile(dataDir string) (*os.File, error) {
	path := LockFile(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("建 go 目录：%w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件：%w", err)
	}
	return f, nil
}

func lockExclusive(*os.File) error { return errNoLock }

func unlockExclusive(*os.File) error { return nil }

func isLockBusy(error) bool { return false }
