//go:build windows

package rpcwire

import "golang.org/x/sys/windows"

// ProcessAlive 说 run 文件里记的后台进程还在不在。
//
// 不能只看“能不能打开进程句柄”（os.FindProcess）：Windows 上进程退出以后，只要还有谁握着它的句柄
// （父进程、杀毒软件、别的程序），进程对象就还在，OpenProcess 照样成功（CI 的 Windows 机器上就这样，2026-11-19）。
// 也不用退出码判（GetExitCodeProcess == STILL_ACTIVE）：进程自己恰好以 259 退出时会被当成还活着。
// 所以以 SYNCHRONIZE 打开，等进程对象 0 毫秒：还没触发（WAIT_TIMEOUT）才是活着，已触发（WAIT_OBJECT_0）是退出了。
//
// 拿不准一律算活着：打开被拒绝（别的用户、受保护的进程）说明进程在；等待本身出错也说明不了它退出。
// 误判成“不在”会让第二个后台去抢同一个数据目录，误判成“在”只是少启动一次（主人 2026-11-19 定：宁可不重启）。
// 只有“没有这个进程号”（ERROR_INVALID_PARAMETER）才返回不在。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return false
		}
		return true
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return true
	}
	return ev == uint32(windows.WAIT_TIMEOUT)
}
