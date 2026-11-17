//go:build windows

package dbguard

import "golang.org/x/sys/windows"

// systemFreeSpace 是 dir 所在盘当前用户还能用的剩余空间。
func systemFreeSpace(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return avail, nil
}
