package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/winshell"
)

type fakeWindow struct{ minimized, toggled, closed int }

func (f *fakeWindow) controls() *winshell.WindowControls {
	return &winshell.WindowControls{
		Minimize: func() { f.minimized++ },
		Toggle:   func() { f.toggled++ },
		Close:    func() { f.closed++ },
	}
}

func windowButtonsApp() (*app, *fakeWindow) {
	f := &fakeWindow{}
	a := shellApp()
	a.winControls = f.controls()
	a.sel, a.title = "local:r1", "白羽"
	return a, f
}

func overlapped(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// 三个键真的调到窗口；最大化以后那个键换成“还原”。
func TestWindowButtonsAct(t *testing.T) {
	a, f := windowButtonsApp()
	tt := shellTester(a, 2048, 1030)
	for _, label := range []string{"最小化", "最大化", "关闭"} {
		if err := tt.Click(label); err != nil {
			t.Fatalf("点不到 %s：%v", label, err)
		}
		tt.Frame()
	}
	if f.minimized != 1 || f.toggled != 1 || f.closed != 1 {
		t.Fatalf("三个键没调到窗口：%+v", f)
	}
	a.winControls.Maximized = true
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("还原"); !ok {
		t.Fatal("最大化以后没有换成“还原”")
	}
	if _, ok := tt.Find("最大化"); ok {
		t.Fatal("最大化以后还写着“最大化”")
	}
}

// 没有真窗口时不画也不占位，测试和预览的布局不受影响。
func TestWindowButtonsAbsentWithoutWindow(t *testing.T) {
	a := shellApp()
	a.sel, a.title = "local:r1", "白羽"
	tt := shellTester(a, 2048, 1030)
	if _, ok := tt.Find("最小化"); ok {
		t.Fatal("没有真窗口不该画窗口按钮")
	}
}

// 右栏开关、换页时三个键不挪地方，不然点完一下鼠标底下的键就换了。
func TestWindowButtonsStayPut(t *testing.T) {
	a, _ := windowButtonsApp()
	a.workbench.shell.st.Panes.SideOpen = false
	tt := shellTester(a, 2048, 1030)
	closed := boxOfLabel(t, tt, "关闭").Box
	windowShot(t, tt, "窗口按钮-右栏关.png")

	a.workbench.shell.st.Panes.SideOpen = true
	tt.Frame()
	tt.Frame()
	windowShot(t, tt, "窗口按钮-右栏开.png")
	if got := boxOfLabel(t, tt, "关闭").Box; got != closed {
		t.Fatalf("开右栏后关闭键挪了：%+v → %+v", closed, got)
	}
	for _, rail := range []int{2, 3, 4} {
		a.rail = rail
		tt.Frame()
		tt.Frame()
		if got := boxOfLabel(t, tt, "关闭").Box; got != closed {
			t.Fatalf("第 %d 页关闭键挪了：%+v → %+v", rail, closed, got)
		}
	}
}

// 窗口按钮不压别的东西：每种宽度、右栏开关、各页，顶行别的内容都在它外面。
func TestWindowButtonsCoverNothing(t *testing.T) {
	cases := []struct {
		name string
		w    int
		side bool
		rail int
		sel  string
		keys []string
	}{
		{"三栏右栏关", 2048, false, 0, "local:r1", []string{"chat-header"}},
		{"三栏右栏开", 2048, true, 0, "local:r1", []string{"chat-header", "side-tabs"}},
		{"两栏", 900, false, 0, "local:r1", []string{"chat-header"}},
		{"没选会话", 2048, false, 0, "", nil},
		{"设置", 2048, false, 4, "local:r1", []string{"settings-title"}},
		{"记忆", 2048, false, 2, "local:r1", []string{"page-title"}},
		{"运行", 2048, false, 3, "local:r1", []string{"page-title"}},
		{"窄窗口列表", 600, false, 0, "", []string{"search-box"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := windowButtonsApp()
			a.sel, a.rail = tc.sel, tc.rail
			a.workbench.shell.st.Panes.SideOpen = tc.side
			tt := shellTester(a, tc.w, 1030)
			var buttons []ui.Rect
			for _, label := range []string{"最小化", "最大化", "关闭"} {
				b := boxOfLabel(t, tt, label).Box
				if b.X < 0 || b.Y < 0 || b.X+b.W > float32(tc.w) {
					t.Fatalf("%s 伸出了窗口：%+v", label, b)
				}
				buttons = append(buttons, b)
			}
			for _, key := range tc.keys {
				e := boxOf(t, tt, key).Box
				for _, b := range buttons {
					if overlapped(e, b) {
						t.Fatalf("%s %+v 被窗口按钮 %+v 压着", key, e, b)
					}
				}
			}
		})
	}
}

// 右栏关着时，胶囊和聊天标题条平级：同高同位，中间一个缝，右边离聊天窗一个缝。
// 顶行的控件中线都在一条线上：logo、搜索框、标题条、胶囊。
func TestTopRowCentersLine(t *testing.T) {
	a, _ := windowButtonsApp()
	a.workbench.shell.st.Panes.SideOpen = false
	tt := shellTester(a, 2048, 1030)
	line := boxOf(t, tt, "chat-header").Box
	want := line.Y + line.H/2
	for _, key := range []string{"app-logo", "search-box", "window-capsule"} {
		b := boxOf(t, tt, key).Box
		if !closeTo(b.Y+b.H/2, want) {
			t.Errorf("%s 中线 %v，应为 %v", key, b.Y+b.H/2, want)
		}
	}
}

func TestWindowCapsuleBesideHeader(t *testing.T) {
	a, _ := windowButtonsApp()
	a.workbench.shell.st.Panes.SideOpen = false
	tt := shellTester(a, 2048, 1030)
	h, cap, chat := boxOf(t, tt, "chat-header").Box, boxOf(t, tt, "window-capsule").Box, boxOf(t, tt, "chat-local:r1").Box
	if !closeTo(h.Y, cap.Y) || !closeTo(h.H, cap.H) {
		t.Fatalf("胶囊和标题条不在一条线：%+v %+v", h, cap)
	}
	if !closeTo(cap.X-(h.X+h.W), 8) || !closeTo(chat.X+chat.W-(cap.X+cap.W), 16) {
		t.Fatalf("缝不对：标题条 %+v 胶囊 %+v 聊天窗 %+v", h, cap, chat)
	}
	if hasKey(tt, "side-tabs") {
		t.Fatal("右栏关着不该有右栏顶行")
	}
}

func windowShot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// 聊天窗挨着窗口右边时，右边两个角和输入框右下角收小，贴着窗口圆角；右栏并排时换成右栏的右边两个角。
// 聊天区、右栏都铺到窗口边，四周不留缝（ADR-326）。
func TestChatAndSideFillToWindowEdge(t *testing.T) {
	const winW, winH = float32(2048), float32(1030)
	a, _ := windowButtonsApp()
	a.workbench.shell.st.Panes.SideOpen = false
	tt := shellTester(a, int(winW), int(winH))
	tt.Frame()
	chat := boxOf(t, tt, "chat-slot").Box
	if !closeTo(chat.Y, 0) || !closeTo(chat.Y+chat.H, winH) || !closeTo(chat.X+chat.W, winW) {
		t.Fatalf("右栏关着时聊天区该铺到窗口上、下、右边：%+v", chat)
	}
	a.workbench.shell.st.Panes.SideOpen = true
	tt.Frame()
	tt.Frame()
	chat, side := boxOf(t, tt, "chat-slot").Box, boxOf(t, tt, "side-slot").Box
	if !closeTo(side.Y, 0) || !closeTo(side.Y+side.H, winH) || !closeTo(side.X+side.W, winW) {
		t.Fatalf("右栏该铺到窗口上、下、右边：%+v", side)
	}
	if !closeTo(chat.Y, 0) || !closeTo(chat.Y+chat.H, winH) {
		t.Fatalf("右栏开着时聊天区也该铺到上、下边：%+v", chat)
	}
}

// 窗口默认大小不超过屏幕，不然顶行和三个键够不着。
func TestFitWindow(t *testing.T) {
	t.Parallel()
	if w, h := fitWindow(1640, 900, 1536, 816); w != 1488 || h != 768 {
		t.Fatalf("小屏：%d×%d", w, h)
	}
	if w, h := fitWindow(1640, 900, 2048, 1030); w != 1640 || h != 900 {
		t.Fatalf("大屏不该缩：%d×%d", w, h)
	}
}
