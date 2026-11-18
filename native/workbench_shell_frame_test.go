package main

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/winshell"
)

// 新界面外壳的交互测试：用 ui.NewTester 真点、真按、真拖，不靠读代码判定（对照笔记第八节通用口径）。

func boxOf(t *testing.T, tt *ui.Tester, key string) uiBox {
	t.Helper()
	for _, b := range uiBoxes(tt) {
		if b.Key == key {
			return b
		}
	}
	t.Fatalf("找不到元素 %s", key)
	return uiBox{}
}

func hasKey(tt *ui.Tester, key string) bool {
	for _, b := range uiBoxes(tt) {
		if b.Key == key {
			return true
		}
	}
	return false
}

func closeTo(a, b float32) bool { return math.Abs(float64(a-b)) <= 1 }

func (a *app) shellState() *winshell.State { return &a.workbench.shell.st }

// A1：导航、搜索行的顶行同高、都在最上面；聊天标题条和右栏顶行同一条线。
func TestShellTopRowsAligned(t *testing.T) {
	a := shellAppFlat()
	a.workbench.shell.st.Panes.SideOpen = true
	tt := shellTester(a, 2048, 1030)
	for _, key := range []string{"nav-header", "search-row"} {
		b := boxOf(t, tt, key)
		if !closeTo(b.Box.H, 48) || !closeTo(b.Box.Y, 16) {
			t.Errorf("%s：y=%v 高=%v，应为 16 和 48", key, b.Box.Y, b.Box.H)
		}
	}
	// 聊天窗和右栏是浮动卡，上面留一个 8 的缝，两边的顶行都再离卡顶 8
	if b := boxOf(t, tt, "side-header"); !closeTo(b.Box.H, 48) || !closeTo(b.Box.Y, 16) {
		t.Errorf("右栏顶行：y=%v 高=%v，应为 16 和 48", b.Box.Y, b.Box.H)
	}
	if b := boxOf(t, tt, "chat-header"); !closeTo(b.Box.H, 48) || !closeTo(b.Box.Y, 16) {
		t.Errorf("标题栏浮条：y=%v 高=%v，应为 16 和 48", b.Box.Y, b.Box.H)
	}
}

// A2：三栏宽度：导航 64、列表默认 300、右栏占 320、聊天拿剩下的；两个分隔条各占 1。
func TestShellColumnWidths(t *testing.T) {
	a := shellAppFlat()
	a.workbench.shell.st.Panes.SideOpen = true
	tt := shellTester(a, 2048, 1030)
	nav, list, side := boxOf(t, tt, "navigation"), boxOf(t, tt, "sidebar"), boxOf(t, tt, "side-slot")
	if !closeTo(nav.Box.W, 64) || !closeTo(list.Box.W, 300) || !closeTo(side.Box.W, 320) {
		t.Fatalf("导航 %v 列表 %v 右栏 %v，应为 64 / 300 / 320", nav.Box.W, list.Box.W, side.Box.W)
	}
	chat := boxOf(t, tt, "chat-local:r1")
	if want := float32(2048 - 64 - 300 - 320 - 2); !closeTo(chat.Box.W, want) {
		t.Fatalf("聊天宽 %v，应为 %v", chat.Box.W, want)
	}
}

