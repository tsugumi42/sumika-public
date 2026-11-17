package main

// 控制接口接进桌面程序以后的测试（规格-本机控制接口 第五节 1、4）：真宿主、真日志、真管道（带测试名的管道，不连真实的），
// 不起窗口和托盘。每个命令断言它走到了托盘用的同一批函数，退出走唯一的退出流程（beforeQuit）。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/datalock"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/toolchain"
)

func testPipeName(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("控制接口走 Windows 命名管道")
	}
	// 带进程号和计数器：只靠 time.Now（Windows 上约 0.5 毫秒分辨率）时，两个进程同时跑会取到同名，建管道报 Access is denied
	return fmt.Sprintf(`\\.\pipe\sumika-ctl-test-main-p%d-%d-%d`, os.Getpid(), time.Now().UnixNano(), ctlPipeSeq.Add(1))
}

var ctlPipeSeq atomic.Int64

type ctlEnv struct {
	opt    backend.Options
	sh     *shell
	log    *stateLog
	name   string
	posted atomic.Int32 // 回到界面线程的调用次数
}

// newCtlEnv 一个接了真宿主的外壳，管道起好了（宿主还没 start，由各个测试决定）
func newCtlEnv(t *testing.T, demo bool) *ctlEnv {
	t.Helper()
	return newCtlEnvWith(t, demo, hostFixture(t))
}

// newCtlEnvWith 同上，数据目录和配置由调用方给（要真的开始接收的测试用 logEnv）
func newCtlEnvWith(t *testing.T, demo bool, opt backend.Options) *ctlEnv {
	t.Helper()
	e := &ctlEnv{name: testPipeName(t), log: &stateLog{}}
	e.opt = opt
	ctx, cancel := context.WithCancel(context.Background())
	a := &app{ctx: ctx, cancel: cancel}
	a.postFn = func(f func()) { e.posted.Add(1); f() }
	e.sh = newShell(a, e.opt, demo)
	noLoginSwitch(e.sh)
	e.sh.ctlPipe = e.name
	if !demo {
		e.sh.host = newHost(e.opt, e.log.add)
		e.sh.initSettings()
	}
	go e.sh.startCtl()
	t.Cleanup(func() {
		e.sh.closeCtl()
		if e.sh.host != nil {
			e.sh.host.shutdown()
		}
		cancel()
	})
	e.waitListening(t)
	return e
}

func (e *ctlEnv) waitListening(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		c, err := ctlproto.Dial(ctx, e.name)
		cancel()
		if err == nil {
			c.Close() // 空连接：服务端读不到请求就丢掉
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("控制接口的管道没起来")
}

func (e *ctlEnv) ask(t *testing.T, req ctlproto.Request) ctlproto.Reply {
	t.Helper()
	return askPipe(t, e.name, req)
}

func askPipe(t *testing.T, name string, req ctlproto.Request) ctlproto.Reply {
	t.Helper()
	req.V = ctlproto.Version
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := ctlproto.Dial(ctx, name)
	if err != nil {
		t.Fatalf("连不上控制接口：%v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	if err := ctlproto.WriteJSON(c, req); err != nil {
		t.Fatal(err)
	}
	line, err := ctlproto.ReadLine(bufio.NewReader(c), 1<<20)
	if err != nil {
		t.Fatalf("读回复：%v", err)
	}
	var r ctlproto.Reply
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("回复不是 JSON：%v %s", err, line)
	}
	return r
}

func dataAs[T any](t *testing.T, r ctlproto.Reply) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.Data, &v); err != nil {
		t.Fatalf("data 解不开：%v\n%s", err, r.Data)
	}
	return v
}

