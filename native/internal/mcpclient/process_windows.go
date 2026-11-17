package mcpclient

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ChildrenDieWithHost：宿主进程退出（含被杀、崩溃）时，它起的 MCP 子进程树一定跟着没了。
// 这里靠 Job 的 KILL_ON_JOB_CLOSE：宿主的最后一个句柄随进程关闭，系统结束整个 Job。
// 启动收尾据此把上一个进程留下的本机服务调用记成“已终止”（store.MCPRecoverAfterRestart）。
const ChildrenDieWithHost = true

// 挂起创建后才进 Job，禁止子进程在托管前派生；不接受带包身份的可逃逸启动器。
type processGroup struct{ job windows.Handle }

func startProcess(cmd *exec.Cmd) (*processGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	p := &processGroup{job: job}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		p.close()
		return nil, err
	}
	attr := cmd.SysProcAttr // launchCommand 可能已经放了 CmdLine（.cmd 垫片），不能盖掉
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.HideWindow = true
	attr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW
	cmd.SysProcAttr = attr
	if err = cmd.Start(); err != nil {
		p.close()
		return nil, err
	}
	fail := func(e error) (*processGroup, error) { cmd.Process.Kill(); cmd.Wait(); p.close(); return nil, e }
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(proc)
	var length uint32
	status, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetPackageFullName").Call(uintptr(proc), uintptr(unsafe.Pointer(&length)), 0)
	if status != uintptr(windows.APPMODEL_ERROR_NO_PACKAGE) {
		return fail(fmt.Errorf("MCP 不允许带包身份启动器"))
	}
	if err = windows.AssignProcessToJobObject(job, proc); err != nil {
		return fail(err)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(cmd.Process.Pid) {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fail(err)
		}
		_, err = windows.ResumeThread(thread)
		closeErr := windows.CloseHandle(thread)
		if err = errors.Join(err, closeErr); err != nil {
			return fail(err)
		}
		return p, nil
	}
	return fail(fmt.Errorf("MCP 无法确认主线程"))
}
func (p *processGroup) kill() error  { return windows.TerminateJobObject(p.job, 1) }
func (p *processGroup) close() error { return windows.CloseHandle(p.job) }

type jobAccounting struct {
	User, Kernel, PeriodUser, PeriodKernel    int64
	PageFaults, Processes, Active, Terminated uint32
}

func (p *processGroup) waitExit(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var accounting jobAccounting
		if err := windows.QueryInformationJobObject(p.job, 1, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return err
		}
		if accounting.Active == 0 {
			return nil
		}
		select {
		case <-tick.C:
		case <-timer.C:
			return fmt.Errorf("MCP 进程树仍未全部退出")
		}
	}
}
