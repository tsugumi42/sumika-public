//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package sendlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// 非 Windows 的独占文件锁：flock(LOCK_EX|LOCK_NB)，语义对齐 lock_windows.go 的 LockFileEx 用法：
// 别人拿着就立刻失败、不排队；进程退出（包括被杀）时系统放掉。
// 和 Windows 那份的差别：Windows 锁的是偏移 2^62 处的一个字节，这里锁整个文件，但 flock 是劝告锁、不挡读写，
// 持锁期间别的进程照样读得到记录，对"互斥"和"读得到"两条要求等价。
// 和 Bun 不互斥：Bun 后台只在 Windows 上跑，Linux 上没有第二方（规格 5.2 注明，同提交）。
// 两次 open 得到两个独立的文件描述，flock 在它们之间互斥，同进程里两个持有者也互斥，和 LockFileEx 按句柄锁一致。

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

// lockExclusive 抢独占锁：别人拿着就立刻失败，不排队（排着队就没法在"发送之前"判断能不能发）
func lockExclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// unlockExclusive 放开这次的锁
func unlockExclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// isLockBusy 说这次抢锁失败是因为别的进程正拿着，不是文件坏了、盘满了之类的
func isLockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK)
}
