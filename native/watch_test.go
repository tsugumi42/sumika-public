package main

// 看门狗在桌面程序一侧的接线测试（docs/规格-看门狗.md）：命令行、数据目录、配置、心跳、状态、开机自启迁移。
// 守护进程本身的行为在 internal/watchdog 的测试里；这里不起守护进程，不碰真实的 Run 项、真实的管道名。

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/watchdog"
)

func TestWatchChildArgsDropsWatchFlag(t *testing.T) {
	cases := []struct{ in, want []string }{
		{nil, []string{}},
		{[]string{"-watch"}, []string{}},
		{[]string{"--watch"}, []string{}},
		{[]string{"-watch=true", "-data", `D:\数据`}, []string{"-data", `D:\数据`}},
		{[]string{"-data", "-watch"}, []string{"-data"}}, // 只管去掉 -watch 本身；值是不是 -watch 轮不到这里判断
		{[]string{"-root", "x", "-account", "90001"}, []string{"-root", "x", "-account", "90001"}},
		{[]string{"watch", "---watch", "-watching"}, []string{"watch", "---watch", "-watching"}}, // 不是这个参数的不动
	}
	for _, c := range cases {
		if got := watchChildArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v → %v，想要 %v", c.in, got, c.want)
		}
	}
}

func TestWatchLockNameIsPerDataDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		// 本机写法的路径：不同目录不同锁，结尾斜杠不影响。大小写不同算不算同一个目录是 Windows 盘的语义（watchLockName 一律转小写），
		// 这一条只在 Windows 上断言
		a, b := watchLockName("/a/data"), watchLockName("/b/data")
		if a == b || a != watchLockName("/a/data/") {
			t.Fatalf("%s %s", a, b)
		}
		return
	}
	a, b := watchLockName(`D:\a\data`), watchLockName(`D:\b\data`)
	if a == b || a != watchLockName(`d:\A\data\`) { // 大小写、末尾斜杠不影响
		t.Fatalf("%s %s", a, b)
	}
}

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sumika.yml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUMIKA_CONFIG", p)
	return p
}

// 配置读得出：守护进程和桌面程序用同一个数据目录，时限取配置里的值
func TestWatchDataDirAndParamsFromConfig(t *testing.T) {
	writeConfig(t, "watchdog:\n  stuckMinutes: 9\n  crashLimit: 5\n  notify: false\n")
	opt := backend.Options{Root: t.TempDir(), DataDir: filepath.Join(t.TempDir(), "d")}
	dir, fallback, err := watchDataDir(opt)
	if err != nil || fallback || dir != opt.DataDir {
		t.Fatalf("%q %v %v", dir, fallback, err)
	}
	p := watchParams(opt, t.Logf)
	if p.Stuck != 9*time.Minute || p.CrashLimit != 5 || p.Notify || p.StartGrace != 3*time.Minute {
		t.Fatalf("%+v", p)
	}
}

// 配置读不了：数据目录退到用户缓存目录，时限用默认值——桌面程序照样要能被拉起
func TestWatchFallsBackOnBrokenConfig(t *testing.T) {
	writeConfig(t, "watchdog:\n  stuckMinutes: [不是数字\n")
	opt := backend.Options{Root: t.TempDir(), DataDir: filepath.Join(t.TempDir(), "d")}
	dir, fallback, err := watchDataDir(opt)
	if err != nil || !fallback || !strings.HasSuffix(filepath.ToSlash(dir), "Sumika/watchdog") {
		t.Fatalf("%q %v %v", dir, fallback, err)
	}
	var logged []string
	p := watchParams(opt, func(f string, a ...any) { logged = append(logged, f) })
	if p.Stuck != 5*time.Minute || p.CrashLimit != 3 || !p.Notify || len(logged) == 0 {
		t.Fatalf("%+v %v", p, logged)
	}
}

func newBeatShell(t *testing.T) (*shell, string) {
	t.Helper()
	opt := hostFixture(t)
	e := newCtlEnvWith(t, false, opt)
	return e.sh, opt.DataDir
}

func TestStartHeartbeatWritesAndMarksQuitting(t *testing.T) {
	old := heartbeatEnabled
	heartbeatEnabled = true
	t.Cleanup(func() { heartbeatEnabled = old })
	sh, dir := newBeatShell(t)
	sh.startHeartbeat()
	if sh.beat.Load() == nil {
		t.Fatal("心跳没开")
	}
	var hb watchdog.Heartbeat
	waitUntil(t, "第一次心跳", func() bool {
		var err error
		hb, err = watchdog.ReadHeartbeat(dir)
		return err == nil
	})
	if hb.Pid != os.Getpid() || hb.Phase == "" || hb.Version == "" || hb.Quitting {
		t.Fatalf("%+v", hb)
	}
	sh.markQuitting()
	if hb, _ = watchdog.ReadHeartbeat(dir); !hb.Quitting {
		t.Fatalf("退出标记没写进去：%+v", hb)
	}
}

func TestHeartbeatOffInDemoRemoteAndTests(t *testing.T) {
	old := heartbeatEnabled
	t.Cleanup(func() { heartbeatEnabled = old })
	sh, dir := newBeatShell(t)

	heartbeatEnabled = false // 测试里默认就是这样：只有真正的入口 runMain 才打开
	sh.startHeartbeat()
	heartbeatEnabled = true
	sh.remote = "auto"
	sh.startHeartbeat()
	sh.remote, sh.demo = "", true
	sh.startHeartbeat()
	sh.demo = false
	if sh.beat.Load() != nil {
		t.Fatal("这些场合不该写心跳")
	}
	if _, err := os.Stat(filepath.Join(dir, watchdog.RunDir, watchdog.HeartbeatFile)); err == nil {
		t.Fatal("不该有心跳文件")
	}
	sh.markQuitting() // 没开心跳时退出流程照走，不 panic
}

func TestWatchdogInfo(t *testing.T) {
	sh, dir := newBeatShell(t)
	if info := sh.watchdogInfo(); info.Running || info.Supervised || info.LastIncidentAt != "" {
		t.Fatalf("没有状态文件：%+v", info)
	}
	at := time.Date(2026, 11, 13, 19, 37, 0, 0, time.UTC)
	write := func(st watchdog.State) {
		t.Helper()
		if err := watchdog.WriteState(dir, st); err != nil {
			t.Fatal(err)
		}
	}
	inc := &watchdog.IncidentRef{Time: at, Reason: "进程退出，退出码 2", ExitCode: 2, File: "x.md"}
	write(watchdog.State{Pid: os.Getpid(), State: watchdog.StateRunning, UpdatedAt: time.Now(), Restarts: 2, LastIncident: inc})
	info := sh.watchdogInfo()
	if !info.Running || info.Pid != os.Getpid() || info.Restarts != 2 || info.LastReason != "进程退出，退出码 2" || info.LastFile != "x.md" {
		t.Fatalf("%+v", info)
	}
	if got, err := time.Parse(time.RFC3339, info.LastIncidentAt); err != nil || !got.Equal(at) {
		t.Fatalf("时间 %q %v", info.LastIncidentAt, err)
	}
	write(watchdog.State{Pid: os.Getpid(), State: watchdog.StateRunning, UpdatedAt: time.Now().Add(-time.Hour), LastIncident: inc})
	if info := sh.watchdogInfo(); info.Running || info.Pid != 0 || info.LastFile != "x.md" {
		t.Fatalf("状态文件太旧不能算在：%+v", info)
	}
	write(watchdog.State{Pid: os.Getpid(), State: watchdog.StateGaveUp, UpdatedAt: time.Now()})
	if info := sh.watchdogInfo(); info.Running || info.State != watchdog.StateGaveUp {
		t.Fatalf("放弃了不算在：%+v", info)
	}
}

// sumika-ctl status 的回复里带守护进程那一栏（真管道，带测试名）
func TestCtlStatusIncludesWatchdog(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("控制接口走 Windows 命名管道")
	}
	opt := hostFixture(t)
	e := newCtlEnvWith(t, false, opt)
	r := e.ask(t, ctlproto.Request{Cmd: "status"})
	st := dataAs[ctlproto.Status](t, r)
	if !r.OK || st.Watchdog == nil || st.Watchdog.Running {
		t.Fatalf("没有守护进程时：%s", r.Data)
	}
	if err := watchdog.WriteState(opt.DataDir, watchdog.State{Pid: os.Getpid(), State: watchdog.StateRunning, UpdatedAt: time.Now(), Restarts: 1}); err != nil {
		t.Fatal(err)
	}
	st = dataAs[ctlproto.Status](t, e.ask(t, ctlproto.Request{Cmd: "status"}))
	if st.Watchdog == nil || !st.Watchdog.Running || st.Watchdog.Restarts != 1 {
		t.Fatalf("%+v", st.Watchdog)
	}
	// 演示数据没有守护进程这一栏
	d := newCtlEnv(t, true)
	if ds := dataAs[ctlproto.Status](t, d.ask(t, ctlproto.Request{Cmd: "status"})); ds.Watchdog != nil {
		t.Fatalf("演示数据不该有：%+v", ds.Watchdog)
	}
}

type memRun struct {
	vals     map[string]string
	disabled map[string]bool
}

func (m *memRun) Get(n string) (string, bool) { v, ok := m.vals[n]; return v, ok }
func (m *memRun) Set(n, v string) error       { m.vals[n] = v; return nil }
func (m *memRun) Delete(n string) error       { delete(m.vals, n); return nil }
func (m *memRun) Disabled(n string) bool      { return m.disabled[n] }
func (m *memRun) ClearDisabled(n string) error {
	delete(m.disabled, n)
	return nil
}

// 开机自启还是 mygo 以前的写法就改成守护进程的写法；托盘和 sumika-ctl 用的开关函数读写的是同一项
func TestMigrateAutostartAndToggleFunctions(t *testing.T) {
	sh, _ := newBeatShell(t)
	sh.migrateAutostart() // 没接上（演示、测试）就什么也不做，不 panic

	exe := `D:\x\sumika.exe`
	mem := &memRun{vals: map[string]string{"Sumika": `"` + exe + `" --mygo-opened-at-login`}, disabled: map[string]bool{}}
	a := watchdog.Autostart{Store: mem, Name: "Sumika", Exe: exe}
	sh.autostart, sh.openAtLogin, sh.setOpenAtLogin = &a, a.On, a.Set
	if !sh.openAtLogin() {
		t.Fatal("旧写法应当读成开着")
	}
	sh.migrateAutostart()
	if got := mem.vals["Sumika"]; got != `"`+exe+`" -watch` || !sh.openAtLogin() {
		t.Fatalf("迁移后 %q", got)
	}
	if err := sh.setOpenAtLogin(false); err != nil || sh.openAtLogin() || len(mem.vals) != 0 {
		t.Fatalf("关：%v %v", err, mem.vals)
	}
	if err := sh.setOpenAtLogin(true); err != nil || mem.vals["Sumika"] != `"`+exe+`" -watch` {
		t.Fatalf("开：%v %v", err, mem.vals)
	}
}

func TestRunningTurnsOf(t *testing.T) {
	if runningTurnsOf(hostState{}) != 0 {
		t.Fatal("没有后台是 0")
	}
}
