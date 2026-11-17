package main

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/layout"
	"sumika-native/internal/api"
	"sumika-native/internal/desksettings"
)

func panesApp() *app {
	a := chatApp([]*msg{{id: "m1", text: "编的一句话"}}, false)
	for i, name := range []string{"小葵", "小桃", "小梅"} {
		a.local = append(a.local, conv{Key: api.ConversationKey{Platform: "local", Scene: "private", Conversation: fmt.Sprintf("console:c%d", i)}, Kind: "local", Title: name, Last: lastLine{Text: "最后一句", CreatedAt: t0}, Unread: i})
	}
	a.sel = a.local[0].Key.Conversation
	a.pn.load(desksettings.DefaultPanes())
	a.pn.layout.Active = layout.Chat
	return a
}
func withPanes(a *app, edit func(*desksettings.Panes)) *app {
	p := desksettings.DefaultPanes()
	edit(&p)
	a.pn.load(p)
	return a
}
func paneTester(a *app, w, h int) *ui.Tester { return testerOf(w, h, a.view) }
func paneWidths(tt *ui.Tester) (list, chat, side float32) {
	get := func(label string) float32 { r, _ := tt.Find(label); return r.W }
	return get("会话列表"), get("聊天窗口"), get("右侧栏内容")
}

const panesY = 500

func listDividerX(t *testing.T, tt *ui.Tester) float32 {
	t.Helper()
	r, ok := tt.Find("Resize conversations")
	if !ok {
		t.Fatal("没有列表分隔线")
	}
	return r.X + 0.5
}
func sideDividerX(t *testing.T, tt *ui.Tester) float32 {
	t.Helper()
	r, ok := tt.Find("Resize third pane")
	if !ok {
		t.Fatal("没有第三栏分隔线")
	}
	return r.X + 0.5
}
func drag(tt *ui.Tester, x, dx float32) {
	tt.Press(x, panesY)
	tt.Move(x+dx, panesY)
	tt.Release(x+dx, panesY)
	tt.Advance(time.Second)
}
func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

// clickAt 按元素的位置点，不用 tt.Click。
// 临时：mygo 0.8 的测试器要求按名字点到的元素自己接住点击，MewTypeUI 有两种结构过不了，真实点击都照样生效（和 0.7.4 一样）：
// 会话列表、文件夹栏的行外层是带名字的 mygo 列表行，里层为了画悬停底色和按下水波另挂了点击；
// MewTypeUI 的 TextField 名字挂在外框上，里层输入框接点击，外框没把焦点转给它（mygo 自己的输入框会转）。
// mygo 开放列表行状态（egoist/mygo#1439）、MewTypeUI 改好以后换回 tt.Click（待办"会话行只走 mygo 列表行"）。
func clickAt(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("找不到 %q", label)
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
}
func closeFloat(a, b float32) bool { return math.Abs(float64(a-b)) < 1 }

func TestPanesResponsiveBoundaries(t *testing.T) {
	for _, w := range []int{380, 451, 452, 453, 711, 712, 713, 1003, 1004, 1005, 2048} {
		a := panesApp()
		a.pn.layout.ThirdOpen = true
		tt := paneTester(a, w, 1030)
		info := a.pn.layout.Info()
		if info.VerticalFolders != (w >= 452) {
			t.Fatalf("%d: 竖栏=%v", w, info.VerticalFolders)
		}
		want := layout.Single
		if w >= 712 {
			want = layout.Double
		}
		if w >= 1004 {
			want = layout.Triple
		}
		if info.Mode != want {
			t.Fatalf("%d: mode=%v want=%v", w, info.Mode, want)
		}
		sr, ok := tt.Find("右侧栏内容")
		if !ok {
			t.Fatalf("%d: 已开的第三栏不可达", w)
		}
		if w < 1004 {
			if !info.ThirdInMain {
				t.Fatalf("%d: 第三栏没有占主槽", w)
			}
			if _, ok := tt.Find("聊天窗口"); ok {
				t.Fatalf("%d: 第三栏与聊天重叠", w)
			}
		}
		if sr.X < 0 || sr.X+sr.W > float32(w) || sr.H <= 0 {
			t.Fatalf("%d: 第三栏越界 %+v", w, sr)
		}
		click(t, tt, labelSideFold)
		if _, ok := tt.Find("聊天窗口"); !ok {
			t.Fatalf("%d: 关闭资料未返回聊天", w)
		}
		t.Logf("width=%d mode=%v rail=%v list=%.0f chat=%.0f third=%.0f main=%v", w, info.Mode, info.VerticalFolders, info.ListWidth, info.ChatWidth, info.ThirdWidth, info.ThirdInMain)
	}
}

