package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/toolchain"
)

const shellJobHelperEnv = "SUMIKA_SHELL_JOB_HELPER"

// 辅助进程当后台，通过生产的 startShell 持有 Job；父测试直接杀掉它，
// 不能靠 defer、取消回调或测试二进制退出时的清理把缺口遮过去。
func TestShellJobHelper(t *testing.T) {
	if os.Getenv(shellJobHelperEnv) == "leaf" {
		time.Sleep(120 * time.Second)
		return
	}
	mode := os.Getenv(shellJobHelperEnv)
	if mode != "backend" && mode != "tree" {
		return
	}
	path := os.Getenv("SUMIKA_SHELL_JOB_PIDS")
	childMode := "tree"
	if mode == "tree" {
		childMode = "leaf"
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellJobHelper$", "-test.timeout=130s")
	cmd.Env = append(os.Environ(), shellJobHelperEnv+"="+childMode)
	if mode == "tree" {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Process.Kill()
		pids, _ := json.Marshal([]int{os.Getpid(), cmd.Process.Pid})
		if err := os.WriteFile(path, pids, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd.Wait()
		return
	}
	process, err := startShell(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer process.close()
	root, _ := json.Marshal([]int{cmd.Process.Pid})
	if err := os.WriteFile(path+".root", root, 0o600); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal(err)
	}
	t.Fatalf("后台的长命令意外结束：%v", cmd.Wait())
}

func shellJobArgs(t *testing.T, command string) jsjson.Value {
	t.Helper()
	args, err := jsjson.Parse(fmt.Sprintf(`{"command":%s}`, jsjson.Quote(command)))
	if err != nil {
		t.Fatal(err)
	}
	return args
}

// shellJobLimit 是取消、结束以后等命令交回、进程退出的上限。长命令要睡 120 秒，没收掉进程树的改法等到这里也等不到，照样红；
// 商店版要现起一个 taskkill，低优先级跑在忙机器上几秒很常见（2026-11-09 合并队列 5 秒红过）。
const shellJobLimit = time.Minute

// shellJobStartLimit 是等长命令起好的上限（包身份检查 + PowerShell 冷启动 + 子进程），比长命令的 120 秒短。
const shellJobStartLimit = 100 * time.Second

func shellJobCommand(t *testing.T, path string) string {
	t.Helper()
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return fmt.Sprintf("$ErrorActionPreference = 'Stop'; [IO.File]::WriteAllText(%s, ('[' + $PID + ']')); $env:%s = 'leaf'; $info = New-Object Diagnostics.ProcessStartInfo; $info.FileName = %s; $info.Arguments = '-test.run=^TestShellJobHelper$ -test.timeout=130s'; $info.UseShellExecute = $false; $child = [Diagnostics.Process]::Start($info); [IO.File]::WriteAllText(%s, ('[' + $PID + ',' + $child.Id + ']')); Start-Sleep -Seconds 120", quote(path), shellJobHelperEnv, quote(exe), quote(path))
}

// 持有按 PID 打开的进程句柄，避免 PID 被复用后误判、误杀。失败清理也只动这些进程。
func shellJobProcesses(t *testing.T, path string, done <-chan error) []windows.Handle {
	t.Helper()
	// 启动包括包身份检查和 PowerShell 冷启动；低优先级整包里简单 exit 已量到 13.95 秒。
	deadline := time.NewTimer(shellJobStartLimit)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var last []byte
	opened := make(map[uint32]windows.Handle)
	for {
		last, _ = os.ReadFile(path)
		if len(last) == 0 {
			last, _ = os.ReadFile(path + ".root")
		}
		var pids []uint32
		if json.Unmarshal(last, &pids) == nil && len(pids) > 0 {
			for _, pid := range pids {
				if pid == 0 || opened[pid] != 0 {
					continue
				}
				h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
				if err != nil {
					t.Fatalf("打开进程 %d：%v", pid, err)
				}
				opened[pid] = h
				t.Cleanup(func() {
					windows.TerminateProcess(h, 1)
					windows.WaitForSingleObject(h, 3000)
					windows.CloseHandle(h)
				})
				if state, err := windows.WaitForSingleObject(h, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
					t.Fatalf("长命令还没取消，进程 %d 已退出：state=%d err=%v", pid, state, err)
				}
			}
			if len(pids) == 2 && opened[pids[0]] != 0 && opened[pids[1]] != 0 {
				t.Logf("已启动长命令：命令 PID=%d，子进程 PID=%d", pids[0], pids[1])
				return []windows.Handle{opened[pids[0]], opened[pids[1]]}
			}
		}
		select {
		case err := <-done:
			t.Fatalf("PID 还没写好命令就退出了：%v，PID 文件 %q", err, last)
		case <-deadline.C:
			t.Fatalf("%v 内没拿到两个 PID，文件内容 %q", shellJobStartLimit, last)
		case <-tick.C:
		}
	}
}

func shellJobExited(t *testing.T, processes []windows.Handle) {
	t.Helper()
	for i, h := range processes {
		if state, err := windows.WaitForSingleObject(h, uint32(shellJobLimit.Milliseconds())); err != nil || state != windows.WAIT_OBJECT_0 {
			t.Errorf("第 %d 个进程 %v 内没被收掉：state=%d err=%v", i+1, shellJobLimit, state, err)
		}
	}
}

func shellJobWait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(shellJobLimit):
		t.Fatalf("%v 内命令没有交回结果", shellJobLimit)
		return nil
	}
}

