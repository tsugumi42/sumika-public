//go:build windows

package memview

import "golang.org/x/sys/windows"

// cpuSeconds 是这个进程到现在用掉的 CPU 秒数（内核态 + 用户态）。GetProcessTimes 的精度约 15 毫秒，量几十秒的安静轮询够用
func cpuSeconds() float64 {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return 0
	}
	ticks := func(f windows.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) } // 100 纳秒一格
	return float64(ticks(kernel)+ticks(user)) * 100 / 1e9
}
