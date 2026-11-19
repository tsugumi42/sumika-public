package backup

import "golang.org/x/sys/windows"

// freeSpace 是 path 所在盘当前用户还能用的剩余空间
func freeSpace(path string) (int64, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, false
	}
	return int64(avail), true
}
