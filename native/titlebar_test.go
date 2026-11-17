package main

import (
	"github.com/egoist/mygo/ui"
	"strings"
	"sumika-native/internal/desksettings"
	"testing"
)

var win11Bar = ui.TitleBar{Height: 32, Right: 138}

func barTester(a *app, w, h int) *ui.Tester {
	tt := ui.NewTester(a.view, w, h)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetTitleBar(win11Bar)
	tt.Frame()
	tt.Frame()
	return tt
}
func controls(w float32) ui.Rect {
	return ui.Rect{X: w - win11Bar.Right, W: win11Bar.Right, H: win11Bar.Height}
}
func overlaps(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func TestTitleBarContentClearsNativeControls(t *testing.T) {
	for _, w := range []int{380, 451, 452, 711, 712, 1003, 1004, 2048} {
		for _, third := range []bool{false, true} {
			a := withPanes(panesApp(), func(p *desksettings.Panes) { p.ThirdOpen = third })
			tt := barTester(a, w, 1030)
			for _, label := range []string{"会话列表", "聊天窗口", "右侧栏内容", "本地", "返回会话列表", labelSideFold, labelSideUnfold} {
				if r, ok := tt.Find(label); ok && (r.Y < 32 || overlaps(r, controls(float32(w)))) {
					t.Fatalf("width=%d third=%v %s %+v遮住系统按钮", w, third, label, r)
				}
			}
			if band, ok := tt.Find("窗口标题带"); !ok || band.Y != 0 || band.H != 32 || band.X+band.W > float32(w)-138 {
				t.Fatalf("标题带=%+v exists=%v", band, ok)
			}
			tt.SetTitleBar(ui.TitleBar{})
			tt.Frame()
			tt.Frame()
			if r, ok := tt.Find("窗口标题带"); ok && r.H > 0 {
				t.Fatalf("全屏仍占标题带: %+v", r)
			}
		}
	}
}

func TestCompactHeaderGeometry(t *testing.T) {
	for _, w := range []int{452, 712, 1004, 2048} {
		a := panesApp()
		tt := barTester(a, w, 1030)
		h, ok := tt.Find("聊天顶栏")
		if !ok || h.H < 54 || h.Y != 32 {
			t.Fatalf("%d: 顶栏=%+v", w, h)
		}
		for _, name := range []string{"加载更早的消息", "压测：加载到 1 万条", labelSideUnfold} {
			before, ok := tt.Find(name)
			if !ok {
				t.Fatal(name)
			}
			a.title = strings.Repeat("长标题 W", 100)
			tt.Frame()
			after, _ := tt.Find(name)
			if after != before || overlaps(after, controls(float32(w))) {
				t.Fatalf("长标题挤动按钮: %s %+v→%+v", name, before, after)
			}
			if after.W < 32 || after.H < 32 {
				t.Fatalf("点击目标过小: %+v", after)
			}
		}
	}
}

func TestTitleBarToggleKeepsControlState(t *testing.T) {
	a := withPanes(panesApp(), func(p *desksettings.Panes) { p.ThirdOpen = true })
	tt := barTester(a, 2048, 1030)
	click(t, tt, "她在想什么")
	for _, bar := range []ui.TitleBar{{}, win11Bar} {
		tt.SetTitleBar(bar)
		tt.Frame()
		if !tt.Focused("她在想什么") {
			t.Fatal("标题带变化丢失标签焦点")
		}
	}
	clickAt(t, tt, "本地")
	for _, bar := range []ui.TitleBar{{}, win11Bar} {
		tt.SetTitleBar(bar)
		tt.Frame()
		if !tt.Focused("本地") {
			t.Fatal("标题带变化丢失导航焦点")
		}
	}
}

func TestTitleBarPages(t *testing.T) {
	for rail := 2; rail <= 4; rail++ {
		a := panesApp()
		a.rail = rail
		tt := barTester(a, 2048, 1030)
		for _, text := range tt.Texts() {
			if text == "演示数据" || text == "窗口标题带" {
				continue
			}
			if r, ok := tt.Find(text); ok && r.Y < 32 {
				t.Fatalf("rail=%d %q进入标题带: %+v", rail, text, r)
			}
		}
	}
}

func TestTitleBarViewerClose(t *testing.T) {
	h := &viewHarness{imgs: vimgs([2]int{1200, 900})}
	tt := viewTester(h)
	tt.SetTitleBar(win11Bar)
	h.show(tt, 0)
	r, ok := tt.Find("关闭")
	if !ok || overlaps(r, controls(vw)) {
		t.Fatalf("查看器关闭按钮=%+v", r)
	}
	click(t, tt, "关闭")
	if h.open {
		t.Fatal("查看器未关闭")
	}
}

func TestTitleBarMemoryPageClickable(t *testing.T) {
	h := newMemHarness(t, newDemoMemory(), 2048, 1030)
	h.a.mainView = "legacy" // 系统标题栏只有旧界面有；新界面的三键自己画
	h.tt.SetTitleBar(win11Bar)
	h.frame()
	h.a.memSelect(h.a.mem.rows[0].Ref)
	h.frame()
	r, ok := h.tt.Find("出处")
	if !ok || r.Y < 32 {
		t.Fatalf("出处标签=%+v", r)
	}
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.frame()
	if h.a.mem.tab != 1 {
		t.Fatal("出处标签不可点击")
	}
}