// 完整 Job 语义用系统 5.1 验证，商店版另测主动取消的 taskkill 兜底。
func shellJobUseSystemPowerShell(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0")
	if !exists(filepath.Join(path, "powershell.exe")) {
		t.Fatal("系统 PowerShell 5.1 不存在")
	}
	setToolPath(t, path)
	t.Setenv("ProgramFiles", "")
	toolchain.Refresh(realToolEnv()) // 清掉 Program Files 后重查，才会落到 PATH 的系统 5.1
}

func TestShellJobCancel(t *testing.T) {
	shellJobUseSystemPowerShell(t)
	path := filepath.Join(t.TempDir(), "pids.json")
	cmd, process, err := startPowerShell(filepath.Dir(path), shellJobCommand(t, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { process.close(); cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	processes := shellJobProcesses(t, path, done)
	t.Logf("启动返回 PID=%d，shell=%s", cmd.Process.Pid, cmd.Path)
	for i, h := range processes {
		var in int32
		api := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
		if ok, _, err := api.Call(uintptr(h), uintptr(process.job), uintptr(unsafe.Pointer(&in))); ok == 0 {
			t.Fatal(err)
		}
		if in == 0 {
			t.Errorf("第 %d 个进程不在命令 Job 里", i+1)
		}
	}

	// 故意留住第二个 Job 句柄：只关后台的句柄不能让这个断言过，必须真的 TerminateJobObject。
	var retained windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), process.job, windows.CurrentProcess(), &retained, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(retained) })
	otherCmd, other, err := startPowerShell(filepath.Dir(path), "Start-Sleep -Seconds 120", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.close(); otherCmd.Process.Kill(); otherCmd.Wait() })
	otherHandle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(otherCmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(otherHandle)
	if err := process.killTree(); err != nil {
		t.Fatal(err)
	}
	shellJobExited(t, processes)
	if state, err := windows.WaitForSingleObject(otherHandle, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Errorf("取消一个命令误伤了另一个作业：state=%d err=%v", state, err)
	}
	if process.job != 0 {
		t.Error("取消后 Job 句柄还没关闭")
	}
	if err := process.killTree(); err != nil {
		t.Errorf("再次取消：%v", err)
	}
	if err := shellJobWait(t, done); err == nil {
		t.Error("被取消的长命令却正常退出")
	}
}

func TestShellJobBackendKilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pids.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellJobHelper$", "-test.timeout=130s")
	cmd.Env = append(os.Environ(), shellJobHelperEnv+"=backend", "SUMIKA_SHELL_JOB_PIDS="+path)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	processes := shellJobProcesses(t, path, done)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := shellJobWait(t, done); err == nil {
		t.Fatalf("后台应该被强杀，实际正常退出：%s", output.String())
	}
	shellJobExited(t, processes)
}

func TestShellJobPowerShellCancel(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "timeout" && testing.Short() {
				t.Skip("要真等 30 秒的超时，合并前、推送前的 go test -short 跳过，每天巡检的整套照跑（ADR-225）")
			}
			path := filepath.Join(t.TempDir(), "pids.json")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			args := shellJobArgs(t, shellJobCommand(t, path))
			want, limit := "Command aborted", shellJobLimit
			if mode == "timeout" {
				// 给冷启动留出原有的 30 秒；到点后仍按 shellJobLimit 交回。
				args.Obj["timeout"] = jsjson.Value{Kind: jsjson.Number, Num: 30}
				want, limit = "Command timed out after 30 seconds", 30*time.Second+shellJobLimit
			}
			done := make(chan error, 1)
			settled := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				select {
				case <-settled:
				case <-time.After(shellJobLimit):
					t.Error("清理后 PowerShell 还没交回")
				}
			})
			deadline := time.NewTimer(limit)
			defer deadline.Stop()
			go func() {
				defer close(settled)
				_, err := powershell(ctx, filepath.Dir(path), args)
				done <- err
			}()
			processes := shellJobProcesses(t, path, done)
			if mode == "cancel" {
				cancel()
				deadline.Reset(shellJobLimit)
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("%s 后的报错不对：%v", mode, err)
				}
			case <-deadline.C:
				t.Fatalf("%s 没在 %s 内交回结果", mode, limit)
			}
			shellJobExited(t, processes)
		})
	}
}

var shellJobHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

func shellJobHandles(t *testing.T) uint32 {
	t.Helper()
	var count uint32
	if ok, _, err := shellJobHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count))); ok == 0 {
		t.Fatal(err)
	}
	return count
}

func TestShellJobNormalExit(t *testing.T) {
	shellJobUseSystemPowerShell(t)
	dir := t.TempDir()
	path, release := filepath.Join(dir, "pids.json"), filepath.Join(dir, "可以退出")
	command := strings.TrimSuffix(shellJobCommand(t, path), "Start-Sleep -Seconds 120")
	command += fmt.Sprintf("while (-not (Test-Path -LiteralPath '%s')) { Start-Sleep -Milliseconds 10 }; '正常退出'", strings.ReplaceAll(release, "'", "''"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	args := shellJobArgs(t, command)
	done := make(chan error, 1)
	settled := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-settled:
		case <-time.After(shellJobLimit):
			t.Error("清理后 PowerShell 还没交回")
		}
	})
	go func() {
		defer close(settled)
		text, err := powershell(ctx, dir, args)
		if err == nil && strings.TrimSpace(text) != "正常退出" {
			err = fmt.Errorf("正常命令结果不对：%q", text)
		}
		done <- err
	}()
	processes := shellJobProcesses(t, path, done)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := shellJobWait(t, done); err != nil {
		t.Fatalf("正常退出：%v", err)
	}
	// 这里没有取消或显式结束 Job，子进程也消失才能证明正常收尾关掉了最后一个 Job 句柄。
	shellJobExited(t, processes)
}

func TestShellJobStartFailureClosesHandles(t *testing.T) {
	dir := t.TempDir()
	run := func() {
		_, err := startShell(exec.Command(filepath.Join(dir, "不存在.exe")))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("不存在的命令应该报文件不存在：%v", err)
		}
	}
	run()
	// 句柄数是整个测试进程的：运行时新起线程、前面测试的收尾会让它涨落几个（CI 上 8 次涨过 3 个，279 → 282）。
	// 真泄漏是每次失败至少漏一个 Job 句柄，所以多跑几次、门槛按每次漏一个的一半算，噪声进不来、漏了照样拦下。
	const n = 64
	before := shellJobHandles(t)
	for range n {
		run()
	}
	after := shellJobHandles(t)
	t.Logf("连续 %d 次启动失败：句柄 %d → %d", n, before, after)
	if after > before+n/2 {
		t.Errorf("连续 %d 次启动失败漏了句柄：%d → %d", n, before, after)
	}
}

// 商店版仍按原顺序选中，取消时用 taskkill 收掉逃出 Job 的子进程。
func TestShellJobPackagedPowerShellCancel(t *testing.T) {
	// 不依赖 PATH 当前选中了哪个版本，显式把已安装的商店别名放到最前面。
	shell := filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WindowsApps", "pwsh.exe")
	if !exists(shell) {
		t.Skipf("没有安装商店版 PowerShell：%s", shell)
	}
	setToolPath(t, filepath.Dir(shell)+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(t.TempDir(), "pids.json")
	cmd, process, err := startPowerShell(filepath.Dir(path), shellJobCommand(t, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { process.close(); cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	processes := shellJobProcesses(t, path, done)
	if !strings.EqualFold(cmd.Path, shell) {
		t.Fatalf("应该继续用 PATH 的商店版 %s，实际选了 %s", shell, cmd.Path)
	}
	var length uint32
	status, _, _ := shellPackageName.Call(uintptr(processes[0]), uintptr(unsafe.Pointer(&length)), 0)
	if status != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
		t.Fatalf("实际运行的 PowerShell 没有包身份：status=%d", status)
	}
	t.Logf("实际使用商店版：%s", cmd.Path)
	if err := process.killTree(); err != nil {
		t.Fatal(err)
	}
	shellJobExited(t, processes)
	if err := shellJobWait(t, done); err == nil {
		t.Error("被取消的商店版命令却正常退出")
	}
}