// 真实的鼠标是一小步一小步移动的，每步都要算数：一路拖过下限收成头像列，再一路拖回来展开，
// 展开回到收起前的宽（主人 2026-11-09：拖不动，最少只能到 260）。
func TestShellDragListInSmallSteps(t *testing.T) {
	a := shellAppFlat()
	tt := shellTester(a, 2048, 1030)
	const nav = float32(64)
	st := func() (float32, bool) { return a.shellState().ListWidth, a.shellState().Panes.Compact }
	drag := func(from, to float32) {
		t.Helper()
		step := float32(4)
		if to < from {
			step = -4
		}
		tt.Press(from, 500)
		for x := from + step; (step < 0 && x > to) || (step > 0 && x < to); x += step {
			tt.Move(x, 500)
			tt.Frame()
		}
		tt.Move(to, 500)
		tt.Frame()
		tt.Release(to, 500)
		tt.Advance(time.Second)
		tt.Frame()
	}
	before, _ := st()
	drag(nav+before+0.5, nav+350)
	if w, compact := st(); !closeTo(w, 350) || compact {
		t.Fatalf("小步拖到 350：宽 %v 折叠 %v", w, compact)
	}
	drag(nav+350+0.5, nav+200) // 还没过门槛：停在下限，不折叠
	if w, compact := st(); !closeTo(w, 260) || compact {
		t.Fatalf("拖到 200 该停在下限 260：宽 %v 折叠 %v", w, compact)
	}
	drag(nav+260+0.5, nav+60)
	if w, compact := st(); !compact || !closeTo(w, a.workbench.shell.kit.G.ListCompactWidth) {
		t.Fatalf("小步拖过门槛该收成头像列：宽 %v 折叠 %v", w, compact)
	}
	narrow, _ := st()
	drag(nav+narrow+0.5, nav+400)
	if w, compact := st(); compact || !closeTo(w, 400) {
		t.Fatalf("从头像列小步拖出来该展开到指针处：宽 %v 折叠 %v", w, compact)
	}
}

// 右栏分隔条同样按绝对位置：拖过下限再拖回来，宽度要等指针回到分隔线才开始变，不能漂移。
func TestShellDragSideInSmallSteps(t *testing.T) {
	a := shellAppFlat()
	a.workbench.shell.st.Panes.SideOpen = true
	tt := shellTester(a, 2048, 1030)
	side := boxOf(t, tt, "side-slot").Box
	start := side.X - 0.5
	tt.Press(start, 500)
	for x := start; x < start+200; x += 4 { // 往右拖：右栏变窄，超过下限 292
		tt.Move(x, 500)
		tt.Frame()
	}
	if w := boxOf(t, tt, "side-slot").Box.W; !closeTo(w, 292) {
		t.Fatalf("拖过下限该停在 292：%v", w)
	}
	for x := start + 200; x > 2048-340; x -= 4 { // 拖回来，到右栏约 340 宽的位置
		tt.Move(x, 500)
		tt.Frame()
	}
	tt.Release(2048-340, 500)
	tt.Advance(time.Second)
	tt.Frame()
	if w := boxOf(t, tt, "side-slot").Box.W; w < 335 || w > 345 {
		t.Fatalf("拖回来右栏宽该跟着指针，约 340：%v", w)
	}
}

// A2：拖列表分隔条改宽；拖到 130 以下收成头像列，再拖宽恢复；改动写回桌面设置的 panes。
func TestShellDragListDivider(t *testing.T) {
	a := shellAppFlat()
	tt := shellTester(a, 2048, 1030)
	x := 64 + a.shellState().ListWidth + 0.5
	drag := func(from, to float32) {
		tt.Press(from, 500)
		tt.Move(to, 500)
		tt.Release(to, 500)
		tt.Advance(time.Second)
		tt.Frame()
	}
	drag(x, 64+400)
	if w := a.shellState().ListWidth; !closeTo(w, 400) {
		t.Fatalf("拖到 400 后列表宽 %v，应为 400", w)
	}
	// 拖出上限（540）的部分不要
	drag(64+400+0.5, 64+900)
	if w := a.shellState().ListWidth; !closeTo(w, 540) {
		t.Fatalf("拖过上限后列表宽 %v，应夹在 540", w)
	}
	drag(64+540+0.5, 64+400)
	if got := a.shellState().Panes; got.ListRatio <= 0.15 || got.ListRatio >= 0.25 || got.Compact { // 400 / (2048 − 64)
		t.Fatalf("拖动没写回分栏偏好：%+v", got)
	}
	// 拖到很窄：收成头像列
	x = 64 + a.shellState().ListWidth + 0.5
	drag(x, 64+60)
	if !a.shellState().Panes.Compact || !closeTo(boxOf(t, tt, "sidebar").Box.W, 62) {
		t.Fatalf("没收成头像列：%+v 宽 %v", a.shellState().Panes, boxOf(t, tt, "sidebar").Box.W)
	}
	if !a.shellState().Panes.Compact {
		t.Fatal("头像列没写回分栏偏好")
	}
	// 从头像列拖宽过 130：恢复
	drag(64+62+0.5, 64+320)
	if a.shellState().Panes.Compact {
		t.Fatal("从头像列拖宽后没恢复")
	}
}

