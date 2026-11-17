package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// launch 是怎么起一份服务：可执行文件、参数（"{port}" 换成挑到的端口）、额外的环境变量。
type launch struct {
	Engine string // "go" 或 "python"
	Exe    string
	Args   []string
	Env    []string
	Stdin  bool // 开着 stdin 管道不关（Go 版 --exit-on-stdin-eof：父进程没了它就退）
}

// lineBuf 收子进程的 stderr：只留最近的若干行（服务的日志每个请求一行、不含输入文字，上限只为防内存）。
type lineBuf struct {
	mu    sync.Mutex
	lines []string
	part  bytes.Buffer
}

const maxLogLines = 20000

// stdinWait：关了 stdin 以后等 Go 版自己退多久，不退就 taskkill。
var stdinWait = 15 * time.Second

func (b *lineBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.part.Write(p)
	for {
		i := bytes.IndexByte(b.part.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(b.part.Next(i+1)), "\r\n")
		b.lines = append(b.lines, line)
		if len(b.lines) > maxLogLines {
			b.lines = b.lines[len(b.lines)-maxLogLines/2:]
		}
	}
	return len(p), nil
}

func (b *lineBuf) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
}

// tail 是出错时给人看的最后几行（服务的日志不含输入文字）。
func (b *lineBuf) tail(n int) string {
	l := b.snapshot()
	return strings.Join(l[max(0, len(l)-n):], "\n")
}

type serverProc struct {
	l      launch
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	log    *lineBuf
	cli    *client
	port   int
	began  time.Time
	done   chan struct{}
	exit   int
	waited sync.Once
}

var (
	procMu  sync.Mutex
	started []*serverProc // 起过的全部，收尾时核对没有残留
)

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// startReady 起一份服务并等到就绪：随机端口、随机令牌（走环境变量，不进命令行）。Go 版端口被抢时退出码是 3（freePort 先监听再关，
// 到子进程绑定之间有窗口），换一个口再试一次；别的退出都直接报错。
func startReady(ctx context.Context, l launch, timeout time.Duration) (*serverProc, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		p, err := tryStart(l)
		if err != nil {
			return nil, err
		}
		if _, err := p.waitReady(ctx, timeout); err != nil {
			p.stop()
			lastErr = err
			if errors.Is(err, errPortBusy) {
				continue
			}
			return nil, err
		}
		return p, nil
	}
	return nil, lastErr
}

var errPortBusy = errors.New("端口被占")

func tryStart(l launch) (*serverProc, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	token := randomToken()
	args := make([]string, len(l.Args))
	for i, a := range l.Args {
		args[i] = strings.ReplaceAll(a, "{port}", strconv.Itoa(port))
	}
	p := &serverProc{l: l, log: &lineBuf{}, port: port, done: make(chan struct{}), exit: -1}
	p.cmd = exec.Command(l.Exe, args...)
	p.cmd.Env = append(os.Environ(), append([]string{"SUMIKA_EMBED_TOKEN=" + token}, l.Env...)...)
	p.cmd.Stdout, p.cmd.Stderr = p.log, p.log
	if l.Stdin {
		if p.stdin, err = p.cmd.StdinPipe(); err != nil {
			return nil, err
		}
	}
	p.cli = newClient(fmt.Sprintf("http://127.0.0.1:%d", port), token)
	p.began = time.Now()
	if err := p.cmd.Start(); err != nil {
		return nil, fmt.Errorf("起 %s 失败：%w", l.Engine, err)
	}
	procMu.Lock()
	started = append(started, p)
	procMu.Unlock()
	go func() {
		err := p.cmd.Wait()
		var ee *exec.ExitError
		switch {
		case err == nil:
			p.exit = 0
		case errors.As(err, &ee):
			p.exit = ee.ExitCode()
		}
		close(p.done)
	}()
	return p, nil
}

func (p *serverProc) pid() int { return p.cmd.Process.Pid }

// waitReady 等 /health 回 200。进程在这之前退出就报错（带日志最后几行；服务的日志不含输入文字）。
// 返回从起进程到就绪过了多少毫秒。
func (p *serverProc) waitReady(ctx context.Context, timeout time.Duration) (int64, error) {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-p.done:
			if p.exit == 3 {
				return 0, fmt.Errorf("%w（%s）", errPortBusy, p.l.Engine)
			}
			return 0, fmt.Errorf("%s 在就绪之前退出了（退出码 %d）：\n%s", p.l.Engine, p.exit, p.log.tail(8))
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, status, err := p.cli.health(cctx)
		cancel()
		if err == nil && status == 200 {
			return time.Since(p.began).Milliseconds(), nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("%s 等了 %v 还没就绪：\n%s", p.l.Engine, timeout, p.log.tail(8))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stop 收掉一份服务，只按它自己的 PID：Go 版关 stdin 让它自己退（最多等 15 秒），不退或 Python 版就 taskkill /PID /T /F
// （Windows 上 venv 的 python.exe 是个启动器，真的解释器是它的子进程，要连子进程一起收）。
func (p *serverProc) stop() {
	p.waited.Do(func() {
		if p.cmd == nil || p.cmd.Process == nil {
			return
		}
		select {
		case <-p.done:
			return
		default:
		}
		if p.stdin != nil {
			p.stdin.Close()
			select {
			case <-p.done:
				return
			case <-time.After(stdinWait):
			}
		}
		killTree(p.pid())
		select {
		case <-p.done:
		case <-time.After(15 * time.Second):
		}
	})
}

func killTree(pid int) {
	if err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run(); err != nil {
		if proc, err2 := os.FindProcess(pid); err2 == nil {
			_ = proc.Kill()
		}
	}
}

// processExists 用 tasklist 查某个 PID 还在不在（收尾时核对：起过的服务一个都不该残留）。
func processExists(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if strings.Contains(sc.Text(), `","`+strconv.Itoa(pid)+`","`) {
			return true
		}
	}
	return false
}

// stopAll 收掉所有还在的，返回起过几份、收尾时还在的 PID（正常是空）。
func stopAll() (total int, residue []int) {
	procMu.Lock()
	all := append([]*serverProc(nil), started...)
	procMu.Unlock()
	for _, p := range all {
		p.stop()
	}
	for _, p := range all {
		if processExists(p.pid()) {
			residue = append(residue, p.pid())
		}
	}
	return len(all), residue
}

// loadedLine 是 Go 版日志里 LOADED 那一行的几个数。
type loadInfo struct {
	LoadMS int64 `json:"loadMs"`
	HashMS int64 `json:"hashMs"`
}

// parseLoaded 从日志里找每个模型的 LOADED 行：LOADED model=… load_ms=… hash_ms=…
func parseLoaded(lines []string) map[string]loadInfo {
	out := map[string]loadInfo{}
	for _, l := range lines {
		i := strings.Index(l, "LOADED model=")
		// "UNLOADED model=…" 里也有 "LOADED model=" 这几个字：前面必须是行首或空格，不然卸载那一行会把真的 LOADED 数覆盖成 0
		if i < 0 || (i > 0 && l[i-1] != ' ') {
			continue
		}
		kv := map[string]string{}
		for _, f := range strings.Fields(l[i+len("LOADED "):]) {
			if k, v, ok := strings.Cut(f, "="); ok {
				kv[k] = v
			}
		}
		ms, _ := strconv.ParseInt(kv["load_ms"], 10, 64)
		hs, _ := strconv.ParseInt(kv["hash_ms"], 10, 64)
		if kv["model"] != "" {
			out[kv["model"]] = loadInfo{LoadMS: ms, HashMS: hs}
		}
	}
	return out
}
