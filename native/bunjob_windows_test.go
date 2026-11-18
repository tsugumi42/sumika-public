//go:build windows

package main

import (
	"sync"

	"golang.org/x/sys/windows"
)

// closeKillJobForTest：关掉作业对象的句柄，等于“sumika.exe 没了”，系统会杀光挂在里面的进程。关完下一次再用会重建
func closeKillJobForTest() {
	if killJob != 0 {
		_ = windows.CloseHandle(killJob)
	}
	killJob, killJobErr, killJobOnce = 0, nil, sync.Once{}
}
