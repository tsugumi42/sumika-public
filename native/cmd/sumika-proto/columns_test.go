package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 分栏照 Telegram（ADR-285 第 1、2 条）：三栏 ≥ 1006、两栏 ≥ 713、更窄一栏；列表默认 B×5/14、至少 260、不封顶；
// 三栏放不下时列表和右栏按比例一起收，右栏不窄于 292、列表不窄于 260；聊天区至少 380；
// 列表拖到 130 以下收成 62 的头像模式，再拖宽恢复。
func TestTelegramColumns(t *testing.T) {
	cases := []struct {
		w          float32
		cols       int
		list, side float32 // 0 表示不检查
	}{
		{2048, 3, 706, sideDefault},           // 1976×5/14 = 705.7；706 + 320 + 380 + 2 放得下，不收
		{1400, 3, 474, sideDefault},           // 1328×5/14 = 474.3
		{1100, 3, 345, 300},                   // 列表 367 + 右栏 320 放不下 646，按比例收成 345、300（都没到下限）
		{breakpointWide, 3, listMin, sideMin}, // 1006：右栏先收到 292，列表 = 552 − 292 = 260
		{breakpointWide - 1, 2, 0, 0},
		{900, 2, 296, 0}, // 828×5/14 = 295.7
		{breakpointNarrow, 2, listMin, 0},
		{breakpointNarrow - 1, 1, 0, 0},
	}
	for _, cs := range cases {
		a := newApp()
		a.Layer = 1
		tt := ui.NewTester(a.view, int(cs.w), 700)
		tt.Frame()
		tt.Frame()
		root := geometryTree(tt)
		list, chat, side := root.Find("sidebar"), root.Find("chat-"+a.cv().ID), root.Find("side")
		cols := 0
		for _, n := range []*geometryNode{list, chat, side} {
			if n != nil && n.Box.W > 0 {
				cols++
			}
		}
		if cols != cs.cols {
			t.Errorf("宽 %.0f：%d 栏，期望 %d", cs.w, cols, cs.cols)
			continue
		}
		if chat != nil && cs.cols > 1 && chat.Box.W < chatMin-1 {
			t.Errorf("宽 %.0f：聊天区 %.1f 小于 %v", cs.w, chat.Box.W, chatMin)
		}
		if cs.list > 0 && list != nil && !near(list.Box.W, cs.list) {
			t.Errorf("宽 %.0f：会话列表 %.1f，期望 %v", cs.w, list.Box.W, cs.list)
		}
		if cs.side > 0 && side != nil && !near(side.Box.W, cs.side) {
			t.Errorf("宽 %.0f：右栏 %.1f，期望 %v", cs.w, side.Box.W, cs.side)
		}
	}
	// 拖分隔条：拖到 100 → 头像模式 66；再拖到 300 → 恢复
	a := newApp()
	a.Layer = 1
	tt := ui.NewTester(a.view, 1400, 700)
	tt.Frame()
	x := navWidth + a.ListWidth
	tt.Press(x, 350)
	tt.Move(navWidth+100, 350)
	tt.Release(navWidth+100, 350)
	tt.Frame()
	if !a.ListCompact || !near(a.ListWidth, listCompact) {
		t.Fatalf("拖到 100 没收成头像模式：compact=%v 宽=%.1f", a.ListCompact, a.ListWidth)
	}
	if root := geometryTree(tt); root.Find("search") != nil {
		t.Error("头像模式还有搜索框")
	}
	x = navWidth + a.ListWidth
	tt.Press(x, 350)
	tt.Move(navWidth+300, 350)
	tt.Release(navWidth+300, 350)
	tt.Frame()
	if a.ListCompact || a.ListWidth < listMin {
		t.Fatalf("从头像模式拖宽没恢复：compact=%v 宽=%.1f", a.ListCompact, a.ListWidth)
	}
}