func TestPanesDragKeyboardAndCompact(t *testing.T) {
	a := panesApp()
	tt := paneTester(a, 2048, 1030)
	l, _, _ := paneWidths(tt)
	if l != 540 {
		t.Fatalf("默认比例上限列表=%v", l)
	}
	drag(tt, listDividerX(t, tt), -200)
	if l, _, _ := paneWidths(tt); !closeFloat(l, 340) {
		t.Fatalf("拖动后列表=%v", l)
	}
	tt.ClickAt(listDividerX(t, tt), panesY)
	tt.Key(0, ui.KeyRight)
	if l, _, _ := paneWidths(tt); !closeFloat(l, 350) {
		t.Fatalf("方向键后列表=%v", l)
	}
	x := listDividerX(t, tt)
	tt.Press(x, panesY)
	for i := 1; i <= 23; i++ {
		tt.Move(x-float32(i*10), panesY)
	}
	tt.Release(x-230, panesY)
	tt.Advance(time.Second)
	if l, _, _ := paneWidths(tt); l != 66 {
		t.Fatalf("越过130应收为66: %v", l)
	}
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Advance(time.Second)
	if l, _, _ := paneWidths(tt); l < 260 {
		t.Fatalf("重新展开列表=%v", l)
	}
	tt.Key(ui.Cmd, ui.KeyBackslash)
	if _, _, s := paneWidths(tt); s != 292 {
		t.Fatalf("第三栏默认=%v", s)
	}
	drag(tt, sideDividerX(t, tt), -500)
	if _, _, s := paneWidths(tt); s != 392 {
		t.Fatalf("第三栏上限=%v", s)
	}
	drag(tt, sideDividerX(t, tt), 600)
	if _, _, s := paneWidths(tt); s != 292 {
		t.Fatalf("第三栏下限=%v", s)
	}
	tt.Key(0, ui.KeyEscape)
	if _, _, s := paneWidths(tt); s != 0 {
		t.Fatalf("Esc未关闭第三栏: %v", s)
	}
}

func TestPanesNarrowBackAndSelection(t *testing.T) {
	a := panesApp()
	tt := paneTester(a, 380, 700)
	click(t, tt, "返回会话列表")
	if _, ok := tt.Find("会话列表"); !ok {
		t.Fatal("未回列表")
	}
	a.open(a.sel)
	tt.Frame()
	if _, ok := tt.Find("聊天窗口"); !ok {
		t.Fatal("再次选择当前会话未进入聊天")
	}
	before := a.pn.preferences()
	tt.SetSize(2048, 1030)
	tt.Frame()
	tt.SetSize(380, 700)
	tt.Frame()
	if a.pn.preferences() != before {
		t.Fatal("调整窗口改写用户偏好")
	}
}

func TestPanesSaveIsDebounced(t *testing.T) {
	a := panesApp()
	var saves []desksettings.Panes
	a.savePanes = func(p desksettings.Panes) { saves = append(saves, p) }
	tt := paneTester(a, 2048, 1030)
	tt.Advance(time.Second)
	tt.SetSize(900, 700)
	tt.Frame()
	tt.SetSize(2048, 1030)
	tt.Frame()
	if len(saves) != 0 {
		t.Fatal("窗口调整不应保存")
	}
	x := listDividerX(t, tt)
	tt.Press(x, panesY)
	for i := 1; i <= 10; i++ {
		tt.Move(x-float32(i*10), panesY)
		tt.Advance(40 * time.Millisecond)
	}
	if len(saves) != 0 {
		t.Fatal("拖动中保存了")
	}
	tt.Release(x-100, panesY)
	tt.Advance(400 * time.Millisecond)
	if len(saves) != 0 {
		t.Fatal("防抖提前保存")
	}
	tt.Advance(200 * time.Millisecond)
	if len(saves) != 1 || saves[0] != a.pn.preferences() {
		t.Fatalf("应只保存最终偏好: %+v", saves)
	}
	tt.Key(ui.Cmd, ui.KeyBackslash)
	p := a.pn.unsaved()
	if p == nil || !p.ThirdOpen {
		t.Fatal("退出前丢了未保存偏好")
	}
	if a.pn.unsaved() != nil {
		t.Fatal("未保存偏好重复取出")
	}
}

func TestPanesKeysOnlyOnChatPages(t *testing.T) {
	a := panesApp()
	a.rail = 4
	tt := paneTester(a, 2048, 1030)
	before := a.pn.preferences()
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Key(ui.Cmd, ui.KeyBackslash)
	if a.pn.preferences() != before {
		t.Fatal("设置页快捷键改了聊天偏好")
	}
}

func TestPanesReverseDuringTransition(t *testing.T) {
	a := panesApp()
	tt := ui.NewTester(a.view, 712, 700)
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyBackslash)
	if !a.pn.layout.ThirdOpen {
		t.Fatal("开栏动作没有进入过渡")
	}
	tt.Advance(40 * time.Millisecond)
	tt.Key(ui.Cmd, ui.KeyBackslash)
	tt.Advance(time.Second)
	if _, ok := tt.Find("聊天窗口"); !ok {
		t.Fatal("反向打断后聊天未恢复")
	}
	if _, ok := tt.Find("右侧栏内容"); ok {
		t.Fatal("反向打断后隐藏资料仍占主槽")
	}
}
