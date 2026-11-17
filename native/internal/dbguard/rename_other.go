//go:build !windows

package dbguard

// ephemeralRenameErr 在非 Windows 上一律为假：同样的 errno 数字在 Unix 上含义不同（5 是
// EIO），本地文件系统改名失败都是真错误，等一等不会好；robustio 也只在 Windows 上重试。
func ephemeralRenameErr(error) bool { return false }
