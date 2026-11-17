package bench

// 量一个壳和它全部子孙进程的 CPU、内存（规格-表二执行器 第四节；做法见调研-表二评测出题"Windows 上量进程树的 CPU 和内存"）：
// 挂起起来 → 放进一个新的 Job（子孙默认跟着进；不设 BREAKAWAY_OK）→ 用线程快照找到主线程恢复。CPU 取 Job 的累计（含已退出的进程），
// 内存每隔一会儿枚举 Job 里的进程、把工作集和私有字节加起来取峰值；另记 Job 自己的提交内存峰值（口径不同，并列记）。
// Job 设了"最后一个句柄关掉就杀掉里面的进程"：壳结束时没收尾的子孙（后台起的命令）一起清掉。
//
// 跑出 Job 的子孙：商店版 pwsh（WindowsApps 下的打包应用）不管经别名还是真实路径起，系统都把它放进自己的 Job，
// 我们的 Job 量不到它和它起的 go test（2026-10-27 实测：Go 壳整轮只有自己 1 个进程在 Job 里）。父进程号是保留的，
// 所以每 50 毫秒拍一张进程快照，父进程一路追得到这棵树、又不在 Job 里的，开句柄记下（退出以后还读得到 CPU），
// 内存采样时一起加上，结束时把还活着的杀掉。比 50 毫秒还短的进程可能漏掉

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobAccounting 是 JOBOBJECT_BASIC_ACCOUNTING_INFORMATION（x/sys 没有，照微软文档声明；时间单位 100 纳秒）
type jobAccounting struct {
	TotalUserTime, TotalKernelTime                                                 int64
	ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime                             int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses uint32
}

// pidList 是 JOBOBJECT_BASIC_PROCESS_ID_LIST（一次最多取这么多个）
type pidList struct {
	Assigned, InList uint32
	IDs              [512]uintptr
}

// memCountersEx 是 PROCESS_MEMORY_COUNTERS_EX
type memCountersEx struct {
	cb                                                 uint32
	PageFaultCount                                     uint32
	PeakWorkingSetSize, WorkingSetSize                 uintptr
	QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage       uintptr
	QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage uintptr
	PagefileUsage, PeakPagefileUsage, PrivateUsage     uintptr
}

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
	procIsProcessInJob       = kernel32.NewProc("IsProcessInJob") // x/sys/windows 没有
)

// inJob：进程在不在这个 Job 里；问不出来当成在（不追它，免得重复算）
func inJob(h, job windows.Handle) bool {
	var in int32
	if r, _, _ := procIsProcessInJob.Call(uintptr(h), uintptr(job), uintptr(unsafe.Pointer(&in))); r == 0 {
		return true
	}
	return in != 0
}

// Job 是起在 Job Object 里、还没等完的一个进程
type Job struct {
	cmd     *exec.Cmd
	job     windows.Handle
	root    uint32
	rootH   windows.Handle            // 自己开着根进程的句柄：它退出以后进程号也不会被重用，等完再补扫一次还认得出它的孩子
	since   time.Time                 // 比这早创建的不是我们的子孙（进程号会被重用）
	tree    map[uint32]bool           // 进过 Job 的进程号（退出了的也记着：它起的跑出 Job 的孩子还要靠它认）
	escaped map[uint32]windows.Handle // 跑出 Job 的子孙；只有采样协程和它停了以后的 Wait 碰
	u       Usage                     // 采样协程写峰值；Wait 等它停了再读
	stop    chan struct{}
	done    chan struct{}
}

// scanEvery 是找跑出 Job 的子孙的间隔：go test 起的编译、链接进程一个就几百毫秒，采样太稀会漏（测试里调长，只看等完那次补扫）
var scanEvery = 50 * time.Millisecond

// StartInJob 起 cmd（挂起、放进新 Job、恢复），每 interval 采样一次内存。要读 cmd 的 StdoutPipe 时先读完再 Wait（exec 的规矩）
func StartInJob(cmd *exec.Cmd, interval time.Duration) (*Job, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("建 Job：%w", err)
	}
	var limit windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limit.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limit)), uint32(unsafe.Sizeof(limit))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("设 Job：%w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	since := time.Now().Add(-time.Second)
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	if err := assignAndResume(job, uint32(cmd.Process.Pid)); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	root := uint32(cmd.Process.Pid)
	rootH, _ := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, root)
	j := &Job{cmd: cmd, job: job, root: root, rootH: rootH, since: since, tree: map[uint32]bool{root: true}, escaped: map[uint32]windows.Handle{},
		stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(j.done)
		t := time.NewTicker(min(interval, scanEvery))
		defer t.Stop()
		for {
			j.findEscaped()
			ws, priv := jobMemory(job)
			ews, epriv := j.escapedMemory()
			j.u.PeakWorkingSet = max(j.u.PeakWorkingSet, ws+ews)
			j.u.PeakPrivate = max(j.u.PeakPrivate, priv+epriv)
			select {
			case <-j.stop:
				return
			case <-t.C:
			}
		}
	}()
	return j, nil
}

