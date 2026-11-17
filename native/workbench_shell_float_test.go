package main

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/winshell"
)

// 列表分隔条的抓取条、拖动跟手，以及放不下三栏时浮在聊天上的右栏（PanelFloat）。
// 规格见 docs/规格-新界面主线.md 第十一节。

// 带过渡动画的测试器：shellTester 为了稳定关了动效，量过渡要开。
func shellTesterMotion(a *app, w, h int) *ui.Tester {
	tt := ui.NewTester(a.view, w, h)
	tt.SetScale(1.25)
	tt.Frame()
	tt.Frame()
	return tt
}

// listW 是会话列表这一栏实际占的宽（聊天窗的左边减去导航和 1 的分隔线）；"sidebar" 元素在窄于 260 时是被裁的整宽内容，不能用它量。
func listW(t *testing.T, tt *ui.Tester) float32 {
	t.Helper()
	return boxOf(t, tt, "chat-card").Box.X - 64 - 1
}

func cursorAt(tt *ui.Tester, x, y float32) ui.Cursor {
	tt.Move(x, y)
	tt.Frame()
	return tt.Cursor()
}

// 抓取条：以分隔线为中心至少 8 宽，悬停是 ew-resize，不占布局宽度。
func TestShellListGripGeometry(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	line := 64 + a.shellState().ListWidth // 分隔线的左边（线宽 1）
	chatBefore := boxOf(t, tt, "chat-card").Box
	list := boxOf(t, tt, "sidebar").Box
	var lo, hi float32 = 1e9, -1e9
	for dx := float32(-14); dx <= 14; dx += 0.5 {
		if cursorAt(tt, line+dx, 500) == ui.CursorResizeEW {
			lo, hi = min(lo, dx), max(hi, dx)
		}
	}
	if hi-lo < 8 {
		t.Fatalf("抓取宽度只有 %v，应至少 8", hi-lo)
	}
	if mid := (lo + hi) / 2; mid < -0.5 || mid > 1.5 {
		t.Fatalf("抓取条中心偏离分隔线 %v（范围 %v～%v）", mid, lo, hi)
	}
	// 上、中、下都能抓；离开范围是普通指针
	for _, y := range []float32{2, 20, 500, 1028} {
		if cursorAt(tt, line+0.5, y) != ui.CursorResizeEW {
			t.Errorf("y=%v 抓不到", y)
		}
	}
	if cursorAt(tt, line+30, 500) == ui.CursorResizeEW || cursorAt(tt, line-30, 500) == ui.CursorResizeEW {
		t.Error("离分隔线 30 还是 ew-resize")
	}
	if got := boxOf(t, tt, "chat-card").Box; got != chatBefore || boxOf(t, tt, "sidebar").Box != list {
		t.Errorf("抓取条改了布局：聊天 %v → %v", chatBefore, got)
	}
	t.Logf("实测：抓取范围 %v～%v，共 %v DIP（mygo 自带 6）", lo, hi, hi-lo)
}

// 离开抓取条的按下、拖动不改列表宽：列表里、聊天里选字、点按的地方都不被抢。
func TestShellListGripOnlyOnGrip(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	line := 64 + a.shellState().ListWidth
	for _, x := range []float32{line - 40, line + 40} {
		tt.Press(x, 500)
		tt.Move(x+120, 500)
		tt.Release(x+120, 500)
		tt.Frame()
		if w := a.shellState().ListWidth; !closeTo(w, 300) {
			t.Fatalf("在 x=%v 拖动改了列表宽：%v", x, w)
		}
	}
}

