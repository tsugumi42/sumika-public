package chatui

import (
	"github.com/egoist/mygo/ui"
)

// 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

// 这一组是 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

func msgsTester(h Host, msgs []*Msg, w int) *ui.Tester {
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("list")).Width(h.Column).Children(func() {
			for i, m := range msgs {
				var prev, next *Msg
				if i > 0 {
					prev = msgs[i-1]
				}
				if i+1 < len(msgs) {
					next = msgs[i+1]
				}
				h.Message(c, m, prev, next)
			}
		})
	}, w, 1600)
	tt.Frame()
	tt.Frame()
	return tt
}