func TestCtlStatus(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, false)
	e.sh.openAtLogin = func() bool { return true }
	// 宿主还没 start：阶段是 starting，也能回 status
	r := e.ask(t, ctlproto.Request{Cmd: "status"})
	if st := dataAs[ctlproto.Status](t, r); !r.OK || st.Phase != "starting" {
		t.Fatalf("宿主没 start 时：%+v", r)
	}
	e.sh.host.start()
	r = e.ask(t, ctlproto.Request{Cmd: "status"})
	st := dataAs[ctlproto.Status](t, r)
	if !r.OK || st.Mode != "archive" || st.Phase != "archive" || st.Reason != "只看历史" {
		t.Fatalf("%+v", st)
	}
	if st.Account != "***01" || strings.Contains(string(r.Data), "90001") {
		t.Fatalf("小号要打码：%s", r.Data)
	}
	if st.Alerts == nil || len(st.Alerts) != 0 || strings.Contains(string(r.Data), `"alerts":null`) {
		t.Fatalf("连接和提醒：%s", r.Data)
	}
	if st.AutoReceive || st.Autostart == nil || !*st.Autostart || st.DataDir != e.opt.DataDir || st.Pid != os.Getpid() || st.Version == "" || st.Demo {
		t.Fatalf("其余字段：%+v", st)
	}
	if st.RunningTurns != 0 || !strings.Contains(string(r.Data), `"runningTurns":0`) {
		t.Fatalf("没有回合在跑：%s", r.Data)
	}
	if st.Env == "" {
		t.Fatalf("status 要带环境概况：%+v", st)
	}
	// 和后台状态页读同一份：阶段文字 = 宿主当前的状态
	if cur := e.sh.host.current(); cur.Phase != st.Phase || cur.Reason != st.Reason {
		t.Fatalf("status 和宿主状态不一致：%+v %+v", st, cur)
	}
}

// setQuickSettle 改 receive start 不带 --wait 时多等的时间，测试结束还原
func setQuickSettle(t *testing.T, d time.Duration) {
	t.Helper()
	old := quickSettle
	quickSettle = d
	t.Cleanup(func() { quickSettle = old })
}

// receive start 走 host.startReceive（夹具里 receive 一定开不了：回到只看历史并写明原因），--wait 同
func TestCtlReceiveStartAndStopUseTheTrayFunctions(t *testing.T) {
	setQuickSettle(t, 30*time.Second) // 失败是当场的，不靠 0.2 秒的墙钟：机器忙时 0.2 秒内没做完会回“已开始”，测试就白红
	e := newCtlEnv(t, false)
	e.sh.host.start()
	r := e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"start"}})
	if r.OK || r.Code != ctlproto.CodeFailed || !strings.Contains(r.Error, "开始接收失败") || ctlproto.ExitCode(r) != 1 {
		t.Fatalf("很快就做完的失败应当场回：%+v", r)
	}
	rc := dataAs[ctlproto.Receive](t, r)
	if rc.Receiving || rc.Phase != "archive" || !rc.Settled {
		t.Fatalf("失败时 data 带当时的状态：%+v", rc)
	}
	if !sawPhase(e.log, phaseSwitching) {
		t.Fatal("没有经过 host.startReceive（状态里没有切换这一步）")
	}
	r = e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "30s"})
	if r.OK || r.Code != ctlproto.CodeFailed || !strings.Contains(r.Error, "开始接收失败") {
		t.Fatalf("--wait：%+v", r)
	}
	// stop：没在接收也是成功（目的达到了），回只看历史
	r = e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"stop"}})
	if rc := dataAs[ctlproto.Receive](t, r); !r.OK || rc.Receiving || rc.Phase != "archive" {
		t.Fatalf("stop：%+v", r)
	}
}

// 没有小号时：start 当场失败，原因是宿主写的那句
func TestCtlReceiveStartWithoutAccount(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, false)
	cfg := filepath.Join(e.opt.DataDir, "config", "sumika.yml")
	if err := os.WriteFile(cfg, []byte("qq:\n  accounts: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.sh.host.start()
	r := e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "10s"})
	if r.OK || !strings.Contains(r.Error, "不能开始接收") {
		t.Fatalf("%+v", r)
	}
}

