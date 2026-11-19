package main

// Bun 子进程的监护（bunchild.go，规格-本地私聊切到Bun 第四节）：
//   - 找 bun 和 server/：找不到回带原因的错；
//   - 起来、等新的 run/rpc.json、连上、确认进了写模式；死了按 2、5、15 秒重启，起不稳超过三次重试就停并写明原因；
//   - 起稳过（连上后撑满 stableAfter）再死从头数；上次留下的旧地址文件不认；
//   - stop 收掉子进程；真进程 + 作业对象：sumika.exe 没了，Bun 跟着没了。
// 监护循环用假子进程、假连接、立刻返回的“等待”，不等墙钟。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcwire"
)

func TestResolveBunExeAndServerDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("原因：bun 子进程监护只在 Windows 上用（bunchild.go 头一行：临时，Electrobun 接管进程后去掉），用例写的是 Windows 路径（D:\\repo、E:\\elsewhere\\server），不为它做 Linux 兼容")
	}
	t.Parallel()
	exists := func(set ...string) func(string) bool {
		return func(p string) bool {
			for _, s := range set {
				if filepath.ToSlash(s) == filepath.ToSlash(p) {
					return true
				}
			}
			return false
		}
	}
	none := func(string) (string, error) { return "", errors.New("没有") }
	found := func(string) (string, error) { return `C:\scoop\bun.exe`, nil }

	if p, err := resolveBunExe(config.Backend{Bun: `D:\x\bun.exe`}, found, `C:\Users\u`, exists(`D:\x\bun.exe`)); err != nil || p != `D:\x\bun.exe` {
		t.Fatalf("配置里写了就用它：%q %v", p, err)
	}
	if _, err := resolveBunExe(config.Backend{Bun: `D:\x\bun.exe`}, found, `C:\Users\u`, exists()); err == nil || !strings.Contains(err.Error(), "backend.bun") {
		t.Fatalf("配置写了但不存在要报错，不悄悄换成 PATH 里的：%v", err)
	}
	if p, err := resolveBunExe(config.Backend{}, found, `C:\Users\u`, exists()); err != nil || p != `C:\scoop\bun.exe` {
		t.Fatalf("PATH 里的 bun：%q %v", p, err)
	}
	home := `C:\Users\u`
	if p, err := resolveBunExe(config.Backend{}, none, home, exists(filepath.Join(home, ".bun", "bin", "bun.exe"))); err != nil || !strings.HasSuffix(p, "bun.exe") {
		t.Fatalf("用户目录下的 .bun：%q %v", p, err)
	}
	if _, err := resolveBunExe(config.Backend{}, none, home, exists()); err == nil || !strings.Contains(err.Error(), "找不到 bun") {
		t.Fatalf("都没有要报错并说清：%v", err)
	}

	root := `D:\repo`
	main := filepath.Join(root, "server", "src", "main.ts")
	if d, err := resolveServerDir(config.Backend{}, root, exists(main)); err != nil || d != filepath.Join(root, "server") {
		t.Fatalf("默认资源根下的 server/：%q %v", d, err)
	}
	if _, err := resolveServerDir(config.Backend{}, root, exists()); err == nil || !strings.Contains(err.Error(), "src/main.ts") {
		t.Fatalf("找不到 server/ 要报错并说清：%v", err)
	}
	other := `E:\elsewhere\server`
	if d, err := resolveServerDir(config.Backend{Server: other}, root, exists(filepath.Join(other, "src", "main.ts"))); err != nil || d != other {
		t.Fatalf("配置里写了绝对路径：%q %v", d, err)
	}
	if d, err := resolveServerDir(config.Backend{Server: "srv"}, root, exists(filepath.Join(root, "srv", "src", "main.ts"))); err != nil || d != filepath.Join(root, "srv") {
		t.Fatalf("相对路径从资源根算：%q %v", d, err)
	}
}

// ───── 假子进程、假连接 ─────

type fakeProc struct {
	pid    int
	done   chan struct{}
	once   sync.Once
	err    error
	killed bool
	mu     sync.Mutex
}

