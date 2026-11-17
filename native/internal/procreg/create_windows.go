package procreg

import "golang.org/x/sys/windows"

// CreationTime 取进程的创建时间（Unix 纪元起的纳秒数）；进程不存在或没权限时是 0。
// 只读：用最小的查询权限打开，不改动也不结束任何进程
func CreationTime(pid int) int64 {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil {
		return 0
	}
	return creation.Nanoseconds() // 换成 Unix 纪元起的纳秒：同一个进程每次算出来一样，只用来核对，不显示
}