// 打开后台进行中（占着 op 锁）：status 照样当场回，不等；receive start 不带 --wait 回"已开始"；带 --wait 超时回 timeout 并带当时的阶段
func TestCtlStatusAndStartWhileSwitching(t *testing.T) {
	setQuickSettle(t, time.Second) // 慢的那一路要等“已开始”的回复：比生产的 0.2 秒宽，给后台线程排上队留余量
	e := newCtlEnv(t, false)
	e.sh.host.start()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.sh.host.beforeOpen = func(m backend.Mode) {
		if m == backend.Receive {
			once.Do(func() { close(entered) })
			<-release
		}
	}
	r := e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"start"}})
	if rc := dataAs[ctlproto.Receive](t, r); !r.OK || rc.Settled || rc.Phase != "switching" {
		t.Fatalf("还在做的应回已开始、阶段 switching：%+v", r)
	}
	<-entered
	began := time.Now()
	r = e.ask(t, ctlproto.Request{Cmd: "status"})
	if st := dataAs[ctlproto.Status](t, r); !r.OK || st.Phase != "switching" || time.Since(began) > uiWaitLimit { // 切换卡到测试放行：等切换的改法这里过不去
		t.Fatalf("切换中 status 应当场回、阶段 switching：%+v（%v）", r, time.Since(began))
	}
	r = e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"start"}, Wait: "300ms"})
	if r.OK || r.Code != ctlproto.CodeTimeout || ctlproto.ExitCode(r) != 1 || dataAs[ctlproto.Receive](t, r).Phase != "switching" {
		t.Fatalf("--wait 到时限：%+v", r)
	}
	close(release)
	waitUntil(t, "切换结束回到只看历史", func() bool { return e.log.last().Phase == phaseArchive })
}

// 查外部程序路径很慢时也不能挡开始接收：查路径在后台做、不持 op 锁；接收后台照常走到打开这一步。
func TestCtlReceiveStartWhileToolLookupIsSlow(t *testing.T) {
	setQuickSettle(t, time.Second)
	e := newCtlEnv(t, false)
	lookupEntered, releaseLookup := make(chan struct{}), make(chan struct{})
	openEntered, releaseOpen := make(chan struct{}), make(chan struct{})
	var onceLookup, onceOpen, onceRelease sync.Once
	e.sh.host.envSnap.resolve = func(toolchain.Env) []toolchain.Result {
		onceLookup.Do(func() { close(lookupEntered) })
		<-releaseLookup
		return nil
	}
	e.sh.host.beforeOpen = func(m backend.Mode) {
		if m == backend.Receive {
			onceOpen.Do(func() { close(openEntered) })
			<-releaseOpen
		}
	}
	release := func() {
		onceRelease.Do(func() {
			close(releaseLookup)
			close(releaseOpen)
		})
	}
	t.Cleanup(release)

	e.sh.host.start()
	<-lookupEntered // 查路径已经在后台等着了
	locked := make(chan struct{})
	go func() {
		e.sh.host.op.Lock()
		e.sh.host.op.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("查外部程序路径时 op 锁还被占着，开始接收会被挡住")
	}
	r := e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"start"}})
	if rc := dataAs[ctlproto.Receive](t, r); !r.OK || rc.Settled || rc.Phase != "switching" {
		release()
		t.Fatalf("查路径不应挡住开始接收：%+v", r)
	}
	<-openEntered // 接收后台真的走到打开这一步，不是卡在 op 锁上
	release()
	waitUntil(t, "切换结束回到只看历史", func() bool { return e.log.last().Phase == phaseArchive })
}

func TestCtlWindowShowGoesToTheUIThread(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, false)
	before := e.posted.Load()
	if r := e.ask(t, ctlproto.Request{Cmd: "window", Args: []string{"show"}}); !r.OK {
		t.Fatalf("%+v", r)
	}
	if e.posted.Load() != before+1 {
		t.Fatalf("window show 应当恰好回界面线程一次，实际 %d 次", e.posted.Load()-before)
	}
}

