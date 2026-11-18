//go:build windows

package sendlock

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// 锁的范围：锁文件里偏移 2^62 处的一个字节（OffsetHigh = 0x40000000、Offset = 0），
// 不跟 JSON 内容重叠，所以持锁期间别的进程还能读记录、手动看文件。会抢这把锁的进程必须锁同一段（Bun 那边照这个写）。
// 锁在文件尾以后是允许的，文件还没有内容也行（规格 5.2 锁的细节）
const (
	lockOffset = int64(1) << 62
	lockBytes  = 1
)

// openLockFile 按规格 5.2 的共享方式打开（没有就建）锁文件：能读写，别的进程同时读、写、删都不挡。
// 不用 os.OpenFile：它不带 FILE_SHARE_DELETE，和 Bun 那边的 CreateFileW 不一致
func openLockFile(dataDir string) (*os.File, error) {
	path := LockFile(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("建 go 目录：%w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件：%w", err)
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件：%w", err)
	}
	return os.NewFile(uintptr(h), path), nil
}

// lockExclusive 抢独占锁：别人拿着就立刻失败，不排队（排着队就没法在"发送之前"判断能不能发）
func lockExclusive(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockBytes, 0, lockRange())
}

// unlockExclusive 放开这次锁住的字节范围
func unlockExclusive(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockBytes, 0, lockRange())
}

// isLockBusy 说这次抢锁失败是因为别的进程正拿着，不是文件坏了、盘满了之类的
func isLockBusy(err error) bool {
	return err == windows.ERROR_LOCK_VIOLATION
}

func lockRange() *windows.Overlapped {
	ol := new(windows.Overlapped)
	ol.Offset = uint32(lockOffset & 0xffffffff)
	ol.OffsetHigh = uint32(lockOffset >> 32)
	return ol
}
