//go:build !windows

package dbguard

import "syscall"

// systemFreeSpace 是 dir 所在盘当前用户还能用的剩余空间。
func systemFreeSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