func TestCtlLogsReadTheDeskLog(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, false)
	if r := e.ask(t, ctlproto.Request{Cmd: "logs"}); r.OK || !strings.Contains(r.Error, "没有日志文件") {
		t.Fatalf("日志没开成时：%+v", r)
	}
	e.sh.openLog()
	t.Cleanup(e.sh.closeLog) // Windows 上日志文件开着，临时目录删不掉
	e.sh.host.start()
	e.sh.logf("探针第一行")
	e.sh.logf("探针第二行")
	// logs 是只读命令，它自己不写日志，所以最后两行就是两行探针
	r := e.ask(t, ctlproto.Request{Cmd: "logs", N: 2})
	lines := dataAs[ctlproto.Logs](t, r).Lines
	if !r.OK || len(lines) != 2 || !strings.HasSuffix(lines[0], "探针第一行") || !strings.HasSuffix(lines[1], "探针第二行") {
		t.Fatalf("%+v", r)
	}
	// 会改状态的命令写一行，带命令名
	e.ask(t, ctlproto.Request{Cmd: "window", Args: []string{"show"}})
	r = e.ask(t, ctlproto.Request{Cmd: "logs", N: 1})
	if lines := dataAs[ctlproto.Logs](t, r).Lines; len(lines) != 1 || !strings.HasSuffix(lines[0], "控制接口：window show → 成功") {
		t.Fatalf("%+v", r)
	}
	r = e.ask(t, ctlproto.Request{Cmd: "logs", N: 500})
	if all := dataAs[ctlproto.Logs](t, r).Lines; len(all) < 4 || !strings.Contains(strings.Join(all, "\n"), "桌面程序启动") {
		t.Fatalf("500 行应包含启动那一行：%v", all)
	}
}

// 档位：只看历史下能列、改回 readonly；列出来的是库里真有的群
func TestCtlGateInArchive(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	p, err := paths.Open(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	qq, _ := set.Platform("qq")
	if _, err := qq.AppendEvent(store.NewEvent{Channel: "qq", Conversation: "qq:group:10001", ActorKind: "human", ActorID: "qq:1", Origin: "peer", Kind: "message", Text: "话", CreatedAt: 1_790_000_000_000}, []string{"ca"}); err != nil {
		t.Fatal(err)
	}
	set.Close()

	e := &ctlEnv{name: testPipeName(t), log: &stateLog{}, opt: opt}
	e.sh = newShell(&app{ctx: context.Background()}, opt, false)
	noLoginSwitch(e.sh)
	e.sh.ctlPipe = e.name
	e.sh.host = newHost(opt, e.log.add)
	e.sh.initSettings() // 句柄的密钥存在桌面设置文件里，要先定下数据目录
	go e.sh.startCtl()
	t.Cleanup(func() { e.sh.closeCtl(); e.sh.host.shutdown() })
	e.waitListening(t)
	e.sh.host.start()

	r := e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"list"}})
	g := dataAs[ctlproto.GateList](t, r)
	if !r.OK || len(g.Groups) != 1 || g.Groups[0].Conversation != "qq:group:***01" || !ctlproto.IsHandle(g.Groups[0].Handle) || strings.Contains(string(r.Data), "10001") || g.Groups[0].Mode != "dry" || g.Groups[0].Source != "default" || g.Groups[0].Actual != "" {
		t.Fatalf("%+v", r)
	}
	r = e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}})
	if r.OK || r.Code != ctlproto.CodeReadOnly || ctlproto.ExitCode(r) != 1 || !strings.Contains(r.Error, "开始接收") {
		t.Fatalf("只看历史下应回 readonly：%+v", r)
	}
	// speak 到不了后台
	r = e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "speak"}})
	if r.OK || r.Code != ctlproto.CodeInvalid {
		t.Fatalf("%+v", r)
	}
}

// 后台受阻（数据锁被占）：gate 回 failed，不 panic
func TestCtlGateWhenBackendBlocked(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, false)
	lock, err := datalock.LockData(e.opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	e.sh.host.start()
	r := e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"list"}})
	if r.OK || r.Code != ctlproto.CodeFailed || !strings.Contains(r.Error, "后台没在运行") {
		t.Fatalf("%+v", r)
	}
	r = e.ask(t, ctlproto.Request{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}})
	if r.OK || r.Code != ctlproto.CodeFailed {
		t.Fatalf("%+v", r)
	}
}

