package chatui

import (
	"testing"
	"time"
)

// 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

// 过程气泡和这一轮的回复气泡之间照气泡的组内间距（ADR-287：过程就是她的一个气泡）。
func TestProcessToReplyGap(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, NoEnter: true}
	at := time.Date(2026, 11, 6, 22, 0, 0, 0, time.Local)
	msgs := []*Msg{
		{ID: "p", Sender: "r1", At: at, Steps: []*Step{{ID: "c", Kind: StepCommand, State: StepDone, Target: "ls"}}},
		{ID: "m", Sender: "r1", At: at.Add(time.Second), Text: "好了。"},
	}
	root := tree(msgsTester(h, msgs, 900))
	p, m := root.find("p"), root.find("m")
	if gap := m.Box.Y - (p.Box.Y + p.Box.H); !near(gap, tk.GapInGroup) {
		t.Errorf("过程到回复气泡 %.2f，期望组内间距 %.2f", gap, tk.GapInGroup)
	}
}
