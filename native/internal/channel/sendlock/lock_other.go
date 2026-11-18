//go:build !windows

package sendlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// 规格 5.2 只写了 Windows 的 LockFileEx（主人这台机器是 Windows）。别的系统上不假装能互斥：抢锁直接报错，发送权接不过来
var errNoLock = errors.New("这个系统没有独占文件锁的实现（规格 5.2 只写了 Windows 的 LockFileEx）")

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