func newFakeProc(pid int) *fakeProc       { return &fakeProc{pid: pid, done: make(chan struct{})} }
func (p *fakeProc) Pid() int              { return p.pid }
func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Err() error            { return p.err }
func (p *fakeProc) Kill() {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.die(errors.New("被杀了"))
}
func (p *fakeProc) die(err error) {
	p.once.Do(func() { p.err = err; close(p.done) })
}
func (p *fakeProc) wasKilled() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.killed }

// supEnv：假的一切。sup.wait 立刻返回并记下每次的等待时长；sup.dial 给一个假连接（mode 决定 runtime.read 回什么）
type supEnv struct {
	t       *testing.T
	data    string
	sup     *bunSupervisor
	link    *bunLink
	mu      sync.Mutex
	spawns  int
	procs   []*fakeProc
	delays  []time.Duration
	lines   []string
	mode    string                       // 假 Bun 的 runtime.read 回什么 mode
	onSpawn func(e *supEnv, p *fakeProc) // 起来以后做什么：默认写出新的地址文件（连得上）
	spawnEr error
	statusC chan string
	nonce   string         // 最近一次 spawn 收到的 SUMIKA_HOST_NONCE：假 Bun 写地址文件时原样写回去
	bg      sync.WaitGroup // 在跑的 onSpawn：清理时先等它们写完，再删临时目录
}

func newSupEnv(t *testing.T) *supEnv {
	t.Helper()
	e := &supEnv{t: t, data: t.TempDir(), mode: "bun", statusC: make(chan string, 64)}
	t.Cleanup(e.bg.Wait) // 后登记先执行：排在 TempDir 删目录之前、测试里登记的 sup.stop 之后
	e.link = newBunLink("还没起")
	e.sup = newBunSupervisor(bunSpec{Exe: "bun", Dir: e.data, DataDir: e.data}, e.link, func(f string, a ...any) { e.logf(f, a...) },
		func(ok bool, line string) {
			select {
			case e.statusC <- map[bool]string{true: "ok:", false: "off:"}[ok] + line:
			default:
			}
		})
	e.sup.stableAfter = time.Hour
	e.sup.poll = time.Millisecond // 等地址文件的间隔用真等待（很短）；重试间隔（wait）才是假的、只记时长
	e.sup.wait = func(ctx context.Context, d time.Duration) bool {
		e.mu.Lock()
		e.delays = append(e.delays, d)
		e.mu.Unlock()
		return ctx.Err() == nil
	}
	e.sup.spawn = func(spec bunSpec, _ func(string, ...any)) (bunProc, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.nonce = nonceOf(spec.Env)
		if e.spawnEr != nil {
			e.spawns++
			return nil, e.spawnEr
		}
		e.spawns++
		p := newFakeProc(1000 + e.spawns)
		e.procs = append(e.procs, p)
		if e.onSpawn != nil {
			f := e.onSpawn
			e.bg.Add(1)
			go func() { defer e.bg.Done(); f(e, p) }()
		}
		return p, nil
	}
	e.onSpawn = func(e *supEnv, p *fakeProc) { e.writeRunFile(time.Now()) }
	e.sup.dial = func(ctx context.Context, url, token string) (*rpcclient.Client, error) {
		f := newFakeRPC()
		f.results = map[string]any{rpcwire.MethodRuntimeRead: rpcwire.Result[rpcwire.RuntimeView]{State: "ready", Value: &rpcwire.RuntimeView{Mode: e.currentMode(), Reason: "测试：假 Bun"}}}
		c, err := rpcclient.New(ctx, f, "test")
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { c.Close() })
		return c, nil
	}
	return e
}

func (e *supEnv) currentMode() string { e.mu.Lock(); defer e.mu.Unlock(); return e.mode }
func (e *supEnv) logf(f string, a ...any) {
	e.mu.Lock()
	e.lines = append(e.lines, strings.TrimSpace(strings.ReplaceAll(f, "%", "")))
	e.mu.Unlock()
	e.t.Logf(f, a...)
}