func TestCtlAutoReceiveAndAutostart(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, false)
	var login atomic.Int32 // 0 没动过 1 开 2 关
	e.sh.setOpenAtLogin = func(on bool) error {
		if on {
			login.Store(1)
		} else {
			login.Store(2)
		}
		return nil
	}
	for _, on := range []bool{true, false} {
		v := map[bool]string{true: "on", false: "off"}[on]
		r := e.ask(t, ctlproto.Request{Cmd: "auto-receive", Args: []string{v}})
		if !r.OK || dataAs[ctlproto.Toggle](t, r).On != on || e.sh.autoRecv.Load() != on {
			t.Fatalf("auto-receive %s：%+v", v, r)
		}
		// 改的是托盘勾选项读的那个文件
		if got, err := desksettings.New(e.opt.DataDir).AutoReceive(); got != on || err != nil {
			t.Fatalf("文件里是 %v %v", got, err)
		}
		if it := menuItem(t, e.sh, "启动后自动接收 QQ"); it == nil || it.Checked != on {
			t.Fatalf("托盘的勾选项没跟上：%+v", it)
		}
		r = e.ask(t, ctlproto.Request{Cmd: "autostart", Args: []string{v}})
		want := int32(2)
		if on {
			want = 1
		}
		if !r.OK || login.Load() != want {
			t.Fatalf("autostart %s：%+v（%d）", v, r, login.Load())
		}
	}
	// 开机自启的写失败：回 failed，带原因
	e.sh.setOpenAtLogin = func(bool) error { return fmt.Errorf("注册表写不了") }
	if r := e.ask(t, ctlproto.Request{Cmd: "autostart", Args: []string{"on"}}); r.OK || r.Error != "注册表写不了" {
		t.Fatalf("%+v", r)
	}
}

// ───────────── 验收 4：quit 走唯一的退出流程 ─────────────