// 跟手：拖动中列表宽每一帧都等于指针位置；拖进头像列也跟着；松手落到头像列或展开，偏好写回。
func TestShellListDragFollowsPointer(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	step := func(x float32) float32 {
		tt.Move(x, 500)
		tt.Frame()
		return listW(t, tt)
	}
	line := 64 + a.shellState().ListWidth + 0.5
	tt.Press(line, 500)
	tt.Frame()
	// 展开 → 折叠：每步指针移动 20，列表宽跟 20
	x := line
	want := a.shellState().ListWidth
	for x > 64+90 {
		x -= 20
		want -= 20
		want = max(want, 62)
		if got := step(x); !closeTo(got, want) {
			t.Fatalf("指针 %v：列表宽 %v，应跟到 %v", x, got, want)
		}
	}
	if !hasKey(tt, "search-expand") {
		t.Fatal("拖到 130 以下内容应已换成头像列")
	}
	// 拖到头：不低于头像列宽，指针继续往左也不抖
	for _, px := range []float32{64 + 40, 64 + 10} {
		if got := step(px); !closeTo(got, 62) {
			t.Fatalf("指针 %v：列表宽 %v，应夹在头像列宽 62", px, got)
		}
	}
	// 往回拖：指针回到 62 以右就又跟上（不是从夹住的值起算）
	if got := step(64 + 200); !closeTo(got, 200) {
		t.Fatalf("从头像列往回拖：宽 %v，应跟到 200", got)
	}
	tt.Move(64+60, 500)
	tt.Release(64+60, 500)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if !a.shellState().Panes.Compact || !closeTo(listW(t, tt), 62) {
		t.Fatalf("松手在 60 应落到头像列：%+v 宽 %v", a.shellState().Panes, listW(t, tt))
	}
	// 头像列 → 展开：拖过 130 内容换回完整列表，松手落在 400
	line = 64 + 62 + 0.5
	tt.Press(line, 500)
	tt.Frame()
	for _, px := range []float32{64 + 100, 64 + 200, 64 + 400} {
		if got := step(px); !closeTo(got, px-64) {
			t.Fatalf("从头像列拖到 %v：宽 %v", px, got)
		}
	}
	if hasKey(tt, "search-expand") {
		t.Fatal("拖宽过 130 内容应换回完整列表")
	}
	tt.Release(64+400, 500)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if p := a.shellState().Panes; p.Compact || p.ListRatio <= 0 || !closeTo(listW(t, tt), 400) {
		t.Fatalf("松手在 400 应展开：%+v 宽 %v", p, listW(t, tt))
	}
	// 松手在 130～260 之间：吸到最小宽 260
	tt.Press(64+400+0.5, 500)
	tt.Frame()
	step(64 + 190)
	tt.Release(64+190, 500)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if p := a.shellState().Panes; p.Compact || !closeTo(listW(t, tt), 260) {
		t.Fatalf("松手在 190 应吸到 260：%+v 宽 %v", p, listW(t, tt))
	}
}

// 拖动时内容不跳：列表宽在 300 到 130 之间变，会话行的位置和宽度不动（只是被裁掉一部分），不重排。
func TestShellListDragContentStable(t *testing.T) {
	a := shellApp()
	tt := shellTester(a, 2048, 1030)
	row := func() ui.Rect { return boxOf(t, tt, "conversation-local:r1").Box }
	before := row()
	tt.Press(64+300+0.5, 500)
	tt.Frame()
	for _, x := range []float32{64 + 280, 64 + 240, 64 + 200, 64 + 150} {
		tt.Move(x, 500)
		tt.Frame()
		got := row()
		if got.X != before.X || got.Y != before.Y || got.H != before.H {
			t.Fatalf("指针 %v：会话行位置 %v → %v 跳了", x, before, got)
		}
		if got.W < 260-2*8-1 { // 内容保持 ListMin 宽，扣两边留的缝
			t.Fatalf("指针 %v：会话行被重排得更窄 %v", x, got)
		}
	}
	tt.Release(64+150, 500)
}

// 松手吸附、双击切换有过渡：宽度逐帧单调变化，不是一帧到位；拖动本身没有延迟。
func TestShellListSnapAnimates(t *testing.T) {
	a := shellApp()
	tt := shellTesterMotion(a, 2048, 1030)
	width := func() float32 { return listW(t, tt) }
	tt.Press(64+300+0.5, 500)
	tt.Frame()
	tt.Move(64+60, 500)
	tt.Frame()
	if w := width(); !closeTo(w, 62) {
		t.Fatalf("拖动中没跟手（有延迟）：宽 %v", w)
	}
	tt.Move(64+150, 500)
	tt.Frame()
	if w := width(); !closeTo(w, 150) {
		t.Fatalf("拖动中没跟手：宽 %v，应 150", w)
	}
	tt.Release(64+150, 500)
	tt.Frame()
	// 松手落在 150 → 展开到 260：逐帧变宽
	last := width()
	steps := 0
	for i := 0; i < 40 && !closeTo(last, 260); i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		w := width()
		if w < last-0.01 {
			t.Fatalf("吸附过程宽度倒退：%v → %v", last, w)
		}
		if w > last+0.01 {
			steps++
		}
		last = w
	}
	if !closeTo(last, 260) || steps < 4 {
		t.Fatalf("吸附没有过渡：终点 %v，变化帧数 %v", last, steps)
	}
	// 双击抓取条：收起，也有过渡
	line := 64 + last + 0.5
	tt.ClickAt(line, 500)
	tt.ClickAt(line, 500)
	tt.Frame()
	if !a.shellState().Panes.Compact {
		t.Fatalf("双击没收起：%+v", a.shellState().Panes)
	}
	if w := width(); w < 100 {
		t.Fatalf("双击收起应有过渡，第一帧就到 %v", w)
	}
	tt.Advance(time.Second)
	tt.Frame()
	if w := width(); !closeTo(w, 62) {
		t.Fatalf("双击后没落到头像列：%v", w)
	}
	t.Logf("吸附 150→260 过渡变化了 %d 帧（16ms 步）", steps)
}

