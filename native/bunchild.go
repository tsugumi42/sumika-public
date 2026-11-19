package main

// 临时：Electrobun 接管进程后去掉（规格-网页界面和Electrobun、ADR-353：以后 Bun 和窗口由 Electrobun 主进程管，不再由 Go 拉起；docs/待办.md 挂着）。
//
// backend.local 是 bun 时，sumika.exe 拉起 Bun 后台子进程（规格-本地私聊切到Bun 第四节）：
//   bun <server>/src/main.ts -data <数据目录>
// 等 run/rpc.json 出现（要比这次启动新、不认上次留下的）再连上；死了按 2、5、15 秒重启，起不稳连续超过三次重试就停并写明原因；
// sumika.exe 退出时子进程一起收掉：Windows 用作业对象（进程挂了系统也会关句柄、带走子进程），不按名字杀。
// 找不到 bun 或 server/ 是配置问题，重试也不会好：host 直接进 blocked 并写明原因。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcwire"
)

// bunSpec 是要起的那个 Bun 子进程
type bunSpec struct {
	Exe     string
	Args    []string
	Dir     string   // 工作目录（server/）
	Env     []string // 追加到当前环境
	DataDir string   // 数据目录：run/rpc.json 在 <DataDir>/run/
}

// bunProc 是一个起来了的子进程
type bunProc interface {
	Pid() int
	Done() <-chan struct{} // 进程退出后关闭
	Err() error            // 退出原因，Done 之后才能读
	Kill()
}

// resolveBunExe 找 bun 可执行文件：配置写了就只认它（写了但不存在不悄悄换别的）；否则 PATH 里的 bun，再看 <用户目录>\.bun\bin\bun.exe
func resolveBunExe(cfg config.Backend, lookPath func(string) (string, error), home string, exists func(string) bool) (string, error) {
	if p := strings.TrimSpace(cfg.Bun); p != "" {
		if exists(p) {
			return p, nil
		}
		return "", fmt.Errorf("配置 backend.bun 指的文件不存在：%s", p)
	}
	if p, err := lookPath("bun"); err == nil && p != "" {
		return p, nil
	}
	if home != "" {
		if p := filepath.Join(home, ".bun", "bin", "bun.exe"); exists(p) {
			return p, nil
		}
	}
	return "", errors.New(`找不到 bun：PATH 里没有，%USERPROFILE%\.bun\bin\bun.exe 也不在。装好 bun，或在 sumika.yml 的 backend.bun 里写它的路径`)
}

// resolveServerDir 找 server/ 目录：配置写了就只认它（相对路径从资源根算）；否则资源根下的 server/。里面要有 src/main.ts
func resolveServerDir(cfg config.Backend, root string, exists func(string) bool) (string, error) {
	dir := strings.TrimSpace(cfg.Server)
	switch {
	case dir == "":
		dir = filepath.Join(root, "server")
	case !filepath.IsAbs(dir):
		dir = filepath.Join(root, dir)
	}
	if !exists(filepath.Join(dir, "src", "main.ts")) {
		return "", fmt.Errorf("找不到 Bun 后台的代码：%s 里没有 src/main.ts（用 backend.server 指到 server/ 目录）", dir)
	}
	return dir, nil
}

// bunBackoff 是起不稳之后每次重试前等多久；连续起不稳超过这么多次重试就停
var bunBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}

// hostNonceEnv 是宿主交给 Bun 子进程的环境变量名：Bun 把它的值写进 run/rpc.json 的 nonce，宿主靠它认出地址文件是不是自己刚拉起的那个写的
const hostNonceEnv = "SUMIKA_HOST_NONCE"

const (
	bunConnectTimeout = 30 * time.Second // 从启动到连上的总时限
	bunStableAfter    = time.Minute      // 连上以后撑过这么久才算“起稳了”，之后再死从头数
)