// writeRunFile 写 run/rpc.json，修改时间设成 at
func (e *supEnv) writeRunFile(at time.Time) {
	dir := filepath.Join(e.data, "run")
	_ = os.MkdirAll(dir, 0o755)
	f := filepath.Join(dir, "rpc.json")
	e.mu.Lock()
	nonce := e.nonce
	e.mu.Unlock()
	// pid 故意和假子进程的对不上：宿主拉起的可能是垫片，真 bun 的 pid 不一样，认的是 nonce
	_ = os.WriteFile(f, []byte(`{"url":"ws://127.0.0.1:1/rpc","token":"编的令牌","pid":1,"impl":"bun","nonce":"`+nonce+`"}`), 0o600)
	_ = os.Chtimes(f, at, at)
}

// nonceOf 从子进程的环境里取宿主给的 SUMIKA_HOST_NONCE
func nonceOf(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, hostNonceEnv+"="); ok {
			return v
		}
	}
	return ""
}

func (e *supEnv) procAt(i int) *fakeProc {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		if len(e.procs) > i {
			p := e.procs[i]
			e.mu.Unlock()
			return p
		}
		e.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	e.t.Fatalf("第 %d 个子进程一直没起", i+1)
	return nil
}

func (e *supEnv) waitStatus(prefix string) string {
	e.t.Helper()
	for {
		select {
		case s := <-e.statusC:
			if strings.HasPrefix(s, prefix) {
				return s
			}
		case <-time.After(10 * time.Second):
			e.t.Fatalf("等不到状态行 %q", prefix)
		}
	}
}

func (e *supEnv) waitLinkUp() *rpcclient.Full {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f, _, _ := e.link.state(); f != nil {
			return f
		}
		time.Sleep(time.Millisecond)
	}
	e.t.Fatal("连接一直没上")
	return nil
}

func (e *supEnv) waitLinkDown() {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f, _, _ := e.link.state(); f == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	e.t.Fatal("连接一直没断")
}

func (e *supEnv) counts() (int, []time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.spawns, append([]time.Duration(nil), e.delays...)
}

func TestSupervisorConnectsAndRestartsAfterDeath(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t)
	e.sup.start()
	t.Cleanup(e.sup.stop)
	f := e.waitLinkUp()
	if got := e.waitStatus("ok:"); !strings.Contains(got, "Bun 后台管") {
		t.Fatalf("状态行：%s", got)
	}
	if _, err := e.link.get(); err != nil {
		t.Fatal(err)
	}
	// 进程死了：连接标成不在，2 秒后重启，重启后又连上（重启是立刻的，link 的“不在”一闪而过，所以看状态行）
	e.procAt(0).die(errors.New("退出码 1"))
	if got := e.waitStatus("off:"); !strings.Contains(got, "本地私聊的后台不在") || !strings.Contains(got, "2 秒后重试") {
		t.Fatalf("死了之后的状态行：%s", got)
	}
	f2 := e.waitLinkUp()
	if f2 == f {
		t.Fatal("重启后该是新连接")
	}
	spawns, delays := e.counts()
	if spawns != 2 || len(delays) != 1 || delays[0] != 2*time.Second {
		t.Fatalf("死一次该 2 秒后重启：起了 %d 次，等待 %v", spawns, delays)
	}
}

func TestSupervisorGivesUpAfterThreeFailedRetries(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t)
	e.onSpawn = func(e *supEnv, p *fakeProc) { p.die(errors.New("起来就崩")) } // 一直写不出地址文件、进程马上退
	e.sup.start()
	t.Cleanup(e.sup.stop)
	got := e.waitStatus("off:本地私聊的后台不在：Bun 进程在连上之前就退出了（起来就崩）；已连续 4 次没起稳，不再自动重试")
	if !strings.Contains(got, "重启 sumika.exe") {
		t.Fatalf("放弃时的状态行要说怎么办：%s", got)
	}
	spawns, delays := e.counts()
	if spawns != 4 {
		t.Fatalf("起一次加三次重试共 4 次，实际 %d", spawns)
	}
	want := []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}
	if len(delays) != 3 || delays[0] != want[0] || delays[1] != want[1] || delays[2] != want[2] {
		t.Fatalf("重试间隔该是 2、5、15 秒：%v", delays)
	}
	if _, reason, _ := e.link.state(); !strings.Contains(reason, "不再自动重试") {
		t.Fatalf("连接状态里要带放弃的原因：%q", reason)
	}
	if _, err := e.link.get(); !errors.Is(err, api.ErrLocalBackendDown) {
		t.Fatalf("放弃以后走 Bun 的方法仍回后台不在：%v", err)
	}
}

