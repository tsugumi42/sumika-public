package sendlock

// 两个进程的那几条（规格-QQ发送上线 第十一节第 3 步"怎么证明"）：临时目录、两个进程。
// 一方持锁时另一方拿不到；进程被杀以后锁由系统放掉，接着拿回来的记录算"上次没正常交出"。
// 子进程就是同一个测试二进制（-test.run 只跑 TestSendlockHelper），数据目录用环境变量传。
// 号一律编的，目录只用 t.TempDir()。

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSendlockHelper 是下面几条拉起来的子进程。环境变量 SENDLOCK_HELPER=1 才进去：
// 抢到锁打印一行 holding（接着上一次拿的打印 resumed），然后等 stdin；收到 release 就交出、打印 released、退出。
// 抢不到打印 take: 那行，以 3 退出。
func TestSendlockHelper(t *testing.T) {
	if os.Getenv("SENDLOCK_HELPER") != "1" {
		t.Skip("只作为子进程跑（两个进程的那几条）")
	}
	dir, account := os.Getenv("SENDLOCK_HELPER_DIR"), os.Getenv("SENDLOCK_HELPER_ACCOUNT")
	h, err := Take(dir, account)
	if err != nil {
		fmt.Printf("take: %v\n", err)
		os.Exit(3)
	}
	if h.Resumed {
		fmt.Println("resumed")
	} else {
		fmt.Println("holding")
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "release" {
			break
		}
	}
	if err := h.Release(); err != nil {
		fmt.Printf("release: %v\n", err)
		os.Exit(4)
	}
	fmt.Println("released")
	os.Exit(0)
}

// holderProc 是一个拿着（或想拿）发送权的子进程
type holderProc struct {
	t       *testing.T
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string
	errPath string
	exited  bool
	exitErr error
}

// startHolder 拉起子进程。子进程的 stderr 写进数据目录里的一份文件（读它不会和子进程抢同一块内存）
func startHolder(t *testing.T, dir, account string) *holderProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSendlockHelper$")
	cmd.Env = append(os.Environ(), "SENDLOCK_HELPER=1", "SENDLOCK_HELPER_DIR="+dir, "SENDLOCK_HELPER_ACCOUNT="+account)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("子进程的 stdin：%v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("子进程的 stdout：%v", err)
	}
	errPath := filepath.Join(dir, "child-stderr.log")
	errFile, err := os.Create(errPath)
	if err != nil {
		t.Fatalf("子进程的 stderr 文件：%v", err)
	}
	cmd.Stderr = errFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("起子进程：%v", err)
	}
	p := &holderProc{t: t, cmd: cmd, stdin: stdin, lines: make(chan string, 16), errPath: errPath}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	t.Cleanup(func() {
		p.kill()
		errFile.Close()
	})
	return p
}

// expect 等子进程打印一行（去掉首尾空白）等于 want 的行。10 秒不来就算失败
func (p *holderProc) expect(want string) {
	p.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				p.t.Fatalf("子进程退出了，没打印 %q（stderr：%s）", want, p.stderr())
			}
			if strings.TrimSpace(line) == want {
				return
			}
		case <-deadline:
			p.t.Fatalf("等 10 秒子进程也没打印 %q（stderr：%s）", want, p.stderr())
		}
	}
}

// expectPrefix 等一行以 want 开头（抢不到锁那行带着 Windows 的错误文字，不好整行对）
func (p *holderProc) expectPrefix(want string) {
	p.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				p.t.Fatalf("子进程退出了，没打印 %q 开头的行（stderr：%s）", want, p.stderr())
			}
			if strings.HasPrefix(strings.TrimSpace(line), want) {
				return
			}
		case <-deadline:
			p.t.Fatalf("等 10 秒子进程也没打印 %q 开头的行（stderr：%s）", want, p.stderr())
		}
	}
}

// release 让子进程交出发送权，并等它正常退出
func (p *holderProc) release() {
	p.t.Helper()
	if _, err := io.WriteString(p.stdin, "release\n"); err != nil {
		p.t.Fatalf("让子进程交出：%v（stderr：%s）", err, p.stderr())
	}
	p.expect("released")
	if err := p.waitExit(); err != nil {
		p.t.Fatalf("交出去了，子进程该正常退出：%v（stderr：%s）", err, p.stderr())
	}
}

// waitExit 等子进程退出（先把 stdout 读完，不然 Wait 会把还没读的管道关掉）
func (p *holderProc) waitExit() error {
	if p.exited {
		return p.exitErr
	}
	for range p.lines {
	}
	p.exitErr = p.cmd.Wait()
	p.exited = true
	return p.exitErr
}