type bunSupervisor struct {
	spec     bunSpec
	link     *bunLink
	logf     func(string, ...any)
	onStatus func(ok bool, line string) // 状态行：由 host 转给界面

	// 下面几个字段默认走真的，测试替换
	spawn          func(bunSpec, func(string, ...any)) (bunProc, error)
	backoff        []time.Duration
	stableAfter    time.Duration
	connectTimeout time.Duration
	poll           time.Duration                             // 等地址文件出现的查询间隔
	wait           func(context.Context, time.Duration) bool // 重试之间等一段时间；ctx 取消回 false
	pollWait       func(context.Context, time.Duration) bool // 等地址文件出现时的间隔
	dial           func(ctx context.Context, url, token string) (*rpcclient.Client, error)
	newNonce       func() string // 每次拉子进程换一个新的（hostNonceEnv）

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newBunSupervisor(spec bunSpec, link *bunLink, logf func(string, ...any), onStatus func(bool, string)) *bunSupervisor {
	return &bunSupervisor{spec: spec, link: link, logf: logf, onStatus: onStatus,
		spawn: spawnBun, backoff: bunBackoff, stableAfter: bunStableAfter, connectTimeout: bunConnectTimeout, poll: 100 * time.Millisecond,
		wait: sleepCtx, pollWait: sleepCtx, newNonce: randomNonce,
		dial: func(ctx context.Context, url, token string) (*rpcclient.Client, error) {
			return rpcclient.Dial(ctx, url, token, "sumika-desktop-bridge")
		}}
}

func randomNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // 系统随机数读不到，没法继续
	}
	return hex.EncodeToString(b)
}

// sleepCtx 等 d，ctx 取消提前回 false
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// start 在后台起监护循环；已经在跑就什么都不做。stop 之后可以再 start（宿主从 blocked 恢复时重新拉起 Bun）
func (s *bunSupervisor) start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.run(ctx)
	}()
}

// stop 停监护、收掉子进程，等循环退出。没 start 过也安全
func (s *bunSupervisor) stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	s.mu.Lock()
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	s.link.down("Bun 后台已停")
}

// running 说监护循环起着没有（start 过、还没 stop）
func (s *bunSupervisor) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel != nil
}

func (s *bunSupervisor) status(ok bool, line string) {
	if s.onStatus != nil {
		s.onStatus(ok, line)
	}
}

func (s *bunSupervisor) run(ctx context.Context) {
	fails := 0 // 连续起不稳的次数；起稳过（连上后撑满 stableAfter）就从头数
	for ctx.Err() == nil {
		s.status(false, "正在启动 Bun 后台……")
		started := time.Now()
		var reason string
		nonce := s.newNonce()
		spec := s.spec
		spec.Env = append(append([]string(nil), spec.Env...), hostNonceEnv+"="+nonce)
		proc, err := s.spawn(spec, s.logf)
		if err != nil {
			// 起都起不来（可执行文件打不开等）：重试不会变好，不数次数，直接停
			s.giveUp("起不了 Bun 进程：" + err.Error())
			return
		}
		client, why := s.connect(ctx, proc, started, nonce)
		if client == nil {
			s.kill(proc)
			if ctx.Err() != nil {
				return
			}
			reason = why
		} else {
			s.link.up(rpcclient.NewFull(client))
			s.status(true, "本地私聊由 Bun 后台管")
			s.logf("Bun 后台已连上（进程 %d）", proc.Pid())
			upAt := time.Now()
			select {
			case <-proc.Done():
				reason = "Bun 进程退出了" + exitText(proc)
			case <-client.Done():
				reason = "和 Bun 的连接断了"
			case <-ctx.Done():
			}
			s.kill(proc)
			client.Close()
			if ctx.Err() != nil {
				return
			}
			if time.Since(upAt) >= s.stableAfter {
				fails = 0
			}
		}
		fails++
		if fails > len(s.backoff) {
			s.giveUp(fmt.Sprintf("%s；已连续 %d 次没起稳，不再自动重试", reason, fails))
			return
		}
		d := s.backoff[fails-1]
		s.link.down(reason)
		s.status(false, fmt.Sprintf("本地私聊的后台不在：%s（%d 秒后重试，第 %d/%d 次）", reason, int(d/time.Second), fails, len(s.backoff)))
		s.logf("Bun 后台没起稳：%s；%v 后重试（第 %d/%d 次）", reason, d, fails, len(s.backoff))
		if !s.wait(ctx, d) {
			return
		}
	}
}

func (s *bunSupervisor) giveUp(reason string) {
	s.logf("Bun 后台停止自动重启：%s", reason)
	s.link.down(reason)
	s.status(false, "本地私聊的后台不在："+reason+"。看桌面日志里 Bun 的输出，修好后重启 sumika.exe")
}

// kill 收掉子进程并等它真的退出
func (s *bunSupervisor) kill(p bunProc) {
	p.Kill()
	<-p.Done()
}

func exitText(p bunProc) string {
	if err := p.Err(); err != nil {
		return "（" + err.Error() + "）"
	}
	return ""
}