func TestCtlQuitGoesThroughBeforeQuit(t *testing.T) {
	t.Parallel()
	name := testPipeName(t)
	opt := hostFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &app{ctx: ctx, cancel: cancel}
	a.postFn = func(f func()) { f() } // 测试里没有界面线程：直接在调用的线程里做
	sh := newShell(a, opt, false)
	noLoginSwitch(sh)
	sh.ctlPipe = name

	var mu sync.Mutex
	var order []string
	note := func(format string, args ...any) {
		mu.Lock()
		order = append(order, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	// 宿主每次发布状态都记一笔；收到"正在退出"那一刻试着连一下管道：此时管道必须已经关了
	sh.host = newHost(opt, func(st hostState) {
		if st.Phase == phaseStopping {
			// 只有"管道不存在"才算已关；别的错误（比如负载下拨号超时）说明没法判断，记成状态不明让测试红，
			// 不当成已关放过——不然管道其实还开着、只是拨号慢，回归就漏了
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, err := ctlproto.Dial(ctx, name)
			switch {
			case err == nil:
				c.Close()
				note("宿主开始收尾时管道还开着")
			case errors.Is(err, os.ErrNotExist):
				note("宿主开始收尾时管道已关")
			default:
				note("宿主开始收尾时管道状态不明：%v", err)
			}
		}
		note("宿主:%s", st.Phase)
	})
	var nQuit atomic.Int32
	finished := make(chan bool, 1) // 第二次 beforeQuit 有没有被拦
	sh.quitApp = func() {
		n := nQuit.Add(1)
		note("quitApp#%d", n)
		if n == 2 {
			note("收尾完时 ctx 已取消=%v", a.ctx.Err() != nil)
		}
		ev := &mygo.QuitEvent{}
		go func() { // mygo.App.Quit 回到界面线程、触发 OnBeforeQuit：测试里用一个 goroutine 代替
			sh.beforeQuit(ev)
			if n == 2 {
				finished <- ev.DefaultPrevented()
			}
		}()
	}
	sh.initSettings()
	go sh.startCtl()
	e := &ctlEnv{name: name}
	e.waitListening(t)
	sh.host.start()

	r := askPipe(t, name, ctlproto.Request{Cmd: "quit"})
	if !r.OK || !dataAs[ctlproto.Quitting](t, r).Quitting {
		t.Fatalf("客户端应拿到「正在退出」：%+v", r)
	}
	select {
	case prevented := <-finished:
		if prevented {
			t.Fatal("收尾完以后的那一次 beforeQuit 不该再拦")
		}
	case <-time.After(30 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("30 秒内没走完退出流程：%v", order)
	}
	mu.Lock()
	got := strings.Join(order, " → ")
	mu.Unlock()
	const want = "宿主:starting → 宿主:archive → quitApp#1 → 宿主开始收尾时管道已关 → 宿主:stopping → 宿主:stopped → quitApp#2 → 收尾完时 ctx 已取消=true"
	if got != want {
		t.Fatalf("退出的先后不对：\n得到 %s\n应是 %s", got, want)
	}
	if sh.quit != quitStopped {
		t.Fatalf("外壳的退出状态是 %d", sh.quit)
	}
	if nQuit.Load() != 2 {
		t.Fatalf("quitApp 被调了 %d 次，应是 2 次（命令一次，收尾完最后一次）", nQuit.Load())
	}
	// 管道关了、数据锁放了、同一组还能再开
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if c, err := ctlproto.Dial(ctx2, name); err == nil {
		c.Close()
		t.Fatal("退出以后还连得上管道")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("退出以后拨管道的结果不是“管道不存在”，没法判断它关了没有：%v", err)
	}
	lock, err := datalock.LockData(opt.DataDir)
	if err != nil {
		t.Fatalf("退出后数据锁没放：%v", err)
	}
	lock.Close()
}

// 退出流程已经走过第一步以后才轮到开管道（启动和退出赶在一起）：不开了，也不泄漏监听
func TestCtlNotStartedAfterQuitBegan(t *testing.T) {
	t.Parallel()
	name := testPipeName(t)
	sh := newShell(&app{}, backend.Options{}, true)
	sh.ctlPipe = name
	sh.closeCtl()
	done := make(chan struct{})
	go func() { sh.startCtl(); close(done) }()
	select {
	case <-done:
	case <-time.After(uiWaitLimit):
		t.Fatal("退出以后 startCtl 还在收连接")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if c, err := ctlproto.Dial(ctx, name); err == nil {
		c.Close()
		t.Fatal("退出开始后管道还是开了")
	}
}

// 演示数据模式：也开管道，receive、gate、auto-receive 回 demo，status 说明是演示
func TestCtlDemoMode(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, true)
	r := e.ask(t, ctlproto.Request{Cmd: "status"})
	if st := dataAs[ctlproto.Status](t, r); !r.OK || !st.Demo || st.Phase != "demo" {
		t.Fatalf("%+v", r)
	}
	for _, req := range []ctlproto.Request{
		{Cmd: "receive", Args: []string{"start"}},
		{Cmd: "gate", Args: []string{"list"}},
		{Cmd: "gate", Args: []string{"set", "qq:group:10001", "off"}},
		{Cmd: "auto-receive", Args: []string{"on"}},
	} {
		if r := e.ask(t, req); r.OK || r.Code != ctlproto.CodeDemo {
			t.Errorf("%+v：%+v", req, r)
		}
	}
	if r := e.ask(t, ctlproto.Request{Cmd: "logs"}); r.OK {
		t.Errorf("演示没有日志文件：%+v", r)
	}
}

// 界面线程卡住超过时限：autostart 不能说"没改成"。修改已经排进界面线程，缓过来以后照样会写；
// 说没改成，Agent 会据此重试或放弃，和实际状态对不上
func TestCtlAutostartTimeoutDoesNotClaimItFailed(t *testing.T) {
	old := uiCallLimit
	uiCallLimit = 50 * time.Millisecond
	t.Cleanup(func() { uiCallLimit = old })
	e := newCtlEnv(t, false)
	release := make(chan struct{})
	var applied atomic.Bool
	e.sh.setOpenAtLogin = func(on bool) error {
		<-release // 界面线程卡住
		applied.Store(on)
		return nil
	}
	r := e.ask(t, ctlproto.Request{Cmd: "autostart", Args: []string{"on"}})
	if r.OK || r.Code != ctlproto.CodeFailed || ctlproto.ExitCode(r) != 1 {
		t.Fatalf("超时应回 failed：%+v", r)
	}
	if strings.Contains(r.Error, "没改成") || !strings.Contains(r.Error, "不确定") || !strings.Contains(r.Error, "status") {
		t.Fatalf("超时的说法：%q", r.Error)
	}
	close(release)
	waitUntil(t, "界面线程缓过来以后修改照样生效", applied.Load)
}

// 后台没起来（数据锁被占，受阻）时 receive stop：本来就没在接收，算成功；阶段和原因照实给。
// 刚停完却回不到只看历史才算失败（那一格要让接收中的后台关掉以后打不开只读库，夹具造不出，靠代码走查）
func TestCtlReceiveStopWhenBackendWasAlreadyDown(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t, false)
	lock, err := datalock.LockData(e.opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	e.sh.host.start()
	if cur := e.sh.host.current(); cur.Phase != phaseBlocked {
		t.Fatalf("夹具没有造出受阻：%+v", cur)
	}
	r := e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"stop"}})
	rc := dataAs[ctlproto.Receive](t, r)
	if !r.OK || ctlproto.ExitCode(r) != 0 || rc.Receiving || rc.Phase != "blocked" || !rc.Settled || rc.Reason == "" {
		t.Fatalf("本来就没在接收，stop 应算成功并带着受阻的阶段：%+v %+v", r, rc)
	}
	if e.sh.host.current().Phase != phaseBlocked {
		t.Fatalf("stop 不该改变受阻的状态：%+v", e.sh.host.current())
	}
}

