package main

import (
	"context"
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
	"sumika-native/internal/datalock"
	"sumika-native/internal/paths"
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
	<-h.ctx.Done() // 等关机真开始了再放 Open 过去，不然测到的可能是“Open 做完以后才退出”
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

// -rpc-listen 写 run/rpc-go.json；run/rpc.json 是 Bun 后台留下的名字，Go 开着协议入口时不碰它
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
		t.Fatalf("run/rpc.json 不是 Go 的文件，Go 不该写它：%s", p.RPCRunFile())
	}
	if filepath.Base(p.RPCGoRunFile()) != "rpc-go.json" || filepath.Base(p.RPCRunFile()) != "rpc.json" {
		t.Fatalf("文件名不对：%s %s", p.RPCGoRunFile(), p.RPCRunFile())
	}
}

// 已经在跑，后来切到接收时数据锁被别的进程抢走，回 archive 也开不了，进 blocked；
// 锁放开、再触发一次：后台开成功，宿主恢复回 archive，界面拿到新的数据源（原来 Bun 测试的后半段，删 Bun 后留下的恢复用例）
func TestHostBlockedByLockRecoversToArchiveWhenLockReleased(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	t.Cleanup(func() { _ = h.shutdown() })
	if s := log.last(); s.Phase != phaseArchive || s.Reader == nil {
		t.Fatalf("出发时该是 archive：%+v", s)
	}
	var other *datalock.DataLock
	h.beforeOpen = func(m backend.Mode) {
		if m == backend.Receive && other == nil {
			var err error
			if other, err = datalock.LockData(opt.DataDir); err != nil {
				t.Errorf("测试抢锁失败：%v", err)
			}
		}
	}
	if r := h.startReceive(); r.Outcome != receiveBlocked {
		t.Fatalf("锁被抢走该受阻：%+v", r)
	}
	if s := log.last(); s.Phase != phaseBlocked || s.Reader != nil || h.cur != nil {
		t.Fatalf("锁被抢走后该 blocked、没有数据源：%+v", s)
	}
	// 锁放开，再触发一次：receive 开不了（夹具没写协议端地址），回 archive 成功
	if other == nil {
		t.Fatal("测试没抢到锁")
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	h.beforeOpen = nil
	if r := h.startReceive(); r.Outcome != receiveOpenFailed {
		t.Fatalf("锁放开后 receive 因没写协议端地址开不了、回 archive：%+v", r)
	}
	s := log.last()
	if s.Phase != phaseArchive || s.Reader == nil || h.cur == nil {
		t.Fatalf("恢复后该回 archive 并有数据源：%+v", s)
	}
	if s.localSendOff() == "" {
		t.Fatalf("archive 阶段发送闸门该还是拦着：%+v", s)
	}
}
