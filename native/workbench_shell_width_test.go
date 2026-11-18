package main

import (
	"fmt"
	"testing"

	"sumika-native/internal/segview"
	"sumika-native/internal/winshell"
)

// 聊天区宽度可配置（外观里的“私聊区宽度”“群聊区宽度”，40%～100%，步长 5）：只影响消息列和输入框，两者永远同宽、边线对齐；
// 群聊拉满（100%）时对方的气泡贴左边内距、自己的贴右边内距；聊天栏窄于 520 时百分比不再缩，直接铺满。
func TestShellChatWidthPercent(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, w := range []int{2048, 1100, 640, 480} {
			for _, pct := range []float32{40, 70, 100} {
				name := fmt.Sprintf("群聊=%v 窗口=%d 宽度=%v%%", group, w, pct)
				a := shellAppFlat()
				a.group = group
				a.msgs = append(a.msgs, &msg{id: "m2", who: "我", mine: true, text: "我说的一句", blocks: segview.TextBlocks("我说的一句"), at: 120000})
				a.list = a.newList()
				a.workbench.shell.widthPct = [2]float32{pct, pct}
				if w < 713 {
					a.workbench.shell.st.Layer = winshell.LayerChat
				}
				tt := shellTester(a, w, 900)
				chat := boxOf(t, tt, "chat-local:r1")
				slot, row := boxOf(t, tt, "composer-frame"), boxOf(t, tt, "message-row")
				if !closeTo(slot.Box.X, row.Box.X) || !closeTo(slot.Box.W, row.Box.W) {
					t.Fatalf("%s：输入框 %+v 和消息列 %+v 没有同宽对齐", name, slot.Box, row.Box)
				}
				gap := float32(16) // 离聊天区两边的距离（ADR-326），窄窗口 8
				if w < 713 {       // 窄窗口断点
					gap = 8
				}
				inset := (chat.Box.W - slot.Box.W) / 2 // 两边各留多少
				full := chat.Box.W - 2*inset
				want := full
				if pct < 100 && chat.Box.W >= winshell.NarrowContentWidth {
					want = (chat.Box.W - 2*gap) * pct / 100 // 两边的距离
				}
				if !closeTo(slot.Box.W, want) {
					t.Fatalf("%s：聊天区宽 %.1f，聊天栏 %.1f，应为 %.1f", name, slot.Box.W, chat.Box.W, want)
				}
				if chat.Box.W < winshell.NarrowContentWidth && !closeTo(slot.Box.W, chat.Box.W-2*gap) {
					t.Fatalf("%s：窄栏（%.0f）里百分比该不再缩，直接铺满：%.1f", name, chat.Box.W, slot.Box.W)
				}
				if group && pct == 100 {
					minX, maxR := float32(1e9), float32(-1)
					for _, b := range uiBoxes(tt) {
						if b.Label == "气泡" {
							minX, maxR = min(minX, b.Box.X), max(maxR, b.Box.X+b.Box.W)
						}
					}
					// 自己的贴右边内距；对方的左边先是头像（宽 + 间距约 41），头像贴左边内距，气泡紧挨着头像
					if d := minX - (chat.Box.X + gap); d < 30 || d > 50 || !closeTo(maxR, chat.Box.X+chat.Box.W-gap) {
						t.Fatalf("%s：气泡 %.1f～%.1f，群聊拉满时头像该贴左内距 %.1f（气泡在头像右边约 41）、自己的贴右内距 %.1f", name, minX, maxR, chat.Box.X+gap, chat.Box.X+chat.Box.W-gap)
					}
				}
			}
		}
	}
}

// 群聊、私聊各用各的那一项：本地群聊和 QQ 群走群聊，本地私聊和 QQ 私聊走私聊；没设就是默认（群聊 100，私聊 55）。
func TestShellChatWidthPicksByConversationKind(t *testing.T) {
	t.Parallel()
	a := shellAppFlat()
	if got := a.wbWidthPct(); got != 100 {
		t.Fatalf("私聊默认 %v，应为 100（没设就拉满）", got)
	}
	a.group = true
	if got := a.wbWidthPct(); got != 100 {
		t.Fatalf("群聊默认 %v，应为 100", got)
	}
	a.workbench.shell.widthPct = [2]float32{70, 85}
	if got := a.wbWidthPct(); got != 85 {
		t.Fatalf("群聊选了 85，实际 %v", got)
	}
	a.group = false
	if got := a.wbWidthPct(); got != 70 {
		t.Fatalf("私聊选了 70，实际 %v", got)
	}
}

// 宽度滑块：拖的时候界面实时变，输入框和消息列一直同宽；两个滑块的位置自己不抖。
func TestShellChatWidthSliderLive(t *testing.T) {
	a := shellAppFlat()
	a.rail = 4
	tt := shellTester(a, 2048, 1030)
	slider := boxOf(t, tt, "look-width-private-slider")
	y := slider.Box.Y + slider.Box.H/2
	tt.Press(slider.Box.X+slider.Box.W*0.5, y)
	assertNoHorizontalShift(t, tt, []string{"look-width-private-slider", "look-width-group-slider"}, func() {
		tt.Move(slider.Box.X+slider.Box.W-2, y)
		tt.Frame()
	}, 10)
	tt.Release(slider.Box.X+slider.Box.W-2, y)
	tt.Frame()
	if got := a.workbench.shell.widthPct[0]; got != 100 {
		t.Fatalf("拖到最右私聊宽度 %v，应为 100", got)
	}
}

// 气泡按内容收紧、时间和文字同行：夹具（手造 blocks）和真实路径（msgFromView 在转换阶段 textBlocks）画出来是一样的；
// 短消息的气泡比长消息窄得多，不是等宽的。
func TestShellBubblesHugContentOnBothPaths(t *testing.T) {
	short, long := "好", "这是一条比较长的消息，用来看气泡是不是按内容收紧，而不是和消息列一样宽"
	a := shellAppFlat()
	a.msgs = []*msg{
		{id: "a", who: "白羽", text: short, blocks: segview.TextBlocks(short), at: 60000},
		{id: "b", who: "白羽", text: long, blocks: segview.TextBlocks(long), at: 120000},
	}
	a.list = a.newList()
	tt := shellTester(a, 2048, 900)
	var widths []float32
	for _, b := range uiBoxes(tt) {
		if b.Label == "气泡" {
			widths = append(widths, b.Box.W)
		}
	}
	if len(widths) != 2 || widths[0] >= widths[1]/2 {
		t.Fatalf("气泡宽 %v：短消息该比长消息窄得多（按内容收紧）", widths)
	}
	// 时间和文字同行：时间框的纵向范围落在气泡里，气泡高不超过一行文字加内距
	if bubble := boxOfLabel(t, tt, "气泡"); bubble.Box.H > 3*a.workbench.shell.kit.G.MetaLine+2*a.workbench.shell.kit.T.BubblePadV+8 {
		t.Fatalf("短消息的气泡高 %.1f，时间像是单独占了一行", bubble.Box.H)
	}
}