// 起稳过（撑满 stableAfter）再死，从头数：不会因为跑了一整天、偶尔崩几次就攒够次数停掉
func TestSupervisorResetsCountAfterStableRun(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t)
	e.sup.stableAfter = 0 // 连上就算起稳
	e.sup.start()
	t.Cleanup(e.sup.stop)
	for i := 0; i < 6; i++ {
		e.waitStatus("ok:") // 这一个连上、算起稳了，再让它死（死得比连上早就是没起稳，不在这条测试里）
		e.procAt(i).die(errors.New("崩了"))
	}
	e.procAt(6) // 第七个起来了：前六次死亡之后的等待都已记下
	_, delays := e.counts()
	if len(delays) < 6 {
		t.Fatalf("应该一直重启：%v", delays)
	}
	for _, d := range delays {
		if d != 2*time.Second {
			t.Fatalf("每次起稳后再死都从头数，间隔该一直是 2 秒：%v", delays)
		}
	}
}

// 连上了但 Bun 是只读的（配置不是 bun、local.lock 被占、run/ 收不紧）：管不了本地私聊，不能当成功；原因带上 Bun 自己说的
func TestSupervisorRejectsReadOnlyBun(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t)
	e.mode = "bun-readonly"
	e.sup.start()
	t.Cleanup(e.sup.stop)
	got := e.waitStatus("off:本地私聊的后台不在：Bun 后台是只读的，管不了本地私聊：测试：假 Bun")
	if !strings.Contains(got, "1/3") {
		t.Fatalf("只读按没起稳算，要重试：%s", got)
	}
	if f, _, _ := e.link.state(); f != nil {
		t.Fatal("只读的 Bun 不能挂进连接")
	}
}

func TestSupervisorIgnoresStaleRunFile(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t)
	// 上次留下的地址文件：一小时前的。新进程还没写自己的，不能连它
	old := time.Now().Add(-time.Hour)
	e.writeRunFile(old)
	dialed := make(chan struct{}, 1)
	realDial := e.sup.dial
	e.sup.dial = func(ctx context.Context, url, token string) (*rpcclient.Client, error) {
		dialed <- struct{}{}
		return realDial(ctx, url, token)
	}
	e.onSpawn = func(e *supEnv, p *fakeProc) {} // 新进程什么也不写
	e.sup.start()
	t.Cleanup(e.sup.stop)
	e.procAt(0)
	select {
	case <-dialed:
		t.Fatal("连了上次留下的旧地址文件")
	case <-time.After(200 * time.Millisecond):
	}
	e.writeRunFile(time.Now()) // 新的写出来了
	select {
	case <-dialed:
	case <-time.After(10 * time.Second):
		t.Fatal("新的地址文件出现后该连上")
	}
	e.waitLinkUp()
}