// findEscaped 拍一张进程快照：父进程是这棵树里的（根、Job 里的、已经记下的跑出去的）、自己却不在 Job 里的，开句柄记下
func (j *Job) findEscaped() {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	parent := map[uint32]uint32{}
	pe := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		parent[pe.ProcessID] = pe.ParentProcessID
	}
	for _, pid := range jobPIDs(j.job) {
		j.tree[pid] = true
	}
	known := map[uint32]bool{}
	for pid := range j.tree {
		known[pid] = true
	}
	for pid := range j.escaped {
		known[pid] = true
	}
	for grew := true; grew; { // 一层一层往下找，直到没有新的
		grew = false
		for pid, pp := range parent {
			if known[pid] || !known[pp] {
				continue
			}
			known[pid], grew = true, true
			h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
			if err != nil {
				continue
			}
			var created, exited, kernel, user windows.Filetime
			if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil || time.Unix(0, created.Nanoseconds()).Before(j.since) || inJob(h, j.job) {
				windows.CloseHandle(h)
				continue
			}
			j.escaped[pid] = h
		}
	}
}

// escapedMemory 是跑出 Job、还活着的子孙这会儿的工作集、私有字节之和
func (j *Job) escapedMemory() (ws, priv uint64) {
	for _, h := range j.escaped {
		if ev, _ := windows.WaitForSingleObject(h, 0); ev != uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		w, p := processMemory(h)
		ws, priv = ws+w, priv+p
	}
	return ws, priv
}

// ticks 是 FILETIME 里的 100 纳秒数（CPU 时间用）
func ticks(f windows.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }

// Wait 等进程结束，返回量到的；关掉 Job 时留在后台的子孙一起清掉。cmd 自己的退出错误原样返回
func (j *Job) Wait() (Usage, error) {
	waitErr := j.cmd.Wait()
	close(j.stop)
	<-j.done
	j.findEscaped() // 根进程刚起完孩子就退出、采样没赶上的：再补扫一次（根的进程号还被 rootH 占着）
	u := j.u
	var acc jobAccounting
	if err := windows.QueryInformationJobObject(j.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&acc)), uint32(unsafe.Sizeof(acc)), nil); err == nil {
		u.CPUMs = (acc.TotalUserTime + acc.TotalKernelTime) / 10_000
		u.Processes = acc.TotalProcesses
	}
	var ext windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(j.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&ext)), uint32(unsafe.Sizeof(ext)), nil); err == nil {
		u.PeakJobCommit = uint64(ext.PeakJobMemoryUsed)
	}
	// 跑出 Job 的子孙：CPU 加进来；还活着的照 Job 关掉时一样杀掉
	for _, h := range j.escaped {
		if ev, _ := windows.WaitForSingleObject(h, 0); ev == uint32(windows.WAIT_TIMEOUT) {
			windows.TerminateProcess(h, 1)
			windows.WaitForSingleObject(h, 5000)
		}
		var created, exited, kernel, user windows.Filetime
		if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil {
			u.CPUMs += (ticks(kernel) + ticks(user)) / 10_000
		}
		windows.CloseHandle(h)
	}
	u.Escaped = uint32(len(j.escaped))
	u.Processes += u.Escaped
	if j.rootH != 0 {
		windows.CloseHandle(j.rootH)
	}
	windows.CloseHandle(j.job)
	return u, waitErr
}

// RunInJob 是 StartInJob + Wait
func RunInJob(cmd *exec.Cmd, interval time.Duration) (Usage, error) {
	j, err := StartInJob(cmd, interval)
	if err != nil {
		return Usage{}, err
	}
	return j.Wait()
}

// assignAndResume 把挂起的进程放进 Job，再恢复它的线程（挂起时只有主线程；Go 的 exec 拿不到主线程句柄，用线程快照找）
func assignAndResume(job windows.Handle, pid uint32) error {
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("打开进程：%w", err)
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		return fmt.Errorf("放进 Job：%w", err)
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("线程快照：%w", err)
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err := windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return fmt.Errorf("打开线程：%w", err)
		}
		_, err = windows.ResumeThread(th)
		windows.CloseHandle(th)
		if err != nil {
			return fmt.Errorf("恢复线程：%w", err)
		}
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("没找到进程 %d 的线程", pid)
	}
	return nil
}

// jobPIDs 是 Job 里这会儿的进程号
func jobPIDs(job windows.Handle) []uint32 {
	var pl pidList
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&pl)), uint32(unsafe.Sizeof(pl)), nil); err != nil && err != windows.ERROR_MORE_DATA {
		return nil
	}
	n := min(int(pl.InList), len(pl.IDs))
	out := make([]uint32, n)
	for i, id := range pl.IDs[:n] {
		out[i] = uint32(id)
	}
	return out
}

// jobMemory 是 Job 里这会儿各进程的工作集、私有字节之和（刚退出、打不开的跳过）
func jobMemory(job windows.Handle) (ws, priv uint64) {
	for _, pid := range jobPIDs(job) {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			continue
		}
		w, p := processMemory(h)
		ws, priv = ws+w, priv+p
		windows.CloseHandle(h)
	}
	return ws, priv
}

// processMemory 是一个进程这会儿的工作集、私有字节
func processMemory(h windows.Handle) (ws, priv uint64) {
	var mc memCountersEx
	mc.cb = uint32(unsafe.Sizeof(mc))
	if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb)); r != 0 {
		return uint64(mc.WorkingSetSize), uint64(mc.PrivateUsage)
	}
	return 0, 0
}