// 松手落位用弹簧并带着拖动最后的速度（规格-过渡动画 铁律 1）：往左拖着松手、要落回右边的 260 时，第一步比从静止起步慢得多
// （临界阻尼弹簧带 -625/s 的初速，16 毫秒后只回了约 2；静止起步约回 7，原来的时长曲线更快）；系统要求减少动态效果时一帧到位。
func TestShellListReleaseKeepsVelocity(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		a := shellApp()
		tt := shellTesterMotion(a, 2048, 1030)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		width := func() float32 { return listW(t, tt) }
		tt.Press(64+300+0.5, 500)
		tt.Frame()
		for x := float32(290); x >= 200; x -= 10 { // 每 16 毫秒左移 10，约 625 每秒
			tt.Advance(16 * time.Millisecond)
			tt.Move(64+x, 500)
			tt.Frame()
		}
		released := width()
		if !closeTo(released, 200) {
			t.Fatalf("reduce=%v 拖动中没跟手：宽 %v，应 200", reduce, released)
		}
		tt.Release(64+200, 500)
		tt.Frame()
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		first := width() - released
		switch {
		case reduce && !closeTo(width(), 260):
			t.Errorf("减少动态效果时松手应一帧到位：宽 %v", width())
		case !reduce && (first < 0 || first > 4.5):
			t.Errorf("松手后第一步回了 %v，带着向左的速度应只回约 2（静止起步约 7）", first)
		}
		tt.Advance(time.Second)
		tt.Frame()
		if !closeTo(width(), 260) {
			t.Fatalf("reduce=%v 松手在 200 应吸到 260：宽 %v", reduce, width())
		}
	}
}

// ---------------- 浮动右栏 ----------------

func openSide(t *testing.T, tt *ui.Tester) {
	t.Helper()
	click(t, tt, "资料与活动（Ctrl+\\）")
	tt.Advance(time.Second)
	tt.Frame()
}

// 放不下三栏（705～997）：右栏是浮在聊天上的面板，四周离窗口 8，不压暗，聊天区宽度和输入框宽度不变。
func TestShellFloatPanelGeometry(t *testing.T) {
	for _, w := range []int{800, 997} {
		a := shellApp()
		tt := shellTester(a, w, 900)
		chat, comp := boxOf(t, tt, "chat-local:r1").Box, boxOf(t, tt, "composer-frame").Box
		openSide(t, tt)
		if !hasKey(tt, "float-panel") || hasKey(tt, "side-overlay") || hasKey(tt, "scrim-close") {
			t.Fatalf("宽 %d：应是浮动面板，没有遮罩", w)
		}
		pn := boxOf(t, tt, "float-panel").Box
		sh := a.shellState().SideShown
		gap := float32(8)
		if !closeTo(pn.X+pn.W, float32(w)-gap) || !closeTo(pn.Y, gap) || !closeTo(pn.Y+pn.H, 900-gap) || !closeTo(pn.W, sh) {
			t.Errorf("宽 %d：面板 %+v，应离右、上、下各 %v，宽 %v", w, pn, gap, sh)
		}
		if got := boxOf(t, tt, "chat-local:r1").Box; got != chat {
			t.Errorf("宽 %d：聊天区被挤动 %v → %v", w, chat, got)
		}
		if got := boxOf(t, tt, "composer-frame").Box; got != comp {
			t.Errorf("宽 %d：输入框被挤动 %v → %v", w, comp, got)
		}
		t.Logf("实测 宽 %d：面板 x=%.0f y=%.0f 宽 %.0f 高 %.0f；聊天区 %.0f 宽不变；输入框 %.0f 宽不变", w, pn.X, pn.Y, pn.W, pn.H, chat.W, comp.W)
	}
}