// Ctrl+B 收起、展开会话列表（头像列）；Ctrl+\ 开合右栏。快捷键走新外壳自己的状态。
func TestShellShortcuts(t *testing.T) {
	a := shellAppFlat()
	tt := shellTester(a, 2048, 1030)
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Frame()
	if !a.shellState().Panes.Compact || !closeTo(boxOf(t, tt, "sidebar").Box.W, 62) {
		t.Fatalf("Ctrl+B 没收起列表：%+v", a.shellState().Panes)
	}
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Frame()
	if a.shellState().Panes.Compact {
		t.Fatal("Ctrl+B 再按没展开")
	}
	if hasKey(tt, "side") {
		t.Fatal("右栏默认不该开着")
	}
	tt.Key(ui.Cmd, ui.KeyBackslash)
	tt.Frame()
	if !hasKey(tt, "side") || !a.shellState().Panes.SideOpen {
		t.Fatal("Ctrl+\\ 没开右栏")
	}
	if !a.shellState().Panes.SideOpen {
		t.Fatal("右栏开合没写回分栏偏好")
	}
	tt.Key(ui.Cmd, ui.KeyBackslash)
	tt.Frame()
	if hasKey(tt, "side") {
		t.Fatal("Ctrl+\\ 再按没关右栏")
	}
}

// 断点两边各差 1：712 是三层（先看到列表），713 是两栏；1005 右栏是抽屉，1006 并排。
func TestShellBreakpoints(t *testing.T) {
	for _, tc := range []struct {
		w                  int
		layers, drawerSide bool
		sideColumn         bool
	}{{704, true, false, false}, {705, false, false, false}, {997, false, true, false}, {998, false, false, true}} {
		a := shellAppFlat()
		a.workbench.shell.st.Panes.SideOpen = true
		if tc.drawerSide {
			a.shellState().Layer = winshell.LayerSide
		}
		tt := shellTester(a, tc.w, 1030)
		if tc.layers && (!hasKey(tt, "list-layer") || hasKey(tt, "chat-local:r1")) {
			t.Errorf("宽 %d：应是列表层", tc.w)
		}
		if !tc.layers && !hasKey(tt, "columns") {
			t.Errorf("宽 %d：应是并排的栏", tc.w)
		}
		if tc.drawerSide != hasKey(tt, "float-panel") {
			t.Errorf("宽 %d：浮动面板 应为 %v", tc.w, tc.drawerSide)
		}
		if inColumn := hasKey(tt, "side") && !hasKey(tt, "float-panel"); tc.sideColumn != inColumn {
			t.Errorf("宽 %d：右栏并排 应为 %v", tc.w, tc.sideColumn)
		}
	}
}

// I1：窄窗口三层：点会话进聊天，返回钮回列表，右栏钮进第三层，返回聊天回来；来回都走通（笔记 A3、I1、D6）。
func TestShellNarrowLayersRoundTrip(t *testing.T) {
	a := shellAppFlat() // 已选着“白羽”：点它只是进聊天层，不用真连后台开会话
	tt := shellTester(a, 640, 900)
	if !hasKey(tt, "list-layer") {
		t.Fatal("窄窗口应先是列表层")
	}
	click(t, tt, "白羽")
	if a.shellState().Layer != winshell.LayerChat {
		t.Fatalf("点会话后层 = %v，应进聊天", a.shellState().Layer)
	}
	if hasKey(tt, "navigation") {
		t.Fatal("聊天层里不该有导航（原型）")
	}
	click(t, tt, "返回会话列表")
	if a.shellState().Layer != winshell.LayerList || !hasKey(tt, "list-layer") {
		t.Fatalf("返回钮没回列表：层 %v", a.shellState().Layer)
	}
	click(t, tt, "白羽")
	click(t, tt, "资料与活动（Ctrl+\\）")
	if a.shellState().Layer != winshell.LayerSide || !hasKey(tt, "side") {
		t.Fatalf("右栏钮没进第三层：%v", a.shellState().Layer)
	}
	click(t, tt, "返回聊天")
	if a.shellState().Layer != winshell.LayerChat {
		t.Fatalf("返回聊天没回聊天层：%v", a.shellState().Layer)
	}
}

