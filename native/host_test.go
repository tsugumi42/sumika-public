package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/datalock"
	"sumika-native/internal/paths"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcwire"
	"sumika-native/internal/store"
)

func refuseDial(context.Context, string, string) (net.Conn, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}

// hostFixture 用真实迁移建临时 Go 库；配置里一个编造的小号没写协议端地址，receive 一定开不了。
func hostFixture(t *testing.T) backend.Options {
	t.Helper()
	data := filepath.Join(t.TempDir(), "宿主 #%&")
	p, err := paths.Init(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("qq:\n  accounts:\n    - id: \"90001\"\n      character: a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	set.Close()
	return backend.Options{Root: t.TempDir(), DataDir: data, Logf: t.Logf, DialContext: refuseDial, SharedAnalyzer: testAnalyzer()}
}

type stateLog struct {
	mu  sync.Mutex
	all []hostState
}

func (l *stateLog) add(s hostState) { l.mu.Lock(); l.all = append(l.all, s); l.mu.Unlock() }
func (l *stateLog) last() hostState { l.mu.Lock(); defer l.mu.Unlock(); return l.all[len(l.all)-1] }

func TestHostArchiveReceiveFallbackAndShutdown(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	if h.account != "90001" {
		t.Fatalf("配置里只有一个小号时应选它：%q %q", h.account, h.noRecv)
	}
	if s := log.last(); s.Phase != phaseArchive || s.Reader == nil {
		t.Fatalf("启动：%+v", s)
	}
	if _, err := log.last().Reader.WatchOverview(context.Background(), api.OverviewRequest{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	// receive 开不了（没写协议端地址）：回到 archive，写明原因
	if r := h.startReceive(); r.Outcome != receiveOpenFailed || r.Err == nil || !r.Retryable() {
		t.Fatalf("receive 开不了应返回可重试的 open 失败：%+v", r)
	}
	s := log.last()
	if s.Phase != phaseArchive || s.Reader == nil || !strings.Contains(s.Reason, "开始接收失败") {
		t.Fatalf("receive 失败后：%+v", s)
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
	if s := log.last(); s.Phase != phaseStopped {
		t.Fatalf("退出：%+v", s)
	}
	lock, err := datalock.LockData(opt.DataDir)
	if err != nil {
		t.Fatalf("退出后数据锁没放：%v", err)
	}
	lock.Close()
}

// 锁空出来的那一下被别的进程拿走：receive 和回退的 archive 都开不了，状态明说后台没在运行
func TestHostLockTakenDuringSwitch(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	var other *datalock.DataLock
	h.beforeOpen = func(m backend.Mode) {
		if m == backend.Receive && other == nil {
			var err error
			if other, err = datalock.LockData(opt.DataDir); err != nil {
				t.Errorf("测试抢锁失败：%v", err)
			}
		}
	}
	if r := h.startReceive(); r.Outcome != receiveBlocked || r.Retryable() {
		t.Fatalf("锁被抢走应返回不可重试的受阻：%+v", r)
	}
	s := log.last()
	if s.Phase != phaseBlocked || s.Reader != nil || !strings.Contains(s.Reason, "后台没在运行：锁被另一个进程占用") {
		t.Fatalf("锁被抢走后：%+v", s)
	}
	// 退出照常可用
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
	other.Close()
}

func TestHostReceiveNeedsOneConfirmedAccount(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	cfg := filepath.Join(opt.DataDir, "config", "sumika.yml")
	if err := os.WriteFile(cfg, []byte("qq:\n  accounts:\n    - id: \"90001\"\n      character: a\n    - id: \"90002\"\n      character: b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	if h.account != "" || !strings.Contains(h.noRecv, "2 个小号") {
		t.Fatalf("两个小号不能自动挑：%q %q", h.account, h.noRecv)
	}
	if r := h.startReceive(); r.Outcome != receiveNoAccount || r.Retryable() {
		t.Fatalf("没有可选小号应返回不可重试：%+v", r)
	}
	if s := log.last(); s.Phase != phaseArchive || !strings.Contains(s.Reason, "不能开始接收") {
		t.Fatalf("%+v", s)
	}
	h.shutdown()
}

// Open 还在进行时退出：退出打断它，30 秒内收完、锁放掉，同一组还能再开（T9）
func TestHostShutdownDuringOpen(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	entered, release := make(chan struct{}), make(chan struct{})
	h.beforeOpen = func(backend.Mode) { close(entered); <-release }
	go h.start()
	<-entered
	done := make(chan error, 1)
	go func() { done <- h.shutdown() }()
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Open 进行中退出，30 秒没收完")
	}
	if s := log.last(); s.Phase != phaseStopped {
		t.Fatalf("%+v", s)
	}
	h2 := newHost(opt, func(hostState) {})
	h2.start()
	if h2.cur == nil {
		t.Fatal("退出后同一组开不了")
	}
	h2.shutdown()
}

// 界面的订阅还开着就切后台：Desktop.Close 先把它收掉，旧订阅的通道关掉，不会碰到关掉的库
func TestHostSwitchClosesOldSubscriptions(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	sub, err := log.last().Reader.WatchOverview(context.Background(), api.OverviewRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	<-sub.Updates()
	h.startReceive() // 开不了，回 archive：中间旧的 archive 已经关过一次
	select {
	case _, ok := <-sub.Updates():
		for ok {
			_, ok = <-sub.Updates()
		}
	case <-time.After(uiWaitLimit):
		t.Fatal("旧订阅没有被收掉")
	}
	h.shutdown()
}

// 配置 Parse 失败时，状态说明和日志都要带出校验文字，而不是只说“诊断详情已隐藏”。
func TestHostConfigParseErrorIsVisible(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	cfg := paths.Paths{Root: opt.DataDir}.ConfigFile()
	if err := os.WriteFile(cfg, []byte("web:\n  timeoutMs: 999999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var lines []string
	var mu sync.Mutex
	opt.Logf = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	want := "web timeoutMs 越界"
	if got := log.last().NoRecv; !strings.Contains(got, want) || strings.Contains(got, "诊断详情已隐藏") {
		t.Fatalf("状态说明里应有配置错误原文：%q", got)
	}
	mu.Lock()
	all := strings.Join(lines, "\n")
	mu.Unlock()
	if !strings.Contains(all, want) {
		t.Fatalf("日志里应有配置错误原文：%q", all)
	}
}

// 配置类型错误时 YAML 会把出错的值放在反引号里；值可能是误贴的密钥，不能进日志和状态。
func TestHostConfigParseErrorHidesValue(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	cfg := paths.Paths{Root: opt.DataDir}.ConfigFile()
	if err := os.WriteFile(cfg, []byte("web:\n  timeoutMs: sk-编的密钥值123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var lines []string
	var mu sync.Mutex
	opt.Logf = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	mu.Lock()
	all := strings.Join(lines, "\n")
	mu.Unlock()
	if got := log.last().NoRecv; strings.Contains(got, "编的密钥值") || strings.Contains(all, "编的密钥值") {
		t.Fatalf("出错的值漏进了状态或日志：%q / %q", got, all)
	}
}

// -rpc-listen 写 run/rpc-go.json，run/rpc.json 归 Bun：Go 开着协议入口时不碰 rpc.json（规格-本地私聊切到Bun 第一节第 2 条）
func TestHostRPCListenWritesGoRunFile(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	h.rpcListen = true
	h.start()
	t.Cleanup(func() { _ = h.shutdown() })
	p, err := paths.Open(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.RPCGoRunFile()); err != nil {
		t.Fatalf("Go 的协议入口该写 %s：%v", p.RPCGoRunFile(), err)
	}
	if _, err := os.Stat(p.RPCRunFile()); err == nil {
		t.Fatalf("run/rpc.json 归 Bun，Go 不该写它：%s", p.RPCRunFile())
	}
	if filepath.Base(p.RPCGoRunFile()) != "rpc-go.json" || filepath.Base(p.RPCRunFile()) != "rpc.json" {
		t.Fatalf("文件名不对：%s %s", p.RPCGoRunFile(), p.RPCRunFile())
	}
}

// ───── 本地私聊交给 Bun（backend.local: bun，bunchild.go） ─────

// bunHostFixture：hostFixture 加上 backend.local: bun；Bun 子进程、连接都是假的（立刻连上、重试不等墙钟）
func bunHostFixture(t *testing.T) (*host, *stateLog, *bunHostProbe) {
	t.Helper()
	opt := hostFixture(t)
	p, err := paths.Open(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), append(b, []byte("backend:\n  local: bun\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	return bunHostOn(t, opt)
}

// bunHostOn：在已经写好 backend.local: bun 的数据目录上再造一个宿主（假 Bun 子进程和假连接）；第二个实例的测试用
func bunHostOn(t *testing.T, opt backend.Options) (*host, *stateLog, *bunHostProbe) {
	t.Helper()
	var log stateLog
	h := newHost(opt, log.add)
	probe := &bunHostProbe{}
	h.resolveBun = func(config.Backend, string, string) (bunSpec, error) {
		return bunSpec{Exe: "bun", Dir: opt.DataDir, DataDir: opt.DataDir}, nil
	}
	h.bunSpawn = func(spec bunSpec, _ func(string, ...any)) (bunProc, error) {
		probe.mu.Lock()
		defer probe.mu.Unlock()
		pr := newFakeProc(2000 + len(probe.procs))
		probe.procs = append(probe.procs, pr)
		dir := filepath.Join(spec.DataDir, "run")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "rpc.json"), []byte(`{"url":"ws://127.0.0.1:1/rpc","token":"编的令牌","pid":1,"impl":"bun","nonce":"`+nonceOf(spec.Env)+`"}`), 0o600)
		return pr, nil
	}
	h.bunTune = func(s *bunSupervisor) {
		s.poll = time.Millisecond
		s.wait = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
		s.dial = func(ctx context.Context, _, _ string) (*rpcclient.Client, error) {
			f := newFakeRPC()
			f.results = map[string]any{rpcwire.MethodRuntimeRead: rpcwire.Result[rpcwire.RuntimeView]{State: "ready", Value: &rpcwire.RuntimeView{Mode: "bun"}}}
			return rpcclient.New(ctx, f, "test")
		}
	}
	return h, &log, probe
}

type bunHostProbe struct {
	mu    sync.Mutex
	procs []*fakeProc
}

func (p *bunHostProbe) proc(i int) *fakeProc {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < len(p.procs) {
		return p.procs[i]
	}
	return nil
}

func (p *bunHostProbe) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.procs) }

func waitHost(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

// bun 模式：找不到 bun 或 server/ 直接进 blocked 写明原因，不去开 Go 的后台、不退回 Go 管本地私聊
func TestHostBunModeBlockedWhenBunOrServerMissing(t *testing.T) {
	t.Parallel()
	h, log, probe := bunHostFixture(t)
	h.resolveBun = func(config.Backend, string, string) (bunSpec, error) {
		return bunSpec{}, errors.New("找不到 bun：PATH 里没有")
	}
	h.start()
	s := log.last()
	if s.Phase != phaseBlocked || s.Reader != nil || !strings.Contains(s.Reason, "backend.local") || !strings.Contains(s.Reason, "找不到 bun") {
		t.Fatalf("找不到 bun 该 blocked 并给原因：%+v", s)
	}
	if h.cur != nil || probe.count() != 0 {
		t.Fatalf("blocked 时不该开 Go 后台、不该起子进程：cur=%v procs=%d", h.cur, probe.count())
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
}

// bun 模式整条：archive 阶段 Bun 一连上就能发本地消息；Bun 死了发送被拦、状态行写原因，界面拿到的 Reader 不变（不清界面）；
// 切到 receive 不重起 Bun；关机收掉 Bun
func TestHostBunModeLifecycle(t *testing.T) {
	t.Parallel()
	h, log, probe := bunHostFixture(t)
	h.start()
	waitHost(t, "Bun 连上", func() bool { return log.last().LocalOK })
	s := log.last()
	if s.Phase != phaseArchive || !s.LocalByBun || s.localSendOff() != "" || !strings.Contains(s.LocalLine, "Bun") {
		t.Fatalf("archive 阶段 Bun 连上后该能发本地消息：%+v", s)
	}
	if _, ok := s.Reader.(*rpcBridge); !ok {
		t.Fatalf("bun 模式界面拿到的该是桥：%T", s.Reader)
	}
	reader := s.Reader
	// 本地私聊的写方法经桥到了 Bun 的连接；读方法照走 Go
	if _, err := reader.(*rpcBridge).bun.get(); err != nil {
		t.Fatal(err)
	}

	// Bun 死了：发送被拦、状态行写原因；Reader 还是同一个
	probe.proc(0).die(errors.New("退出码 1"))
	waitHost(t, "Bun 又连上（监护重启了）", func() bool { return probe.count() == 2 && log.last().LocalOK })
	if log.last().Reader != reader {
		t.Fatal("Bun 重启不该换界面的数据源")
	}

	// 切到 receive（开不了：没写协议端地址，回 archive）：Bun 不重起，连接不变
	before := probe.count()
	h.startReceive()
	if probe.count() != before {
		t.Fatalf("archive ↔ receive 切换不该重起 Bun：%d → %d", before, probe.count())
	}
	if !log.last().LocalOK || !log.last().LocalByBun {
		t.Fatalf("切换后本地私聊的状态该还在：%+v", log.last())
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
	if !probe.proc(probe.count() - 1).wasKilled() {
		t.Fatal("关机该收掉 Bun 子进程")
	}
}

// 死到放弃：发送被拦并显示原因（不再自动重试）
func TestHostBunModeShowsWhyWhenBunKeepsDying(t *testing.T) {
	t.Parallel()
	h, log, probe := bunHostFixture(t)
	h.bunSpawn = func(spec bunSpec, _ func(string, ...any)) (bunProc, error) {
		pr := newFakeProc(3000)
		probe.mu.Lock()
		probe.procs = append(probe.procs, pr)
		probe.mu.Unlock()
		pr.die(errors.New("起来就崩"))
		return pr, nil
	}
	h.start()
	waitHost(t, "放弃重启", func() bool { return strings.Contains(log.last().LocalLine, "不再自动重试") })
	s := log.last()
	if s.LocalOK || s.localSendOff() == "" || !strings.Contains(s.localSendOff(), "本地私聊的后台不在") {
		t.Fatalf("Bun 起不来时发送该被拦并写原因：%+v", s)
	}
	if s.Phase != phaseArchive || s.Reader == nil {
		t.Fatalf("Go 这边照常（只看历史还能看）：%+v", s)
	}
	_ = h.shutdown()
}

// go 模式不起 Bun、不改行为；桥不带 bun
func TestHostGoModeStartsNoBun(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	h.bunSpawn = func(bunSpec, func(string, ...any)) (bunProc, error) {
		t.Error("go 模式不该起 Bun 子进程")
		return nil, errors.New("不该走到这里")
	}
	h.start()
	if s := log.last(); s.LocalByBun || s.localSendOff() != "请先启动后台，再发送本地消息" {
		t.Fatalf("go 模式 archive 阶段发送照旧被拦：%+v", s)
	}
	if b, ok := log.last().Reader.(*rpcBridge); !ok || b.bun != nil {
		t.Fatalf("go 模式的桥不带 bun：%T", log.last().Reader)
	}
	_ = h.shutdown()
}