// connect 等子进程写出新的 run/rpc.json 再连上，并确认它进了写模式。失败回原因（ctx 取消回空）。
// 地址文件要认得出是这个子进程写的（runFileIsOurs）：只看修改时间的话，别的进程碰巧在这段时间写的 rpc.json 也会被连上
func (s *bunSupervisor) connect(ctx context.Context, proc bunProc, started time.Time, nonce string) (*rpcclient.Client, string) {
	runFile := filepath.Join(s.spec.DataDir, "run", "rpc.json")
	deadline := time.Now().Add(s.connectTimeout)
	var last string
	for {
		select {
		case <-proc.Done():
			return nil, "Bun 进程在连上之前就退出了" + exitText(proc)
		case <-ctx.Done():
			return nil, ""
		default:
		}
		if rf, ok := readFreshRunFile(runFile, started); ok && runFileIsOurs(rf, nonce) {
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			c, err := s.dial(dctx, rf.URL, rf.Token)
			if err == nil {
				if why := checkWritable(dctx, c); why != "" {
					cancel()
					c.Close()
					return nil, why
				}
				cancel()
				return c, ""
			}
			cancel()
			last = "：" + err.Error()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Sprintf("等了 %d 秒没连上 Bun%s", int(s.connectTimeout/time.Second), last)
		}
		if !s.pollWait(ctx, s.poll) {
			return nil, ""
		}
	}
}

// readFreshRunFile 读 run/rpc.json；比这次启动更早的不认（上次留下的，对应的进程早死了或不是这次起的）
func readFreshRunFile(path string, started time.Time) (rpcwire.RunFile, bool) {
	var rf rpcwire.RunFile
	info, err := os.Stat(path)
	if err != nil || info.ModTime().Before(started.Add(-time.Second)) {
		return rf, false
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &rf) != nil || rf.URL == "" || rf.Token == "" {
		return rf, false
	}
	return rf, true
}

// runFileIsOurs：地址文件是不是我们刚拉起的那个 Bun 写的。认 impl（有就必须是 bun）和一次性的 nonce——
// 不认 pid：宿主拉起的可能是垫片（scoop 的 shimsun.exe 再启动真的 bun），进程号对不上；nonce 只有这次拉起的子进程树拿得到，别的进程写不出来
func runFileIsOurs(rf rpcwire.RunFile, nonce string) bool {
	if rf.Impl != "" && rf.Impl != "bun" {
		return false
	}
	return nonce != "" && rf.Nonce == nonce
}

// checkWritable：Bun 进了写模式才算数。只读（配置不是 bun、local.lock 被占、run/ 收不紧）连上了也没法管本地私聊，回原因
func checkWritable(ctx context.Context, c *rpcclient.Client) string {
	var rv rpcwire.Result[rpcwire.RuntimeView]
	if err := c.Call(ctx, rpcwire.MethodRuntimeRead, rpcwire.Empty{}, &rv); err != nil {
		return "读 Bun 的运行状态失败：" + err.Error()
	}
	if rv.Value == nil || rv.Value.Mode != "bun" {
		reason := ""
		if rv.Value != nil {
			reason = rv.Value.Reason
		}
		return "Bun 后台是只读的，管不了本地私聊：" + reason
	}
	return ""
}

// lineLog 把子进程的输出按行转给日志，行首加“Bun：”
type lineLog struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	logf func(string, ...any)
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		i := bytes.IndexByte(l.buf.Bytes(), '\n')
		if i < 0 {
			if l.buf.Len() > 64<<10 { // 没有换行的超长输出：切一段，别无限攒
				l.logf("Bun：%s", strings.TrimRight(l.buf.String(), "\r\n"))
				l.buf.Reset()
			}
			return len(p), nil
		}
		line := strings.TrimRight(string(l.buf.Next(i+1)), "\r\n")
		if line != "" {
			l.logf("Bun：%s", line)
		}
	}
}

type execProc struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *execProc) Pid() int              { return p.cmd.Process.Pid }
func (p *execProc) Done() <-chan struct{} { return p.done }
func (p *execProc) Err() error            { return p.err }
func (p *execProc) Kill()                 { _ = p.cmd.Process.Kill() }

// spawnBun 真的起子进程：隐藏控制台窗口、输出进日志、挂进作业对象（sumika.exe 退出时系统连带收掉）
func spawnBun(spec bunSpec, logf func(string, ...any)) (bunProc, error) {
	cmd := exec.Command(spec.Exe, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.Env...)
	out := &lineLog{logf: logf}
	cmd.Stdout, cmd.Stderr = out, out
	hideChildWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := assignToKillJob(cmd.Process); err != nil {
		// 挂不进作业对象：子进程就没有“跟着 sumika.exe 一起死”的保证，宁可不要
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("没能把 Bun 进程挂进作业对象（退出时收不掉它）：%w", err)
	}
	p := &execProc{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}