// 点面板外面收起，这一下照样落到下面（输入框拿到焦点）；Esc、Ctrl+\ 也能关。
func TestShellFloatPanelClosesAndDoesNotBlock(t *testing.T) {
	closers := []struct {
		name string
		fn   func(*testing.T, *ui.Tester)
	}{
		{"Esc", func(t *testing.T, tt *ui.Tester) { tt.Key(0, ui.KeyEscape) }},
		{"点聊天区", func(t *testing.T, tt *ui.Tester) {
			chat := boxOf(t, tt, "chat-local:r1").Box
			tt.ClickAt(chat.X+40, chat.Y+300)
		}},
		{"Ctrl+\\", func(t *testing.T, tt *ui.Tester) { tt.Key(ui.Cmd, ui.KeyBackslash) }},
	}
	for _, cl := range closers {
		a := shellApp()
		tt := shellTester(a, 900, 900)
		openSide(t, tt)
		cl.fn(t, tt)
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		if hasKey(tt, "float-panel") || a.shellState().Layer != winshell.LayerChat || a.shellState().Panes.SideOpen {
			t.Errorf("%s：没关上（层 %v，开 %v）", cl.name, a.shellState().Layer, a.shellState().Panes.SideOpen)
		}
	}
}

// 档位：2048 和 998 挤开（没有浮动面板）、997 和 800 浮动、640 整层。
func TestShellSideModesByWidth(t *testing.T) {
	for _, tc := range []struct {
		w     int
		float bool
		push  bool
	}{{2048, false, true}, {998, false, true}, {997, true, false}, {800, true, false}, {640, false, false}} {
		a := shellApp()
		a.workbench.shell.st.Panes.SideOpen = true
		a.shellState().Layer = winshell.LayerSide
		tt := shellTester(a, tc.w, 900)
		if got := hasKey(tt, "float-panel"); got != tc.float {
			t.Errorf("宽 %d：浮动面板 应为 %v", tc.w, tc.float)
		}
		if got := hasKey(tt, "side-slot"); got != tc.push {
			t.Errorf("宽 %d：并排右栏 应为 %v", tc.w, tc.push)
		}
		if tc.w == 640 && !hasKey(tt, "side") {
			t.Errorf("宽 640：第三层应显示右栏")
		}
	}
}

