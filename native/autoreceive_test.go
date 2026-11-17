package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"

	backend "sumika-native/internal/app"
	"sumika-native/internal/datalock"
	"sumika-native/internal/desksettings"
)

// autoShell 是接了真宿主的外壳（没有窗口和托盘）：外壳里和自动接收有关的部分都不碰界面
func autoShell(t *testing.T, opt backend.Options) (*shell, *stateLog) {
	t.Helper()
	log := &stateLog{}
	sh := newShell(&app{}, opt, false)
	noLoginSwitch(sh)
	sh.host = newHost(opt, log.add)
	return sh, log
}

// noLoginSwitch 把开机自动启动的读写换成假的：mygo 的要等界面线程的主循环，测试里没有，会一直等下去
func noLoginSwitch(sh *shell) {
	sh.openAtLogin = func() bool { return false }
	sh.setOpenAtLogin = func(bool) error { return nil }
}

func sawPhase(l *stateLog, phase string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.all {
		if s.Phase == phase {
			return true
		}
	}
	return false
}

func settingsFileOf(opt backend.Options) string { return desksettings.New(opt.DataDir).File() }

func menuItem(t *testing.T, sh *shell, label string) *mygo.MenuItem {
	t.Helper()
	for _, it := range sh.menu().Items() {
		if it.Label == label {
			return it
		}
	}
	return nil
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("10 秒内没等到：%s", what)
}

// 默认关：没有设置文件，起来以后只看历史，不试着接收
func TestAutoReceiveOffByDefault(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	sh, log := autoShell(t, opt)
	sh.initSettings()
	if sh.autoRecv.Load() {
		t.Fatal("没有设置文件时应该是关")
	}
	sh.host.start()
	sh.maybeAutoReceive()
	if sawPhase(log, phaseSwitching) {
		t.Fatal("默认关，却试着开始接收了")
	}
	if s := log.last(); s.Phase != phaseArchive || s.Reason != "只看历史" {
		t.Fatalf("%+v", s)
	}
	if _, err := os.Stat(settingsFileOf(opt)); !os.IsNotExist(err) {
		t.Fatalf("没人改过开关，不该有设置文件：%v", err)
	}
	sh.host.shutdown()
}

// 开着：起来以后走 host.startReceive（夹具里 receive 一定开不了，回到只看历史并写明原因，正好证明确实试了）
func TestAutoReceiveOnStartsThroughHostStartReceive(t *testing.T) {
	// 真实退避合计 255 秒；这里只看“失败后回到只看历史并写明原因”，退避本身另有 retryAutoReceive 的测试
	old := autoReceiveDelays
	autoReceiveDelays = []time.Duration{time.Millisecond}
	t.Cleanup(func() { autoReceiveDelays = old })
	opt := hostFixture(t)
	first, _ := autoShell(t, opt)
	first.initSettings()
	if err := first.setAutoReceive(true); err != nil {
		t.Fatal(err)
	}
	// 下一次启动：新的外壳，从文件读
	sh, log := autoShell(t, opt)
	sh.initSettings()
	if !sh.autoRecv.Load() {
		t.Fatal("重启以后开关丢了")
	}
	sh.host.start()
	sh.maybeAutoReceive()
	if !sawPhase(log, phaseSwitching) {
		t.Fatal("开着，却没有试着开始接收")
	}
	if s := log.last(); s.Phase != phaseArchive || !strings.Contains(s.Reason, "开始接收失败") {
		t.Fatalf("夹具里 receive 开不了，应回到只看历史并写明原因：%+v", s)
	}
	sh.host.shutdown()
}

// 开着但后台没起来（数据锁被别的进程占着）：不是只看历史，就不动
func TestAutoReceiveSkippedWhenBackendBlocked(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	first, _ := autoShell(t, opt)
	first.initSettings()
	if err := first.setAutoReceive(true); err != nil {
		t.Fatal(err)
	}
	lock, err := datalock.LockData(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	sh, log := autoShell(t, opt)
	sh.initSettings()
	sh.host.start()
	if s := log.last(); s.Phase != phaseBlocked {
		t.Fatalf("夹具没造成受阻：%+v", s)
	}
	sh.maybeAutoReceive()
	if sawPhase(log, phaseSwitching) {
		t.Fatal("后台受阻时不该试着接收")
	}
	sh.host.shutdown()
}

// 不碰用户的 sumika.yml
func TestAutoReceiveDoesNotRewriteSumikaYml(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	cfg := filepath.Join(opt.DataDir, "config", "sumika.yml")
	before, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := autoShell(t, opt)
	sh.initSettings()
	for _, on := range []bool{true, false, true} {
		if err := sh.setAutoReceive(on); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.ReadFile(cfg)
	if string(before) != string(after) {
		t.Fatalf("sumika.yml 被改了：\n%s\n→\n%s", before, after)
	}
}

// 托盘菜单：勾选项和缓存一致，点一下改文件和缓存；演示数据里没有这一项
func TestTrayMenuAutoReceiveItem(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	sh, _ := autoShell(t, opt)
	if it := menuItem(t, sh, "启动后自动接收 QQ"); it == nil || it.Type != mygo.MenuItemCheckbox || it.Checked || !it.Disabled {
		t.Fatalf("设置还没读出来时应有一个没勾、点不了的勾选项：%+v", it)
	}
	sh.initSettings()
	it := menuItem(t, sh, "启动后自动接收 QQ")
	if it == nil || it.Checked || it.Disabled {
		t.Fatalf("读出来以后应能点、没勾：%+v", it)
	}
	it.Checked = true // 点击时先翻转勾选，再调 Click
	it.Click(it, nil)
	waitUntil(t, "点击后开关变成开", func() bool { return sh.autoRecv.Load() })
	if on, err := desksettings.New(opt.DataDir).AutoReceive(); !on || err != nil {
		t.Fatalf("文件里没有记下：%v %v", on, err)
	}
	if it := menuItem(t, sh, "启动后自动接收 QQ"); it == nil || !it.Checked {
		t.Fatalf("再弹出菜单应是勾着的：%+v", it)
	}

	demo := newShell(&app{}, opt, true)
	noLoginSwitch(demo)
	if it := menuItem(t, demo, "启动后自动接收 QQ"); it != nil {
		t.Fatal("演示数据不碰任何数据目录，不该有这一项")
	}
}

// 读不到配置（没有数据目录）：开关当作关、改不了，也不会 panic
func TestAutoReceiveWithoutDataDir(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "坏的配置.yml")
	if err := os.WriteFile(bad, []byte("qq: [这不是合法的\n  : : :"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUMIKA_CONFIG", bad)
	sh, _ := autoShell(t, backend.Options{Root: t.TempDir()})
	sh.initSettings()
	if sh.settings.Load() != nil || sh.autoRecv.Load() {
		t.Fatal("配置读不出来，却有了设置")
	}
	if err := sh.setAutoReceive(true); err == nil {
		t.Fatal("没有数据目录时改成功了")
	}
	sh.maybeAutoReceive()
}