// A4：中等宽度的浮动右栏（原抽屉，没有遮罩了）：打开后 Esc、点外面两种方式都能关，关后回到聊天；点聊天区不关（见 float 测试）。
func TestShellDrawerThreeWaysToClose(t *testing.T) {
	for _, way := range []string{"Esc", "点外面"} {
		a := shellAppFlat()
		tt := shellTester(a, 900, 900)
		click(t, tt, "资料与活动（Ctrl+\\）")
		if !hasKey(tt, "float-panel") {
			t.Fatalf("%s：浮动右栏没打开", way)
		}
		switch way {
		case "Esc":
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
		default:
			chat := boxOf(t, tt, "chat-local:r1").Box
			tt.ClickAt(chat.X+40, chat.Y+300)
			tt.Frame()
		}
		tt.Advance(time.Second)
		tt.Frame()
		if hasKey(tt, "float-panel") || a.shellState().Layer != winshell.LayerChat || a.shellState().Panes.SideOpen {
			t.Errorf("%s：没关上（层 %v，开 %v）", way, a.shellState().Layer, a.shellState().Panes.SideOpen)
		}
	}
}

// 输入区默认全宽（主人 2026-11-09）：和标题栏浮条同宽，四周离聊天窗 16（ADR-326）。
func TestShellChatColumnFullWidth(t *testing.T) {
	a := shellAppFlat()
	tt := shellTester(a, 2048, 1030)
	chat := boxOf(t, tt, "chat-local:r1")
	slot := boxOf(t, tt, "composer-frame")
	bar := boxOf(t, tt, "chat-header")
	if !closeTo(slot.Box.X-chat.Box.X, 16) || !closeTo(slot.Box.W, chat.Box.W-32) || !closeTo(slot.Box.W, bar.Box.W) {
		t.Fatalf("输入区 %+v、浮条 %+v、聊天窗 %+v：输入区应和浮条同宽、两边留 16", slot.Box, bar.Box, chat.Box)
	}
	if bottom := chat.Box.Y + chat.Box.H - (slot.Box.Y + slot.Box.H); !closeTo(bottom, 16) {
		t.Fatalf("输入区离聊天窗底边 %v，应是 16", bottom)
	}
}

// 新壳从自己的 shellPanes 键读栏位偏好，不读旧界面的 panes：旧键里的 compact=true、thirdOpen=true 不会让新壳一打开
// 就是头像列或右栏（主人 2026-11-08 真窗口：默认就是“只有头像、宽度却很宽”）。默认展开，列表宽按 5/14。
func TestShellIgnoresLegacyPanes(t *testing.T) {
	a := shellAppFlat()
	a.pn.load(desksettings.Panes{Compact: true, ThirdOpen: true, ListRatio: 0.1})
	tt := shellTester(a, 2048, 1030)
	if a.shellState().Panes.Compact || a.shellState().Panes.SideOpen {
		t.Fatalf("新壳读了旧键：%+v", a.shellState().Panes)
	}
	if w := boxOf(t, tt, "sidebar").Box.W; !closeTo(w, 300) {
		t.Fatalf("默认列表宽 %v，应为 300（展开）", w)
	}
	if !tt.HasText("今晚的月色真好") || !hasKey(tt, "conversation-local:r1") {
		t.Fatal("默认就该是展开的会话列表，有名字和预览")
	}
}

// 栏位偏好 500 毫秒没再变就写进桌面设置的 shellPanes；重新读回来一样；拖分隔条过程中不一帧写一次。
func TestShellPanesPersist(t *testing.T) {
	st := desksettings.New(hostFixture(t).DataDir)
	a := shellAppFlat()
	a.desktop = st
	a.postFn = func(f func()) { f() }
	tt := shellTester(a, 2048, 1030)
	tt.Key(ui.Cmd, ui.KeyBackslash)
	tt.Frame()
	if got, _ := st.ShellPanes(); got.SideOpen {
		t.Fatal("还没过 500 毫秒就写了")
	}
	for range 5 {
		tt.Advance(200 * time.Millisecond)
		tt.Frame()
	}
	a.deskSaves.wait()
	if got, err := st.ShellPanes(); err != nil || !got.SideOpen {
		t.Fatalf("栏位偏好没写进桌面设置：%+v %v", got, err)
	}
}
