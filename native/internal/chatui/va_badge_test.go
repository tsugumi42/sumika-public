package chatui

import (
	"math"
	"testing"
	"time"
)

// 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

// 发送中的时钟和失败角标同位：同一个格子、贴气泡左侧底边。
func TestSendingSameSpotAsFailed(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, NoEnter: true}
	at := time.Date(2026, 11, 6, 22, 0, 0, 0, time.Local)
	spot := map[State][2]float32{}
	for _, st := range []State{Sending, Failed} {
		m := &Msg{ID: "s", Sender: "self", Self: true, At: at, Text: "这一条。", State: st}
		root := tree(msgsTester(h, []*Msg{m}, 900))
		bubble := root.find("bubble")
		key := map[State]string{Sending: "sending", Failed: "retry"}[st]
		slot := root.find(key)
		if bubble == nil || slot == nil {
			t.Fatalf("找不到气泡或 %s", key)
		}
		icon := slot // 量格子里的图标本身：格子位置两边本来就一样，差在图标在格子里贴哪
		for len(icon.Children) > 0 {
			icon = icon.Children[0]
		}
		spot[st] = [2]float32{bubble.Box.X - (icon.Box.X + icon.Box.W), bubble.Box.Y + bubble.Box.H - (icon.Box.Y + icon.Box.H)}
	}
	if spot[Sending] != spot[Failed] {
		t.Errorf("发送中的格子离气泡 %v，失败角标 %v，不同位", spot[Sending], spot[Failed])
	}
}

// 失败角标看得见的圆和角标的框一样大（规格"18 圆角标"；原来图标四周留白，圆只有约 15）。
func TestBadgeCircleFillsBox(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, NoEnter: true}
	m := &Msg{ID: "s", Sender: "self", Self: true, At: time.Date(2026, 11, 6, 22, 0, 0, 0, time.Local), Text: "这一条。", State: Failed}
	tt := msgsTester(h, []*Msg{m}, 800)
	badge := tree(tt).find("badge")
	ink := inkBox(tt, badge.Box)
	if math.Abs(float64(ink.W-tk.Badge)) > 1.2 || math.Abs(float64(ink.H-tk.Badge)) > 1.2 {
		t.Errorf("角标的圆 %.1f×%.1f，框是 %.1f", ink.W, ink.H, tk.Badge)
	}
}