// 地址文件要认得出是自己拉起的那个 Bun 写的（复核 R5）：刚写出来、有 url 和令牌，但 nonce 不对 / 没有 / impl 是 go 的，一概不连
func TestSupervisorOnlyConnectsToItsOwnChildsRunFile(t *testing.T) {
	t.Parallel()
	foreign := map[string]string{
		"别的进程写的（nonce 不对）":          `{"url":"ws://127.0.0.1:1/rpc","token":"编的令牌","pid":7,"impl":"bun","nonce":"别人的"}`,
		"手动起的（没有 nonce）":            `{"url":"ws://127.0.0.1:1/rpc","token":"编的令牌","pid":7,"impl":"bun"}`,
		"Go 的协议入口（impl 是 go）":       `{"url":"ws://127.0.0.1:1/rpc","token":"编的令牌","pid":7,"impl":"go"}`,
		"impl 不对，nonce 碰巧对（不可能但要拒）": `{"url":"ws://127.0.0.1:1/rpc","token":"编的令牌","pid":7,"impl":"go","nonce":"NONCE"}`,
	}
	for name, content := range foreign {
		t.Run(name, func(t *testing.T) {
			e := newSupEnv(t)
			e.sup.connectTimeout = 150 * time.Millisecond
			dialed := make(chan struct{}, 4)
			realDial := e.sup.dial
			e.sup.dial = func(ctx context.Context, url, token string) (*rpcclient.Client, error) {
				dialed <- struct{}{}
				return realDial(ctx, url, token)
			}
			e.onSpawn = func(e *supEnv, p *fakeProc) {
				e.mu.Lock()
				n := e.nonce
				e.mu.Unlock()
				dir := filepath.Join(e.data, "run")
				_ = os.MkdirAll(dir, 0o755)
				_ = os.WriteFile(filepath.Join(dir, "rpc.json"), []byte(strings.ReplaceAll(content, "NONCE", n)), 0o600)
			}
			e.sup.start()
			t.Cleanup(e.sup.stop)
			e.waitStatus("off:本地私聊的后台不在：等了")
			select {
			case <-dialed:
				t.Fatal("不是自己拉起的子进程写的地址文件，不该连")
			default:
			}
			if f, _, _ := e.link.state(); f != nil {
				t.Fatal("不该挂上连接")
			}
		})
	}
}

// 垫片：宿主拉起的进程号和写进地址文件的 pid 对不上（scoop 的 bun 垫片再启动真 bun），照样连上——认 nonce 不认 pid
func TestSupervisorConnectsWhenPidDiffersBehindShim(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t) // 默认的假 Bun 写的 pid 是 1，假子进程是 100x：对不上
	e.sup.start()
	t.Cleanup(e.sup.stop)
	e.waitLinkUp()
}

