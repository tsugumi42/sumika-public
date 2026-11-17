package chatui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 气泡的状态只在右下角时间那一格写一次，不在正文下面另起一行。
func TestInfoText(t *testing.T) {
	at := time.Date(2026, 11, 9, 2, 31, 0, 0, time.Local)
	cases := []struct {
		name string
		m    Msg
		want string
	}{
		{"普通", Msg{Text: "正文", At: at}, "02:31"},
		{"撤回只写一次", Msg{Text: "正文", At: at, State: Recalled, Notes: []string{"已撤回"}}, "已撤回 02:31"},
		{"撤回没带注释", Msg{Text: "正文", At: at, State: Recalled}, "已撤回 02:31"},
		{"没发出去", Msg{Text: "正文", At: at, State: Failed, Notes: []string{"没发出去"}}, "没发出去 02:31"},
		{"被打断又撤回", Msg{Text: "正文", At: at, State: Recalled, Notes: []string{"被打断", "已撤回"}}, "被打断 已撤回 02:31"},
		{"原文是空的，正文已经写了", Msg{At: at, State: Recalled, Notes: []string{"已撤回"}}, "02:31"},
		{"没有时间也要写状态", Msg{Text: "正文", Notes: []string{"没发出去"}}, "没发出去"},
		{"什么都没有", Msg{Text: "正文"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := infoText(&tc.m); got != tc.want {
				t.Fatalf("%q，应为 %q", got, tc.want)
			}
		})
	}
}

func TestStatusNoteDrawnOnlyInTimeSlot(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600, NoEnter: true}
	at := time.Date(2026, 11, 9, 2, 31, 0, 0, time.Local)
	m := &Msg{ID: "m", Text: "两块钱两亿来了", At: at, State: Recalled, Notes: []string{"已撤回"}}
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 900, 300)
	tt.Frame()
	n := 0
	for _, s := range tt.Texts() {
		n += strings.Count(s, "已撤回")
	}
	if n != 1 {
		t.Fatalf("“已撤回”写了 %d 次：%q", n, tt.Texts())
	}
}
