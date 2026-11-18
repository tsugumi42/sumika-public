package main

import (
	"context"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"testing"
	"time"
)

type forwardReadCall struct {
	ctx   context.Context
	req   api.ForwardRequest
	reply chan api.ForwardView
}
type controlledForwardReader struct {
	api.Reader
	calls chan forwardReadCall
}

func (r *controlledForwardReader) ReadForward(ctx context.Context, req api.ForwardRequest) (api.Result[api.ForwardView], error) {
	call := forwardReadCall{ctx: ctx, req: req, reply: make(chan api.ForwardView, 1)}
	r.calls <- call
	return api.Result[api.ForwardView]{State: api.Ready, Value: <-call.reply}, nil
}
func TestForwardRefreshEnterAndReturn(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "queued-old-result"}[queued], func(t *testing.T) {
			r := &controlledForwardReader{calls: make(chan forwardReadCall, 10)}
			posts := make(chan func(), 10)
			a := &app{reader: r, postFn: func(fn func()) { posts <- fn }}
			t.Cleanup(a.stopForward)
			tt := ui.NewTester(func(c *ui.Context) { a.forwardWindow(c) }, 900, 800)
			next := func(id string) forwardReadCall {
				t.Helper()
				select {
				case call := <-r.calls:
					if call.req.ID != id {
						t.Fatalf("读错层：%s", call.req.ID)
					}
					return call
				case <-time.After(uiWaitLimit):
					t.Fatalf("没有读取%s", id)
					return forwardReadCall{}
				}
			}
			post := func() func() {
				t.Helper()
				select {
				case fn := <-posts:
					return fn
				case <-time.After(uiWaitLimit):
					t.Fatal("没有交回结果")
					return nil
				}
			}
			answer := func(call forwardReadCall, text string) {
				call.reply <- api.ForwardView{ID: call.req.ID, State: "ready", Nodes: []api.ForwardNode{{Name: "编的名字", Segments: []api.DisplaySegment{{Kind: "text", Text: text}}}}}
			}
			a.openForward(api.EventRef{}, "outer", nil)
			answer(next("outer"), "最初外层")
			post()()
			tt.Frame()
			parent := a.forward.stack[0]
			a.refreshForward()
			old := next("outer")
			a.refreshForward()
			var stale func()
			if queued {
				answer(old, "迟到旧外层")
				stale = post()
			}
			a.openForward(api.EventRef{}, "inner", []string{"outer"})
			if parent.busy {
				t.Error("取消父层读取后仍然忙")
			}
			if old.ctx.Err() == nil {
				t.Error("父层请求没有取消")
			}
			answer(next("inner"), "里层正文")
			post()()
			tt.Frame()
			click(t, tt, "返回")
			current := next("outer")
			answer(current, "刷新后的外层")
			post()()
			tt.Frame()
			if queued {
				stale()
				tt.Frame()
			} else {
				answer(old, "迟到旧外层")
			}
			if !tt.HasText("刷新后的外层") || tt.HasText("迟到旧外层") || parent.busy || parent.dirty {
				t.Fatalf("返回刷新未完成或旧结果覆盖：texts=%v busy=%v dirty=%v", tt.Texts(), parent.busy, parent.dirty)
			}
		})
	}
}