// 本地可用阶段 receive stop：不动后台，回复就是宿主那句，退出码照“没什么可停”算成功。
func TestCtlReceiveStopInLocalPhaseKeepsBackend(t *testing.T) {
	t.Parallel()
	e := newCtlEnvWith(t, false, phaseLocalOptions(t, true))
	e.sh.host.start()
	if cur := e.sh.host.current(); cur.Phase != phaseLocal {
		t.Fatalf("夹具没进本地可用：%+v", cur)
	}
	r := e.ask(t, ctlproto.Request{Cmd: "receive", Args: []string{"stop"}})
	rc := dataAs[ctlproto.Receive](t, r)
	if !r.OK || ctlproto.ExitCode(r) != 0 || rc.Receiving || rc.Phase != "local" || !rc.Settled || rc.Reason != localStopNote {
		t.Fatalf("本地可用时 stop：%+v %+v", r, rc)
	}
	if cur := e.sh.host.current(); cur.Phase != phaseLocal || e.sh.host.cur == nil || !e.sh.host.localOnly {
		t.Fatalf("stop 不该拆掉本地后台：%+v", cur)
	}
}

// 管道名被别的进程占了（抢注）：控制接口开不成，不能只悄悄写一行日志；状态区要有一句，主人看得到
func TestCtlPipeTakenShowsInStatusArea(t *testing.T) {
	t.Parallel()
	name := testPipeName(t)
	squatter, err := ctlserver.ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	a := &app{ctx: context.Background()}
	a.postFn = func(f func()) { f() }
	sh := newShell(a, hostFixture(t), false)
	noLoginSwitch(sh)
	sh.ctlPipe = name
	sh.startCtl() // 开不成就直接返回
	if !strings.Contains(a.status, "本机控制接口没开成") || strings.Contains(a.status, "S-1-5") || strings.Contains(a.status, "pipe") {
		t.Fatalf("状态区：%q", a.status)
	}
	if sh.ctl != nil {
		t.Fatal("没开成就不该有服务端")
	}
}