// 退场时内容保留到动画结束，逐帧量：关闭后面板还在、字还在，位置单调往右，结束后撤掉；进场同理。
func TestShellFloatPanelExitKeepsContent(t *testing.T) {
	a := shellApp()
	tt := shellTesterMotion(a, 900, 900)
	click(t, tt, "资料与活动（Ctrl+\\）")
	// 位移是绘制时的变换，不进布局框；量标题实际画出来的位置
	paintedX := func() float32 {
		r, ok := tt.Find("资料与活动")
		if !ok {
			t.Fatal("面板里没有标题")
		}
		return r.X
	}
	enter, lastX := 0, float32(1e9)
	for i := 0; i < 60; i++ {
		x := paintedX()
		if x > lastX+0.01 {
			t.Fatalf("进场位置倒退：%v → %v", lastX, x)
		}
		if x < lastX-0.01 {
			enter++
		}
		lastX = x
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		if i > 20 {
			break
		}
	}
	if enter < 4 {
		t.Fatalf("进场只变化了 %d 帧，没有动画", enter)
	}
	if !tt.HasText("资料与活动") {
		t.Fatal("打开后面板里没有内容")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	// 关闭后的每一帧：宿主状态已是关，面板和里面的内容照样在，位置只往右走
	exit, frames := 0, 0
	lastX = paintedX()
	for hasKey(tt, "float-panel") && frames < 60 {
		if !tt.HasText("资料与活动") {
			t.Fatalf("退场第 %d 帧内容空了", frames)
		}
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
		frames++
		if !hasKey(tt, "float-panel") {
			break
		}
		x := paintedX()
		if x < lastX-0.01 {
			t.Fatalf("退场位置倒退：%v → %v", lastX, x)
		}
		if x > lastX+0.01 {
			exit++
		}
		lastX = x
	}
	if frames >= 60 || exit < 4 {
		t.Fatalf("退场没动画或不收尾：帧 %d，变化 %d", frames, exit)
	}
	t.Logf("逐帧实测：进场 %d 帧位置变化；退场 %d 帧位置变化、内容每帧都在，共 %d 帧撤掉", enter, exit, frames)
}

// 抽屉退场比进场快（230 进 / 200 出）；减少动态效果时面板不位移，淡入淡出还在。
func TestShellFloatPanelExitFasterAndReduceMotion(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		a := shellApp()
		tt := shellTesterMotion(a, 900, 900)
		tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
		tt.Frame()
		title := func() ui.Rect {
			r, _ := tt.Find("资料与活动")
			return r
		}
		click(t, tt, "资料与活动（Ctrl+\\）")
		first := title()
		px := func() int {
			r := title()
			p := tt.Image().RGBAAt(int(r.X*1.25)-6, int((r.Y+r.H/2)*1.25))
			return int(p.R) + int(p.G) + int(p.B)
		}
		p0 := px()
		enter := 0 // 进场帧数 = 到不再要帧为止
		var pMid int
		for i := 0; i < 60 && tt.FramePending(); i++ {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
			enter++
			if i == 2 {
				pMid = px()
			}
		}
		pEnd := px()
		if reduce {
			if x := title().X; math.Abs(float64(first.X-x)) > 0.01 {
				t.Errorf("减少动态效果时抽屉还在位移：第一帧 %v 停稳 %v", first.X, x)
			}
			if !(p0 != pMid && pMid != pEnd) {
				t.Errorf("减少动态效果时抽屉淡入没了：%d → %d → %d", p0, pMid, pEnd)
			}
		}
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		exit := 0
		for hasKey(tt, "float-panel") && exit < 60 {
			tt.Advance(16 * time.Millisecond)
			tt.Frame()
			exit++
		}
		if exit >= 60 || exit >= enter {
			t.Errorf("reduce=%v 退场 %d 帧、进场 %d 帧，退场应更快（200 对 230）", reduce, exit, enter)
		}
	}
}

// 拉宽拉窄：浮动面板和并排右栏之间切换不闪、不丢状态、不重放动画。
func TestShellFloatPanelResizeSwitch(t *testing.T) {
	a := shellApp()
	tt := shellTesterMotion(a, 900, 900)
	closeX := func() float32 { r, _ := tt.Find("资料与活动"); return r.X }
	click(t, tt, "资料与活动（Ctrl+\\）")
	tt.Advance(time.Second)
	tt.Frame()
	if !hasKey(tt, "float-panel") {
		t.Fatal("900 应是浮动")
	}
	open0 := closeX()
	tt.SetSize(2048, 900)
	tt.Frame()
	if hasKey(tt, "float-panel") || !hasKey(tt, "side-slot") || !a.shellState().Panes.SideOpen {
		t.Fatalf("拉宽到 2048：浮动面板该立刻没了、右栏并排、状态保留（开 %v）", a.shellState().Panes.SideOpen)
	}
	// 拉宽后的第一帧并排右栏就在最终位置（没有滑动）
	side := boxOf(t, tt, "side-slot").Box
	if !closeTo(side.X+side.W, 2048) {
		t.Fatalf("并排右栏没贴右边：%+v", side)
	}
	tt.SetSize(900, 900)
	tt.Frame()
	if !hasKey(tt, "float-panel") {
		t.Fatal("拉窄回 900 应重新是浮动面板")
	}
	// 回到浮动：只有一次进场，不会先退场；稳定后位置在原位
	tt.Advance(time.Second)
	tt.Frame()
	if got := closeX(); !closeTo(got, open0) {
		t.Fatalf("拉窄后面板没停在原位：关闭钮 %v，原来 %v", got, open0)
	}
	// 切到窄窗口（整层）：浮动面板直接没
	tt.SetSize(640, 900)
	tt.Frame()
	if hasKey(tt, "float-panel") {
		t.Fatal("640 不该有浮动面板")
	}
}

