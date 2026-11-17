package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/winshell"
)

// postQueue 把 a.post 的回调攒起来，由测试线程在写盘完成后按顺序放行：界面线程的对账不和后台写盘抢。
func postQueue(a *app) (drain func()) {
	var mu sync.Mutex
	var q []func()
	a.postFn = func(f func()) {
		mu.Lock()
		q = append(q, f)
		mu.Unlock()
	}
	return func() {
		mu.Lock()
		fs := q
		q = nil
		mu.Unlock()
		for _, f := range fs {
			f()
		}
	}
}

// 分栏偏好写盘失败：不能再当成已经存上；同一份值不原样重试；用户再改一次就写得进去。
func TestShellPanesSaveFailureRollsBackAndRetriesOnChange(t *testing.T) {
	st := desksettings.New(hostFixture(t).DataDir)
	a := shellApp()
	a.desktop = st
	drain := postQueue(a)
	tt := shellTester(a, 2048, 1030)
	w := &a.workbench.shell
	step := func() {
		tt.Advance(time.Second)
		tt.Frame()
	}
	flush := func() {
		a.deskSaves.wait()
		drain()
	}

	bad := winshell.Panes{ListRatio: 5} // 越界：SetShellPanes 一定拒绝
	w.st.Panes = bad
	step()
	step()
	flush()
	if w.panesSaved == bad {
		t.Fatal("写失败了，界面还把越界的偏好当成已保存")
	}
	if !strings.Contains(a.status, "栏位偏好没写下") {
		t.Fatalf("写失败没提示：%q", a.status)
	}

	a.status = ""
	step()
	step()
	flush()
	if a.status != "" {
		t.Fatalf("同一份失败的值被原样重试了：%q", a.status)
	}

	good := winshell.Panes{ListRatio: 0.3}
	w.st.Panes = good
	step()
	step()
	flush()
	got, err := st.ShellPanes()
	if err != nil || got.ListRatio != 0.3 {
		t.Fatalf("改成合法值后没写进桌面设置：%+v %v status=%q dirty=%v", got, err, a.status, w.lookDirty)
	}
	if w.panesSaved != good || a.status != "" {
		t.Fatalf("写成功后账不对：saved=%+v status=%q", w.panesSaved, a.status)
	}
}

// 读盘回来之前用户已经动了分栏：读到的旧值不能盖掉用户的新值，用户的新值还要写下去。
func TestShellPanesLateLoadDoesNotOverwriteUserChange(t *testing.T) {
	st := desksettings.New(hostFixture(t).DataDir)
	if err := st.SetShellPanes(desksettings.ShellPanes{ListRatio: 0.4}); err != nil {
		t.Fatal(err)
	}
	a := shellApp()         // 桌面设置还没读回来：a.desktop 为空
	a.mainViewLocked = true // 读盘不改主界面开关，继续画新界面
	a.postFn = func(f func()) { f() }
	tt := shellTester(a, 2048, 1030)
	w := &a.workbench.shell
	step := func() {
		tt.Advance(time.Second)
		tt.Frame()
	}
	mine := winshell.Panes{ListRatio: 0.3, SideOpen: true}
	w.st.Panes = mine
	step()
	step()

	(&shell{a: a}).loadMainView(st) // 晚到的读盘
	if w.st.Panes != mine {
		t.Fatalf("晚到的读盘盖掉了用户刚改的分栏：%+v", w.st.Panes)
	}
	step()
	step()
	a.deskSaves.wait()
	got, err := st.ShellPanes()
	if err != nil || got.ListRatio != 0.3 || !got.SideOpen {
		t.Fatalf("用户改的分栏没写下去：%+v %v status=%q dirty=%v saved=%+v cur=%+v", got, err, a.status, w.panesDirty, w.panesSaved, w.st.Panes)
	}
}

// 没人动过分栏时，读盘读到的就是界面上的值。
func TestShellPanesLoadAppliesWhenUntouched(t *testing.T) {
	st := desksettings.New(hostFixture(t).DataDir)
	if err := st.SetShellPanes(desksettings.ShellPanes{ListRatio: 0.4, SideOpen: true}); err != nil {
		t.Fatal(err)
	}
	a := shellApp()
	a.postFn = func(f func()) { f() }
	tt := shellTester(a, 2048, 1030)
	(&shell{a: a}).loadMainView(st)
	tt.Frame()
	if got := a.workbench.shell.st.Panes; got.ListRatio != 0.4 || !got.SideOpen {
		t.Fatalf("没动过分栏，读到的值没用上：%+v", got)
	}
}

// 外观写盘失败：退回"没存上"，同一份值不原样重试，再改一次就写得进去。
func TestShellLookSaveFailureKeepsDirty(t *testing.T) {
	st := desksettings.New(hostFixture(t).DataDir)
	a := shellApp()
	a.desktop = st
	drain := postQueue(a)
	w := &a.workbench.shell
	w.look, w.lookInit = chatui.DefaultSettings(), true
	kick := false
	tt := ui.NewTester(func(c *ui.Context) {
		if kick {
			kick = false
			a.wbLookChanged(c)
		}
		a.flushWbLook(c)
	}, 400, 300)
	step := func() {
		tt.Advance(time.Second)
		tt.Frame()
	}
	flush := func() {
		a.deskSaves.wait()
		drain()
	}
	tt.Frame()

	w.look.Radius = 99 // 越界：SetLook 一定拒绝
	kick = true
	step()
	step()
	flush()
	if !w.lookDirty {
		t.Fatal("外观写失败了，界面还当成已存上")
	}
	if !strings.Contains(a.status, "外观没写下") {
		t.Fatalf("写失败没提示：%q", a.status)
	}

	a.status = ""
	step()
	step()
	flush()
	if a.status != "" {
		t.Fatalf("同一份失败的值被原样重试了：%q", a.status)
	}

	w.look.Radius = 10
	kick = true
	step()
	step()
	flush()
	got, err := st.Look()
	if err != nil || got.Radius != 10 {
		t.Fatalf("改成合法值后没写进桌面设置：%+v %v status=%q dirty=%v", got, err, a.status, w.lookDirty)
	}
	if w.lookDirty {
		t.Fatal("写成功了还当成没存上")
	}
}

// 晚到的旧写盘失败不能把更新的那次打回去；最新一次失败才退回磁盘上的值。
func TestSaveTrackIgnoresStaleFailure(t *testing.T) {
	var tr saveTrack[int]
	tr.adopt(1)
	first := tr.begin()
	second := tr.begin()
	if tr.finish(first, 2, errors.New("旧的写失败")) {
		t.Fatal("旧一次的失败被当成最新结果")
	}
	if tr.skip(3) || tr.hasFailed {
		t.Fatal("旧一次的失败留下了失败标记")
	}
	if tr.finish(second, 3, nil) || tr.disk != 3 {
		t.Fatalf("最新一次成功没记进磁盘值：%d", tr.disk)
	}
	third := tr.begin()
	if !tr.finish(third, 4, errors.New("写失败")) {
		t.Fatal("最新一次失败没报出来")
	}
	if tr.disk != 3 || !tr.skip(4) || tr.skip(5) {
		t.Fatalf("失败后账不对：disk=%d", tr.disk)
	}
	tr.rearm()
	if tr.skip(4) {
		t.Fatal("rearm 后还跳过")
	}
}
