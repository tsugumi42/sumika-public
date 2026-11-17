package main

import (
	"os"
	"testing"

	"sumika-native/internal/desksettings"
)

// 外壳读设置：上次存的分栏状态放进界面，接上存盘；写出去的能被下一次启动读到
func TestShellLoadsAndSavesPanes(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	st := desksettings.New(opt.DataDir)
	want := desksettings.Panes{ListRatio: 0.3, Compact: true, ThirdWidth: 392, ThirdOpen: false}
	if err := st.SetPanes(want); err != nil {
		t.Fatal(err)
	}
	a := &app{postFn: func(fn func()) { fn() }}
	sh := newShell(a, opt, false)
	noLoginSwitch(sh)
	sh.initSettings()
	if a.pn.preferences() != want || a.savePanes == nil {
		t.Fatalf("启动时该读到 %+v 并接上存盘：%+v 存盘接上=%v", want, a.pn.preferences(), a.savePanes != nil)
	}
	next := desksettings.Panes{ListRatio: 0.35, ThirdWidth: 300, ThirdOpen: true}
	a.savePanes(next)
	waitUntil(t, "分栏状态写进文件", func() bool { got, err := desksettings.New(opt.DataDir).Panes(); return err == nil && got == next })
}

// 设置文件里分栏那一项读坏了：按默认值起来，仍然接上存盘（下次改了会把坏的换掉）
func TestShellPanesBadFileFallsBackToDefault(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	st := desksettings.New(opt.DataDir)
	if err := st.SetPanes(desksettings.DefaultPanes()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.File(), []byte(`{"panes": {"listRatio": "宽", "thirdOpen": 3}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{postFn: func(fn func()) { fn() }}
	sh := newShell(a, opt, false)
	noLoginSwitch(sh)
	sh.initSettings()
	if a.pn.preferences() != desksettings.DefaultPanes() || a.savePanes == nil {
		t.Fatalf("读坏了该按默认值：%+v 存盘接上=%v", a.pn.preferences(), a.savePanes != nil)
	}
}
