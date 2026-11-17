package tools

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"sumika-native/internal/toolchain"
)

// hideWindow：不弹黑框（spawn 的 windowsHide）
func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

// shellProcess 的 Job 句柄只留在后台、不让子进程继承。一个命令一个 Job，
// 后台被杀时最后一个句柄随之关闭，系统收掉作业里的所有进程。
// 商店版 PowerShell 的子进程会脱离 Job：取消和超时另用 taskkill 兜底；
// 后台被强杀时这些子进程仍会继续跑。装非商店版 pwsh 7 能避开，但 scoop、GitHub 发布的版本只带英文资源，
// 中文系统上报错会变成英文（2026-10-28 试过，又卸了）。
// 锁让取消和正常收尾不能同时使用、关闭同一个句柄。
type shellProcess struct {
	mu      sync.Mutex
	job     windows.Handle
	treePID int // 只有带包身份的命令需要 taskkill 兜底。
}

// startShell 照 Codex utils/pty/src/win/job.rs 的 spawn_contained：
// CREATE_SUSPENDED → AssignProcessToJobObject → 恢复，避免入 Job 前已经生出孙进程。
// Go 的 exec.Cmd 不给主线程句柄，用线程快照找挂起的主线程（同 bench 的 assignAndResume）。
// 商店版的取消兜底照 OpenCode packages/util/src/cross-spawn-spawner.ts 的 killGroup（taskkill /T /F）。
func startShell(cmd *exec.Cmd) (*shellProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("建命令 Job：%w", err)
	}
	p := &shellProcess{job: job}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, errors.Join(fmt.Errorf("设命令 Job：%w", err), p.close())
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(err, p.close())
	}
	if err := p.assign(uint32(cmd.Process.Pid)); err != nil {
		// 任一步失败都先杀掉、等回收；没加入 Job 的命令绝不恢复。
		killErr := cmd.Process.Kill()
		cmd.Wait()
		return nil, errors.Join(err, killErr, p.close())
	}
	return p, nil
}

// GetPackageFullName 对普通 Win32 进程回 APPMODEL_ERROR_NO_PACKAGE；对带包身份的进程回需要的缓冲长度。
var shellPackageName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetPackageFullName")

func (p *shellProcess) assign(pid uint32) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("打开命令进程：%w", err)
	}
	defer windows.CloseHandle(process)
	var length uint32
	status, _, _ := shellPackageName.Call(uintptr(process), uintptr(unsafe.Pointer(&length)), 0)
	if status == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || status == 0 {
		p.treePID = int(pid)
	} else if status != uintptr(windows.APPMODEL_ERROR_NO_PACKAGE) {
		return fmt.Errorf("检查命令包身份：%w", syscall.Errno(status))
	}
	if err := windows.AssignProcessToJobObject(p.job, process); err != nil {
		return fmt.Errorf("命令放进 Job：%w", err)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("命令线程快照：%w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("打开命令主线程：%w", err)
		}
		_, resumeErr := windows.ResumeThread(thread)
		closeErr := windows.CloseHandle(thread)
		if resumeErr != nil {
			return errors.Join(fmt.Errorf("恢复命令主线程：%w", resumeErr), closeErr)
		}
		return closeErr
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("读取命令线程：%w", err)
	}
	return fmt.Errorf("没找到命令进程 %d 的主线程", pid)
}

func (p *shellProcess) killTree() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == 0 {
		return nil
	}
	var treeErr error
	if p.treePID != 0 {
		// 先杀树、再结束 Job；先杀根进程会丢掉 taskkill 查子进程用的树关系。
		if r, ok := toolchain.Lookup("taskkill"); ok {
			cmd := exec.Command(r.Path, "/F", "/T", "/PID", strconv.Itoa(p.treePID))
			hideWindow(cmd)
			if err := cmd.Run(); err != nil {
				treeErr = fmt.Errorf("结束商店版命令进程树：%w", err)
			}
		} else {
			treeErr = errors.New("没找到 taskkill，结束商店版命令进程树失败")
		}
	}
	err := windows.TerminateJobObject(p.job, 1)
	return errors.Join(treeErr, err, p.closeLocked())
}

func (p *shellProcess) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeLocked()
}

func (p *shellProcess) closeLocked() error {
	if p.job == 0 {
		return nil
	}
	err := windows.CloseHandle(p.job)
	p.job = 0
	return err
}
