//go:build windows

package dbguard

import "golang.org/x/sys/windows"

// ephemeralRenameErr 报告这个错误是不是 Windows 上替换文件时能等一等再试的，照 Go 源码
// cmd/internal/robustio/robustio_windows.go 判的三种：杀毒、索引程序正开着文件时改名被拒
// （拒绝访问、共享冲突），临时文件一时找不到也算。
func ephemeralRenameErr(err error) bool {
	errno, ok := errnoOf(err)
	if !ok {
		return false
	}
	switch errno {
	case windows.ERROR_ACCESS_DENIED, windows.ERROR_FILE_NOT_FOUND, windows.ERROR_SHARING_VIOLATION:
		return true
	}
	return false
}
