package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// 转发查看窗口里的"正在从 QQ 取回聊天记录…"（loadstate 的规则）：阈值内不写，慢了才写。

// 转发窗口刚打开、本地有缓存一下就回来：不闪"正在从 QQ 取回聊天记录…"；慢了才写
func TestLoadHintForwardWindow(t *testing.T) {
	for _, slow := range []bool{false, true} {
		name := map[bool]string{false: "fast", true: "slow"}[slow]
		t.Run(name, func(t *testing.T) {
			r := &controlledForwardReader{calls: make(chan forwardReadCall, 10)}
			posts := make(chan func(), 10)
			a := &app{reader: r, postFn: func(fn func()) { posts <- fn }}
			t.Cleanup(a.stopForward)
			tt := ui.NewTester(func(c *ui.Context) { a.forwardWindow(c) }, 900, 800)
			a.openForward(api.EventRef{}, "outer", nil)
			var call forwardReadCall
			select {
			case call = <-r.calls:
			case <-time.After(uiWaitLimit):
				t.Fatal("没有读取")
			}
			tt.Frame()
			const hint = "正在从 QQ 取回聊天记录"
			if slow {
				advanceFrames(tt, 5, 50*time.Millisecond, hint, t)
				tt.Advance(100 * time.Millisecond)
				tt.Frame()
				if !tt.HasText(hint + "…") {
					t.Fatalf("过了阈值应写提示：%v", tt.Texts())
				}
			} else {
				advanceFrames(tt, 3, 50*time.Millisecond, hint, t)
			}
			call.reply <- api.ForwardView{ID: "outer", State: "ready", Nodes: []api.ForwardNode{{Name: "编的名字", Segments: []api.DisplaySegment{{Kind: "text", Text: "正文"}}}}}
			select {
			case fn := <-posts:
				fn()
			case <-time.After(uiWaitLimit):
				t.Fatal("没有交回结果")
			}
			tt.Frame()
			advanceFrames(tt, 4, 250*time.Millisecond, hint, t)
		})
	}
}
