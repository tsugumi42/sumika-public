// Package fileatomic 共享桌面设置和根目录标记的改名边界，避免 Windows 短时占用使保存偶发失败。
package fileatomic

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Replace 沿用桌面设置已有的处理：只等 Windows 的短时共享冲突，失败由调用方保留旧文件、清临时文件。
func Replace(from, to string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for {
		err := os.Rename(from, to)
		if err == nil || !Transient(err) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Transient 报告这个错误是不是 Windows 上别的程序（杀毒、预览、另一个读者）暂时占着文件造成的：拒绝访问、共享冲突。
// 派生数据（缩略图缓存）遇到它可以当没缓存处理，不必让整次读取失败。
func Transient(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32)) // 拒绝访问、共享冲突
}
