package chatui

import (
	"strings"
	"testing"
	"time"
)

// 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

// 撤回的照样显示原文、时间前写"已撤回"，和前后同一个人的消息合组（名字、头像不再出一次）。
func TestRecalledKeepsTextAndGroup(t *testing.T) {
	tk := Derive(DefaultSettings())
	h := Host{T: tk, Column: 720, NoEnter: true, Group: true}
	at := time.Date(2026, 11, 6, 22, 0, 0, 0, time.Local)
	msgs := []*Msg{
		{ID: "a", Sender: "r1", Name: "晴枝", At: at, Text: "撤回前的原话", State: Recalled},
		{ID: "b", Sender: "r1", Name: "晴枝", At: at.Add(30 * time.Second), Text: "刚才那句说错了。"},
	}
	tt := msgsTester(h, msgs, 900)
	all := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(all, "撤回前的原话") || !strings.Contains(all, "已撤回 22:00") {
		t.Errorf("撤回的没显示原文或标记：%q", all)
	}
	if strings.Count(all, "晴枝") != 1+1 { // 名字一次 + 头像读屏标签一次
		t.Errorf("撤回打断了合组，名字或头像出现了不止一次：%q", all)
	}
}
