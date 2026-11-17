package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/segview"
)

// 标题栏和输入框是真叠在消息列上面的浮层，不是上下两块（主人 2026-11-08：别拿一样的背景图案假装透明）。
// 这几条测的是结构：消息列铺满聊天区，上下各让出实测的浮层高度；滚到底最后一条停在输入框上沿；往上滚消息从输入框底下经过。
func overlayApp(n int) *app {
	a := shellApp()
	for i := range n {
		text := fmt.Sprintf("第 %d 条编造的消息，长一点好让它占满一行再多一点点", i)
		a.msgs = append(a.msgs, &msg{id: fmt.Sprintf("o%d", i), who: "白羽", mine: i%3 == 0, text: text, blocks: segview.TextBlocks(text), at: 60000 * int64(i+2)})
	}
	a.list = a.newList()
	return a
}

func TestShellLastMessageStopsAtComposerTop(t *testing.T) {
	a := overlayApp(60)
	tt := shellTester(a, 2048, 1030)
	a.list.ScrollToEnd()
	tt.Frame()
	tt.Frame()
	list, comp := boxOf(t, tt, "workbench-messages"), boxOf(t, tt, "composer")
	chat := boxOf(t, tt, "chat-local:r1")
	if !closeTo(list.Box.Y, chat.Box.Y) || !closeTo(list.Box.H, chat.Box.H) {
		t.Fatalf("消息列 %+v 没有铺满聊天区 %+v（浮层要叠在它上面）", list.Box, chat.Box)
	}
	last := boxOfLabel(t, tt, "消息：o59")
	if last.Box.Y+last.Box.H > comp.Box.Y+0.5 {
		t.Fatalf("最后一条的下沿 %.1f 压进了输入框（上沿 %.1f）", last.Box.Y+last.Box.H, comp.Box.Y)
	}
	if gap := comp.Box.Y - (last.Box.Y + last.Box.H); gap > 40 {
		t.Fatalf("最后一条离输入框上沿 %.1f，该紧贴着（底部内距 = 输入区实测高度）", gap)
	}
}

func boxOfLabel(t *testing.T, tt *ui.Tester, label string) uiBox {
	t.Helper()
	for _, b := range uiBoxes(tt) {
		if b.Label == label {
			return b
		}
	}
	t.Fatalf("找不到 %s", label)
	return uiBox{}
}

func TestShellMessagesScrollUnderComposerAndHeader(t *testing.T) {
	for _, material := range []string{"liquid", "blur"} {
		a := overlayApp(60)
		a.workbench.shell.material = material
		tt := shellTester(a, 2048, 1030)
		a.list.ScrollTo(30, ui.Start)
		tt.Frame()
		tt.Frame()
		comp, head := boxOf(t, tt, "composer"), boxOf(t, tt, "chat-header")
		overlap := func(r uiBox) bool {
			var n int
			for _, b := range uiBoxes(tt) {
				if strings.HasPrefix(b.Label, "消息：") && b.Box.W > 0 {
					if b.Box.X < r.Box.X+r.Box.W && r.Box.X < b.Box.X+b.Box.W && b.Box.Y < r.Box.Y+r.Box.H && r.Box.Y < b.Box.Y+b.Box.H {
						n++
					}
				}
			}
			return n > 0
		}
		if !overlap(comp) {
			t.Errorf("%s：输入框后面没有消息元素（消息没从底下过）", material)
		}
		if !overlap(head) {
			t.Errorf("%s：标题栏后面没有消息元素", material)
		}
		want := map[string]string{"liquid": "glass.Glass", "blur": "glass.Blur"}[material]
		for _, key := range []string{"composer", "chat-header-fill"} {
			if got, _ := nodeMaterial(tt, key); got != want {
				t.Errorf("%s：%s 的材质 %q，应为 %q", material, key, got, want)
			}
		}
	}
}

// 输入框变高（加了引用卡）：消息列底部的内边距跟着变，滚到底最后一条仍停在输入框上沿。
func TestShellListBottomInsetFollowsComposerHeight(t *testing.T) {
	a := overlayApp(60)
	tt := shellTester(a, 2048, 1030)
	a.list.ScrollToEnd()
	tt.Frame()
	tt.Frame()
	before := a.workbench.shell.st.ComposerH
	compBefore := boxOf(t, tt, "composer").Box.H
	a.composerOf(a.sel).AddQuote(chatui.Quoted{MessageID: "o10", Name: "白羽", Text: "第 10 条"}, false)
	for range 4 {
		tt.Frame()
	}
	a.list.ScrollToEnd()
	tt.Frame()
	tt.Frame()
	after := a.workbench.shell.st.ComposerH
	compAfter := boxOf(t, tt, "composer").Box.H
	if compAfter <= compBefore+10 {
		t.Fatalf("加了引用卡输入框没变高：%v → %v", compBefore, compAfter)
	}
	if d := (after - before) - (compAfter - compBefore); d > 1 || d < -1 {
		t.Fatalf("底部内距变化 %.1f，应等于输入框高度变化 %.1f", after-before, compAfter-compBefore)
	}
	last, comp := boxOfLabel(t, tt, "消息：o59"), boxOf(t, tt, "composer")
	if last.Box.Y+last.Box.H > comp.Box.Y+0.5 {
		t.Fatalf("输入框变高后最后一条压进了输入框：%.1f > %.1f", last.Box.Y+last.Box.H, comp.Box.Y)
	}
}

// 给别的线用的“浮层上沿”：消息列可见区 = 列表框去掉上下浮层，OverlayInsets 给出这两个高度（实测，不写死）。
func TestShellOverlayInsetsAreMeasured(t *testing.T) {
	a := overlayApp(5)
	tt := shellTester(a, 2048, 1030)
	top, bottom := a.wbOverlayInsets()
	comp, chat := boxOf(t, tt, "composer"), boxOf(t, tt, "chat-local:r1")
	if !closeTo(top, 64) {
		t.Fatalf("上边浮层高 %v，应为标题栏加上边距 64", top)
	}
	if want := chat.Box.Y + chat.Box.H - comp.Box.Y; !closeTo(bottom, want) {
		t.Fatalf("下边浮层高 %v，应为聊天区底到输入框上沿 %v", bottom, want)
	}
}
