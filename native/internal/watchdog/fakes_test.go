package watchdog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 测试用的假东西：时钟由测试手动拨，子进程由测试决定什么时候退出，通知和探测只记账。
// 全程没有固定时长的等待：守护进程停在"等下一个定时器"时，测试拨时钟让它往下走。

const guard = 15 * time.Second // 只用来防止测试卡死，不是同步手段

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	armed  chan struct{}
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local), armed: make(chan struct{}, 1024)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	select {
	case c.armed <- struct{}{}:
	default:
	}
	return t.ch
}

// advance 直接拨 d，把到点的定时器放出去
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
		} else {
			kept = append(kept, t)
		}
	}
	c.timers = kept
}

// step 等守护进程挂上一个定时器（说明它停在等时间上了），再拨 d
func (c *fakeClock) step(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-c.armed:
	case <-time.After(guard):
		t.Fatal("守护进程没有停在等定时器上：它在等别的东西，或者已经结束了")
	}
	c.advance(d)
}

// stepUntil 守护进程每停在一个定时器上就拨 d，直到 cond 成立。
// 拿到"挂上了新定时器"的信号说明守护进程已经做完上一轮的事，这时 cond 的结果是可靠的，所以先看 cond 再决定拨不拨
func (c *fakeClock) stepUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(guard)
	for !cond() {
		select {
		case <-c.armed:
			if cond() {
				return
			}
			c.advance(d)
		case <-time.After(time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("等 %s 等不到", what)
		}
	}
}

type fakeChild struct {
	pid    int
	done   chan struct{}
	once   sync.Once
	code   atomic.Int32
	killed atomic.Bool
	// killFails：Kill 报错且进程不退出（模拟结束不掉）
	killFails bool
}

func (c *fakeChild) Pid() int              { return c.pid }
func (c *fakeChild) Done() <-chan struct{} { return c.done }
func (c *fakeChild) ExitCode() int         { return int(c.code.Load()) }
func (c *fakeChild) exit(code int) {
	c.code.Store(int32(code))
	c.once.Do(func() { close(c.done) })
}
func (c *fakeChild) Kill() error {
	c.killed.Store(true)
	if c.killFails {
		return fmt.Errorf("拒绝访问")
	}
	c.exit(1)
	return nil
}

type fakeNotifier struct {
	mu    sync.Mutex
	calls [][2]string
}

func (n *fakeNotifier) Notify(title, body string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, [2]string{title, body})
	return nil
}

func (n *fakeNotifier) list() [][2]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([][2]string(nil), n.calls...)
}

// harness 把守护进程接上全套假东西
type harness struct {
	t        *testing.T
	dir      string
	clock    *fakeClock
	notifier *fakeNotifier
	params   Params
	spawned  chan *fakeChild
	launched chan int     // 守护进程记完这次的启动时刻（打出“已拉起”）才放出 pid，见 child()
	spawnErr atomic.Value // error
	count    atomic.Int32
	probes   atomic.Int32
	probeErr atomic.Value // 非 nil 的 error 表示探测不通
	shutting atomic.Bool
	nextPid  atomic.Int32
	killFail bool
	sup      *Supervisor
	result   chan Result
	cancel   context.CancelFunc
}

type errBox struct{ err error }

func testParams() Params {
	return Params{
		Stuck: 5 * time.Minute, StartGrace: 3 * time.Minute, CrashWindow: 10 * time.Minute, RestartDelay: 3 * time.Second,
		CrashLimit: 3, Notify: true, CheckEvery: 30 * time.Second, ProbeTimeout: 10 * time.Second, ProbeStrikes: 2,
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), clock: newFakeClock(), notifier: &fakeNotifier{}, params: testParams(),
		spawned: make(chan *fakeChild, 64), launched: make(chan int, 64), result: make(chan Result, 1)}
	h.nextPid.Store(1000)
	h.probeErr.Store(errBox{err: fmt.Errorf("连不上")})
	h.sup = &Supervisor{
		DataDir:  h.dir,
		Params:   func() Params { return h.params },
		Notifier: h.notifier,
		Now:      h.clock.Now,
		After:    h.clock.After,
		Logf: func(f string, a ...any) {
			t.Logf("守护进程："+f, a...)
			if f == launchedLog && len(a) == 1 {
				if pid, ok := a[0].(int); ok {
					h.launched <- pid
				}
			}
		},
		Pid: 7,
		// 不碰这台机器的真进程表和登录时间：没有谁还活着、不按登录时间过滤（启动对账的用例在 reconcile_test.go 里另设）
		Alive:        func(int) bool { return false },
		SessionStart: func() time.Time { return time.Time{} },
		ShuttingDown: func() bool {
			return h.shutting.Load()
		},
		Probe: func(context.Context, int) error {
			h.probes.Add(1)
			return h.probeErr.Load().(errBox).err
		},
		Spawn: func(context.Context) (Child, error) {
			h.count.Add(1)
			if e, _ := h.spawnErr.Load().(errBox); e.err != nil {
				return nil, e.err
			}
			c := &fakeChild{pid: int(h.nextPid.Add(1)), done: make(chan struct{}), killFails: h.killFail}
			h.spawned <- c
			return c, nil
		},
	}
	return h
}

func (h *harness) probeAnswers(ok bool) {
	if ok {
		h.probeErr.Store(errBox{})
	} else {
		h.probeErr.Store(errBox{err: fmt.Errorf("连不上")})
	}
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.result <- h.sup.Run(ctx) }()
	h.t.Cleanup(func() {
		cancel()
		select {
		case <-h.result:
		case <-time.After(guard):
		}
	})
}

// launchedLog 是守护进程记完启动时刻、写完状态以后打的那行（supervisor.go）
const launchedLog = "已拉起桌面程序，进程 %d"

// child 等守护进程拉起下一个桌面程序，而且等它把这次的启动时刻记完才返回。
// 只等 Spawn 不够：Spawn 返回后守护进程才读时钟记“从什么时候开始跑”，测试这时拨时钟会被它算成“刚起就退”（11-17 合并队列负载下红过）
func (h *harness) child() *fakeChild {
	h.t.Helper()
	var c *fakeChild
	select {
	case c = <-h.spawned:
	case <-time.After(guard):
		h.t.Fatal("守护进程没有拉起桌面程序")
		return nil
	}
	select {
	case pid := <-h.launched:
		if pid != c.pid {
			h.t.Fatalf("记下的进程 %d 不是刚拉起的 %d", pid, c.pid)
		}
	case <-time.After(guard):
		h.t.Fatal("守护进程拉起以后没有记下启动时刻")
	}
	return c
}

func (h *harness) wait() Result {
	h.t.Helper()
	select {
	case r := <-h.result:
		h.result <- r // 清理时还要读一次
		return r
	case <-time.After(guard):
		h.t.Fatal("守护进程没有结束")
		return 0
	}
}

func (h *harness) beat(pid int, quitting bool) {
	h.t.Helper()
	if err := WriteHeartbeat(h.dir, Heartbeat{Pid: pid, StartedAt: h.clock.Now(), Time: h.clock.Now(), Version: "t", Phase: "archive", Quitting: quitting}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) incidents() []string {
	h.t.Helper()
	m, _ := filepath.Glob(filepath.Join(h.dir, "logs", "incidents", "*.md"))
	return m
}

func (h *harness) readIncident(path string) string {
	h.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *harness) writeLog(name, text string, appendTo bool) {
	h.t.Helper()
	p := filepath.Join(h.dir, "logs", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendTo {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(p, flag, 0o644)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		h.t.Fatal(err)
	}
}
