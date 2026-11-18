package bench

// Job Object：量得到子孙进程的 CPU 和内存、壳退出后留在后台的子孙被一起清掉、退出码原样带回。
// 子孙进程用测试程序自己扮（BENCH_JOB 指定扮哪种）

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestJobHelper 平时什么都不做；带 BENCH_JOB 起来时扮一种子进程
func TestJobHelper(t *testing.T) {
	switch os.Getenv("BENCH_JOB") {
	case "":
		return
	case "tree": // 起一个吃 CPU、占内存的子进程，等它完
		c := helper("burn")
		if err := c.Run(); err != nil {
			os.Exit(2)
		}
	case "burn": // 占 256 MB（每页都写到），一边占着一边空转 600 毫秒
		b := make([]byte, 256<<20)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		// 按自己的 CPU 时间转满 600 毫秒，不按墙上时间：机器忙时按墙上时间转，分到的 CPU 不够，
		// 断言的 400 毫秒会偶发不够（2026-10-28 负载 33% 时 5 次红 1 次，375 毫秒）
		for ownCPU() < 600*time.Millisecond {
		}
		runtime.KeepAlive(b)
	case "orphan": // 在后台起一个睡很久的子进程，记下它的编号，自己先走
		c := helper("sleep")
		if err := c.Start(); err != nil {
			os.Exit(2)
		}
		os.WriteFile(os.Getenv("BENCH_JOB_PID"), []byte(strconv.Itoa(c.Process.Pid)), 0o644)
	case "sleep":
		time.Sleep(time.Minute)
	case "pwshloop": // 经商店版 pwsh 空转一会儿（它会跑出 Job）
		if exec.Command("pwsh.exe", "-NoProfile", "-Command", `$x=0; foreach($i in 1..6000000){$x++}; $x`).Run() != nil {
			os.Exit(2)
		}
	case "pwshorphan": // 在后台起一个睡很久的商店版 pwsh，记下编号，自己先走
		c := exec.Command("pwsh.exe", "-NoProfile", "-Command", "Start-Sleep 60")
		if err := c.Start(); err != nil {
			os.Exit(2)
		}
		os.WriteFile(os.Getenv("BENCH_JOB_PID"), []byte(strconv.Itoa(c.Process.Pid)), 0o644)
	case "fail":
		os.Exit(3)
	}
	os.Exit(0)
}

// ownCPU 是本进程到现在用掉的 CPU 时间（用户态 + 内核态）
func ownCPU() time.Duration {
	var c, e, k, u syscall.Filetime
	h, _ := syscall.GetCurrentProcess()
	if syscall.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0
	}
	// Filetime 的 Nanoseconds() 是按"1601 年起的时间点"换算的，用在时长上会成负数；这里自己拼，单位 100 纳秒
	ticks := func(f syscall.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return time.Duration(ticks(k)+ticks(u)) * 100
}

func helper(kind string) *exec.Cmd {
	c := exec.Command(os.Args[0], "-test.run=^TestJobHelper$")
	c.Env = append(os.Environ(), "BENCH_JOB="+kind)
	return c
}

func TestJobCountsDescendants(t *testing.T) {
	u, err := RunInJob(helper("tree"), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", u)
	const mb = 1 << 20
	if u.Processes != 2 || u.Escaped != 0 {
		t.Errorf("子进程也要算进来、在 Job 里的不能再当成跑出去的重复算：进程 %d 个，跑出去的 %d 个", u.Processes, u.Escaped)
	}
	if u.CPUMs < 400 {
		t.Errorf("子进程空转 600 毫秒，CPU 只记了 %d 毫秒", u.CPUMs)
	}
	if u.PeakWorkingSet < 200*mb || u.PeakPrivate < 200*mb || u.PeakJobCommit < 200*mb {
		t.Errorf("子进程占了 256 MB：工作集 %d MB、私有 %d MB、Job 提交 %d MB", u.PeakWorkingSet/mb, u.PeakPrivate/mb, u.PeakJobCommit/mb)
	}
}

func TestJobKillsLeftovers(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	c := helper("orphan")
	c.Env = append(c.Env, "BENCH_JOB_PID="+pidFile)
	if _, err := RunInJob(c, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // 已经没了
	}
	defer windows.CloseHandle(h)
	if ev, _ := windows.WaitForSingleObject(h, 5000); ev != windows.WAIT_OBJECT_0 {
		windows.TerminateProcess(h, 1)
		t.Fatal("壳退出以后，留在后台的子孙要跟着 Job 一起清掉")
	}
}

// storePwsh：PATH 里的 pwsh 是商店版（WindowsApps 下）才测跑出 Job 的那几项
func storePwsh(t *testing.T) {
	p, err := exec.LookPath("pwsh.exe")
	if err != nil || !strings.Contains(strings.ToLower(p), `\windowsapps\`) {
		t.Skip("PATH 里的 pwsh 不是商店版：", p, err)
	}
}

func TestJobCountsEscapedPwsh(t *testing.T) {
	if testing.Short() {
		t.Skip("跑真实 pwsh 的对照工具，放在每日整套")
	}
	storePwsh(t)
	u, err := RunInJob(helper("pwshloop"), 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", u)
	// pwsh 自己的私有内存四五十 MB，不在 Job 的提交峰值里：加上它，私有峰值要明显比 Job 的大
	if u.Escaped == 0 || u.CPUMs < 1000 || u.PeakPrivate < u.PeakJobCommit+20<<20 {
		t.Fatalf("商店版 pwsh 跑出 Job 也要量到（空转 600 万次，CPU 远不止 1 秒）：%+v", u)
	}
}

func TestJobKillsEscapedLeftovers(t *testing.T) {
	storePwsh(t)
	// 采样间隔调到一小时：只剩开头一次（pwsh 还没起）和等完补扫一次，壳起完 pwsh 马上退出的情况靠补扫找到
	defer func(d time.Duration) { scanEvery = d }(scanEvery)
	scanEvery = time.Hour
	pidFile := filepath.Join(t.TempDir(), "pid")
	c := helper("pwshorphan")
	c.Env = append(c.Env, "BENCH_JOB_PID="+pidFile)
	if _, err := RunInJob(c, time.Hour); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return // 已经没了
	}
	defer windows.CloseHandle(h)
	if ev, _ := windows.WaitForSingleObject(h, 5000); ev != windows.WAIT_OBJECT_0 {
		windows.TerminateProcess(h, 1)
		t.Fatal("壳退出以后，跑出 Job 的子孙也要清掉")
	}
}

func TestJobExitCode(t *testing.T) {
	_, err := RunInJob(helper("fail"), 50*time.Millisecond)
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("退出码要原样带回：%v", err)
	}
}
