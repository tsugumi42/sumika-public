package selfbuild

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 重编锁（lock.go，系统文件锁）的测试：全部用真的多进程。
// 测试二进制自己当子进程：TestMain 里看环境变量 SELFBUILD_LOCKHELPER，是子进程就干活、不跑测试。
//   count：取锁 N 次，持锁期间在共享计数文件里做“进入 / 退出”，进入时发现已经有人在里面就往违规文件记一笔；
//   hold ：取锁、打印 locked、一直睡着等着被杀。

const (
	envHelper     = "SELFBUILD_LOCKHELPER"
	envHelperLock = "SELFBUILD_LOCKFILE"
	envHelperCnt  = "SELFBUILD_COUNTFILE"
	envHelperBad  = "SELFBUILD_BADFILE"
	envHelperN    = "SELFBUILD_N"
)

func TestMain(m *testing.M) {
	switch os.Getenv(envHelper) {
	case "":
		os.Exit(m.Run())
	case "count":
		os.Exit(helperCount())
	case "hold":
		os.Exit(helperHold())
	default:
		fmt.Fprintln(os.Stderr, "不认识的 helper 模式")
		os.Exit(2)
	}
}

func helperCount() int {
	lock, cnt, bad := os.Getenv(envHelperLock), os.Getenv(envHelperCnt), os.Getenv(envHelperBad)
	n, _ := strconv.Atoi(os.Getenv(envHelperN))
	for i := 0; i < n; i++ {
		rel, err := acquireLock(lock, 60*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		// 进入：计数文件必须是 0（没人在里面），改成 1；睡一小会儿（给别的进程撞进来的机会）；退出前必须还是 1，改回 0
		if b, _ := os.ReadFile(cnt); strings.TrimSpace(string(b)) != "0" {
			appendLine(bad, fmt.Sprintf("pid %d 第 %d 次进入时计数是 %q", os.Getpid(), i, b))
		}
		os.WriteFile(cnt, []byte("1"), 0o644)
		time.Sleep(time.Millisecond)
		if b, _ := os.ReadFile(cnt); strings.TrimSpace(string(b)) != "1" {
			appendLine(bad, fmt.Sprintf("pid %d 第 %d 次退出时计数是 %q", os.Getpid(), i, b))
		}
		os.WriteFile(cnt, []byte("0"), 0o644)
		rel()
	}
	return 0
}

func appendLine(path, s string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(f, s)
		f.Close()
	}
}

func helperHold() int {
	rel, err := acquireLock(os.Getenv(envHelperLock), 60*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	_ = rel
	fmt.Println("locked")
	time.Sleep(10 * time.Minute)
	return 0
}

func startLockHelper(t *testing.T, mode string, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), append([]string{envHelper + "=" + mode}, env...)...)
	return cmd
}

// 4 个进程各取锁 40 次，持锁期间在共享文件里做进入 / 退出计数：任何时刻最多一个进程在里面。
func TestMultiProcessMutualExclusion(t *testing.T) {
	dir := t.TempDir()
	lock, cnt, bad := filepath.Join(dir, "x.lock"), filepath.Join(dir, "count"), filepath.Join(dir, "bad")
	os.WriteFile(cnt, []byte("0"), 0o644)
	const procs, rounds = 4, 40
	var cmds []*exec.Cmd
	var outs []*bytes.Buffer
	for i := 0; i < procs; i++ {
		c := startLockHelper(t, "count", envHelperLock+"="+lock, envHelperCnt+"="+cnt, envHelperBad+"="+bad, envHelperN+"="+strconv.Itoa(rounds))
		var buf bytes.Buffer
		c.Stdout, c.Stderr = &buf, &buf
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds, outs = append(cmds, c), append(outs, &buf)
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Errorf("子进程 %d 失败：%v\n%s", i, err, outs[i])
		}
	}
	if b, _ := os.ReadFile(bad); len(b) > 0 {
		t.Fatalf("同时有两个进程持锁：\n%s", b)
	}
}

// 持锁的子进程被杀：系统自动放锁，下一个进程马上拿到，不等期限，也不用清什么。
func TestKilledHolderReleasesLockImmediately(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	c := startLockHelper(t, "hold", envHelperLock+"="+lock)
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("子进程没取到锁：%q %v", line, err)
	}
	// 子进程占着：拿不到
	if _, err := acquireLock(lock, 200*time.Millisecond); err == nil {
		t.Fatal("子进程占着锁，不该拿到")
	}
	c.Process.Kill()
	c.Wait()
	start := time.Now()
	rel, err := acquireLock(lock, 30*time.Second)
	if err != nil {
		t.Fatalf("持锁进程被杀以后应马上拿到锁：%v", err)
	}
	rel()
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("被杀后拿锁花了 %v，应当马上", el)
	}
}

// 期限到了拿不到：返回中文原因（带占着的 pid）；经 Ensure 走一遍，退出码非零、不放行。
func TestDeadlineReturnsChineseReasonAndNonzeroExit(t *testing.T) {
	root := mkModule(t, false)
	exe := filepath.Join(t.TempDir(), "sumika-dev.exe")
	os.WriteFile(exe, []byte("旧程序"), 0o755)
	c := startLockHelper(t, "hold", envHelperLock+"="+exe+".lock")
	out, _ := c.StdoutPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	if line, _ := bufio.NewReader(out).ReadString('\n'); strings.TrimSpace(line) != "locked" {
		t.Fatalf("子进程没取到锁：%q", line)
	}
	_, err := acquireLock(exe+".lock", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "重编") || !strings.Contains(err.Error(), strconv.Itoa(c.Process.Pid)) {
		t.Fatalf("应返回带中文原因和占锁 pid 的错误：%v", err)
	}
	var stderr bytes.Buffer
	built := false
	rebuilt, code, err := Ensure(root, Env{
		Exe: exe, Args: []string{"x"}, Stderr: &stderr, Built: "旧摘要", LockWait: 300 * time.Millisecond,
		Build: func(root, out, digest string) error { built = true; return os.WriteFile(out, []byte("新"), 0o755) },
		Exec:  func(string, []string, string) (int, error) { t.Error("拿不到锁不该执行"); return 0, nil },
	})
	if !rebuilt || code == 0 || err == nil || !strings.Contains(err.Error(), "重编") || built {
		t.Fatalf("拿不到锁应非零退出、不编、不放行：rebuilt=%v code=%d err=%v built=%v", rebuilt, code, err, built)
	}
}

// 放锁以后锁文件还在（常驻、不删），里面的 pid 只是诊断。
func TestLockFileStaysAndHoldsDiagnosticPid(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	rel, err := acquireLock(lock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(lock); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("锁里应写着自己的 pid：%q", b)
	}
	rel()
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("放锁后锁文件应还在：%v", err)
	}
	// 锁文件里是别的内容（旧版留下的 pid、垃圾）也不影响取锁
	os.WriteFile(lock, []byte("999999\n乱七八糟"), 0o644)
	rel2, err := acquireLock(lock, time.Second)
	if err != nil {
		t.Fatalf("锁文件里有旧内容不该影响取锁：%v", err)
	}
	rel2()
}