// 窗口三个键：位置不跳、在浮动面板上面点得到；面板顶行让出它的宽；标题条宽度不变。
func TestShellFloatPanelKeepsWindowButtons(t *testing.T) {
	a := shellApp()
	var minimized int
	a.winControls = &winshell.WindowControls{Minimize: func() { minimized++ }, Toggle: func() {}, Close: func() {}}
	tt := shellTester(a, 900, 900)
	cap0 := boxOf(t, tt, "window-capsule").Box
	hdr := boxOf(t, tt, "chat-header").Box
	openSide(t, tt)
	cap1 := boxOf(t, tt, "window-capsule").Box
	if cap0 != cap1 {
		t.Fatalf("开浮动面板后三键胶囊跳了：%v → %v", cap0, cap1)
	}
	if got := boxOf(t, tt, "chat-header").Box; got != hdr {
		t.Errorf("标题条被面板改了：%v → %v", hdr, got)
	}
	click(t, tt, "最小化")
	if minimized != 1 {
		t.Fatal("浮动面板开着时最小化点不到")
	}
	head := boxOf(t, tt, "side-header").Box
	if head.X+head.W > cap1.X+0.5 {
		t.Errorf("面板顶行 %v 盖到胶囊 %v", head, cap1)
	}
	if !closeTo(head.Y, hdr.Y) || !closeTo(head.H, hdr.H) {
		t.Errorf("面板顶行 %v 和聊天标题条 %v 不在同一条线", head, hdr)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if got := boxOf(t, tt, "window-capsule").Box; got != cap0 {
		t.Fatalf("关面板后三键胶囊跳了：%v → %v", cap0, got)
	}
}

// 出图（设 LOOK_SHOT 才出）：浮动右栏浅色、深色，液态和实色；列表头像列；抓取区悬停。
func TestShotFloatAndGrip(t *testing.T) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		t.Skip("设 LOOK_SHOT 才出图")
	}
	save := func(tt *ui.Tester, name string) {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
		fmt.Println("出图", filepath.Join(dir, name))
	}
	for _, dark := range []bool{false, true} {
		for _, mat := range []string{"liquid", "solid"} {
			a := shellApp()
			a.winControls = &winshell.WindowControls{Minimize: func() {}, Toggle: func() {}, Close: func() {}}
			a.workbench.shell.material = mat
			tt := shellTester(a, 997, 760)
			if dark {
				a.look.settings.Mode = "night"
				tt.Frame()
				tt.Frame()
			}
			openSide(t, tt)
			tt.Frame()
			save(tt, fmt.Sprintf("浮动右栏-%s-%s.png", map[bool]string{false: "浅", true: "深"}[dark], mat))
		}
	}
	a := shellApp()
	tt := shellTester(a, 997, 760)
	tt.Press(64+300+0.5, 400)
	tt.Move(64+60, 400)
	tt.Frame()
	save(tt, "列表拖动中-头像列.png")
	tt.Release(64+60, 400)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	save(tt, "列表头像列-松手后.png")
	tt.Move(64+62+0.5, 400)
	tt.Frame()
	save(tt, "抓取区悬停.png")
}

// 点外面收起时这一下落到下面：点输入框，面板收起、输入框拿到焦点。没有单独的关闭钮；点面板里面、点三个键都不收起。
func TestShellFloatPanelLightDismiss(t *testing.T) {
	a, _ := windowButtonsApp()
	tt := shellTester(a, 900, 900)
	openSide(t, tt)
	if _, ok := tt.Find("关闭资料"); ok {
		t.Fatal("浮动面板不该再有关闭钮")
	}
	panel := boxOf(t, tt, "float-panel").Box
	tt.ClickAt(panel.X+60, panel.Y+400) // 面板里面
	tt.Frame()
	if !a.shellState().Panes.SideOpen {
		t.Fatal("点面板里面不该收起")
	}
	capsule := boxOf(t, tt, "window-capsule").Box
	tt.ClickAt(capsule.X+1, capsule.Y+1) // 三个键的胶囊边上
	tt.Frame()
	if !a.shellState().Panes.SideOpen {
		t.Fatal("点三个键不算点外面")
	}
	comp := boxOf(t, tt, "composer-frame").Box
	tt.ClickAt(comp.X+30, comp.Y+20)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if hasKey(tt, "float-panel") || a.shellState().Panes.SideOpen {
		t.Fatal("点外面该收起")
	}
	if !tt.Focused("发一条消息") {
		t.Fatal("这一下该落到输入框")
	}
}
