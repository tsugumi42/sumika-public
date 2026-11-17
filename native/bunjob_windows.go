//go:build windows

package main

// 临时：Electrobun 接管进程后去掉（见 bunchild.go）。
// Bun 子进程挂进一个“关句柄就杀光”的作业对象：sumika.exe 正常退出、崩溃、被杀，系统都会关掉句柄，带走 Bun 和它起的子进程。
// 不按名字杀进程。

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	killJobOnce sync.Once
	killJob     windows.Handle
	killJobErr  error
)

func ensureKillJob() (windows.Handle, error) {
	killJobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			killJobErr = fmt.Errorf("CreateJobObject：%w", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
		}
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			_ = windows.CloseHandle(h)
			killJobErr = fmt.Errorf("SetInformationJobObject：%w", err)
			return
		}
		killJob = h // 进程生命周期内一直开着，退出时由系统关
	})
	return killJob, killJobErr
}

// assignToKillJob 把子进程挂进作业对象
func assignToKillJob(p *os.Process) error {
	job, err := ensureKillJob()
	if err != nil {
		return err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return fmt.Errorf("OpenProcess：%w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		return fmt.Errorf("AssignProcessToJobObject：%w", err)
	}
	return nil
}

// hideChildWindow 不给子进程开控制台窗口
func hideChildWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