// 每次拉子进程换一个 nonce：重启后的新进程不会被上一个进程留下的地址文件骗到
func TestSupervisorUsesFreshNoncePerSpawn(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t)
	e.sup.start()
	t.Cleanup(e.sup.stop)
	e.waitLinkUp()
	first := func() string { e.mu.Lock(); defer e.mu.Unlock(); return e.nonce }()
	if len(first) < 16 {
		t.Fatalf("nonce 该是随机串：%q", first)
	}
	e.procAt(0).die(errors.New("崩了"))
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.mu.Lock()
		n, spawns := e.nonce, e.spawns
		e.mu.Unlock()
		if spawns >= 2 {
			if n == first {
				t.Fatal("第二次拉起该换一个新的 nonce")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("没有重启")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunFileIsOurs(t *testing.T) {
	t.Parallel()
	ok := func(rf rpcwire.RunFile, nonce string) bool { return runFileIsOurs(rf, nonce) }
	if !ok(rpcwire.RunFile{Impl: "bun", Nonce: "n1", PID: 5}, "n1") {
		t.Fatal("impl 和 nonce 都对该认（pid 随意）")
	}
	if !ok(rpcwire.RunFile{Nonce: "n1"}, "n1") {
		t.Fatal("impl 没写（不是必填）、nonce 对：认")
	}
	for name, rf := range map[string]rpcwire.RunFile{
		"nonce 不对": {Impl: "bun", Nonce: "n2"}, "没有 nonce": {Impl: "bun"}, "impl 是 go": {Impl: "go", Nonce: "n1"},
	} {
		if ok(rf, "n1") {
			t.Fatalf("%s：不该认", name)
		}
	}
	if ok(rpcwire.RunFile{Impl: "bun"}, "") {
		t.Fatal("我们这边没有 nonce 时不能靠“文件里也没有”认下来")
	}
}

func TestSupervisorGivesUpAtOnceWhenSpawnFails(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t)
	e.spawnEr = errors.New("拒绝访问")
	e.sup.start()
	t.Cleanup(e.sup.stop)
	got := e.waitStatus("off:本地私聊的后台不在：起不了 Bun 进程：拒绝访问")
	if spawns, delays := e.counts(); spawns != 1 || len(delays) != 0 {
		t.Fatalf("起都起不来重试没用，直接停：起了 %d 次，等待 %v", spawns, delays)
	}
	_ = got
}

func TestSupervisorStopKillsTheChild(t *testing.T) {
	t.Parallel()
	e := newSupEnv(t)
	e.sup.start()
	e.waitLinkUp()
	p := e.procAt(0)
	e.sup.stop()
	select {
	case <-p.Done():
	default:
		t.Fatal("stop 返回时子进程该已经收掉")
	}
	if !p.wasKilled() {
		t.Fatal("stop 该杀子进程")
	}
	if _, err := e.link.get(); !errors.Is(err, api.ErrLocalBackendDown) {
		t.Fatalf("stop 之后连接该是不在：%v", err)
	}
	e.sup.stop() // 可以重复停
	(&bunSupervisor{}).stop()
}

// ───── 真进程 ─────

// 假装成 Bun 的子进程：用测试程序自己，设了环境变量才进来，打一行就一直挂着
func TestBunHelperProcess(t *testing.T) {
	t.Parallel()
	if os.Getenv("SUMIKA_BUN_HELPER") != "1" {
		t.Skip("只给 spawnBun 的真进程测试当子进程用")
	}
	os.Stdout.WriteString("helper-ready\n")
	os.Stderr.WriteString("helper-stderr\n")
	select {}
}

func TestSpawnBunCapturesOutputAndDiesWithTheJob(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("作业对象只在 Windows")
	}
	var mu sync.Mutex
	var lines []string
	logf := func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		line := f
		if len(a) > 0 {
			line = strings.Replace(f, "%s", a[0].(string), 1)
		}
		lines = append(lines, line)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := spawnBun(bunSpec{Exe: exe, Args: []string{"-test.run=^TestBunHelperProcess$"}, Env: []string{"SUMIKA_BUN_HELPER=1"}, Dir: t.TempDir()}, logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Kill(); <-p.Done() })
	has := func(sub string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range lines {
			if strings.Contains(l, sub) {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(20 * time.Second)
	for !(has("helper-ready") && has("helper-stderr")) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !has("Bun：") || !has("helper-ready") || !has("helper-stderr") {
		t.Fatalf("子进程的输出该按行进日志、行首“Bun：”：%v", lines)
	}
	select {
	case <-p.Done():
		t.Fatal("子进程不该自己退出")
	default:
	}
	// sumika.exe 没了（作业对象的句柄被系统关掉）：子进程跟着没了，不靠任何人去杀它
	closeKillJobForTest()
	select {
	case <-p.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("作业对象关掉后子进程该被系统收走")
	}
}

func TestLineLogSplitsLinesAndKeepsPartial(t *testing.T) {
	t.Parallel()
	var got []string
	l := &lineLog{logf: func(f string, a ...any) { got = append(got, a[0].(string)) }}
	_, _ = l.Write([]byte("第一行\r\n第二"))
	_, _ = l.Write([]byte("行\n\n第三行\n"))
	if len(got) != 3 || got[0] != "第一行" || got[1] != "第二行" || got[2] != "第三行" {
		t.Fatalf("按行切、半行攒着、空行不记：%v", got)
	}
}

// 宿主把资源根用 -root 交给 Bun：两边的配置文件回退位置（<资源根>/sumika.yml）才是同一处（复核 R7）
func TestDefaultResolveBunPassesResourceRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "server", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "server", "src", "main.ts"), []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "bun.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec, err := defaultResolveBun("")(config.Backend{Bun: exe}, root, `D:\数据`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(spec.Args, "|")
	want := strings.Join([]string{filepath.Join(root, "server", "src", "main.ts"), "-data", `D:\数据`, "-root", root}, "|")
	if got != want {
		t.Fatalf("参数不对：\n got %s\nwant %s", got, want)
	}
}