// kill 强杀子进程：不写 released，也不放锁（锁由系统在进程退出时放掉）
func (p *holderProc) kill() {
	if p.exited {
		return
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.waitExit()
}

func (p *holderProc) stderr() string {
	b, err := os.ReadFile(p.errPath)
	if err != nil {
		return "读不到"
	}
	return string(b)
}

// 一方持锁时另一方拿不到；持锁的那一方写好 released、关掉句柄以后才轮得到另一方。
// 持锁期间记录照样读得到（锁在文件内容之外）
func TestTwoProcessesOnlyOneHolds(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	writeRecordFile(t, dir, freshReleasedRecord(testAccount))

	child := startHolder(t, dir, testAccount)
	child.expect("holding")

	// 子进程持有期间：记录读得到，接手拿不到
	rec := readRecordFile(t, dir)
	if rec.State != StateHolding || rec.Holder != HolderGo || rec.PID != child.cmd.Process.Pid || rec.ReleasedAt != 0 {
		t.Fatalf("子进程持有时的记录是 %+v", rec)
	}
	if h, err := Take(dir, testAccount); !errors.Is(err, ErrLocked) {
		if h != nil {
			_ = h.Release()
		}
		t.Fatalf("子进程拿着锁，这边拿到的是 %v，要 %v", err, ErrLocked)
	}

	child.release()
	if rec = readRecordFile(t, dir); rec.State != StateReleased || rec.ReleasedAt == 0 {
		t.Fatalf("子进程交出去以后的记录是 %+v", rec)
	}

	h, err := Take(dir, testAccount)
	if err != nil {
		t.Fatalf("子进程交出了，这边该接得过来：%v", err)
	}
	if h.Resumed {
		t.Fatal("正常交接过来的，不该报成接着上一次")
	}
	if err := h.Release(); err != nil {
		t.Fatalf("交出去：%v", err)
	}
}

// 这边拿着锁的时候，另一配置成 go 的进程拿不到（它的发送类调用就一次也发不出去）；
// 这边放出以后，新起的那个才拿得到
func TestTwoProcessesRefusedWhileLocked(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	writeRecordFile(t, dir, freshReleasedRecord(testAccount))

	h, err := Take(dir, testAccount)
	if err != nil {
		t.Fatalf("该接得过来：%v", err)
	}
	child := startHolder(t, dir, testAccount)
	child.expectPrefix("take: " + ErrLocked.Error())
	if err := child.waitExit(); err == nil {
		t.Fatal("抢不到锁的子进程该以非 0 退出")
	}

	if err := h.Release(); err != nil {
		t.Fatalf("交出去：%v", err)
	}
	child2 := startHolder(t, dir, testAccount)
	child2.expect("holding")
	child2.release()
}

// 进程被杀：来不及写 released，锁由系统放掉。记录里还是自己（Go）在 holding → 直接接着拿
func TestKilledHolderLockComesBack(t *testing.T) {
	dir := t.TempDir()
	writeIntent(t, dir, Intent{Account: testAccount, Holder: HolderGo})
	writeRecordFile(t, dir, freshReleasedRecord(testAccount))

	child := startHolder(t, dir, testAccount)
	child.expect("holding")
	child.kill()

	if rec := readRecordFile(t, dir); rec.State != StateHolding || rec.Holder != HolderGo {
		t.Fatalf("被杀的进程不该改记录，现在读到的是 %+v", rec)
	}

	// 锁由系统在进程退出时放掉；Windows 收尾可能晚一点点，短重试（最多 3 秒）
	deadline := time.Now().Add(3 * time.Second)
	var (
		got *Holder
		err error
	)
	for {
		got, err = Take(dir, testAccount)
		if err == nil || !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("进程被杀以后锁该放掉，拿回来的是 %v", err)
	}
	if !got.Resumed {
		t.Fatal("上一次是自己持有的（被杀了），该接着拿并报 Resumed")
	}
	if !got.CanSend() {
		t.Fatal("被杀后重新拿到自己的锁，却不能发送")
	}
	rec := readRecordFile(t, dir)
	if rec.State != StateHolding || rec.Holder != HolderGo || rec.PID != os.Getpid() {
		t.Fatalf("接着拿以后的记录是 %+v", rec)
	}
	if err := got.Release(); err != nil {
		t.Fatalf("交出去：%v", err)
	}
}
