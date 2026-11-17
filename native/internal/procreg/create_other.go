//go:build !windows

package procreg

// CreationTime 在非 Windows 上没有实现：返回 0，表示"没取到"，登记簿照常工作，只是没有 pid 复用的核对
func CreationTime(pid int) int64 { return 0 }
