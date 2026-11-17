package main

import (
	"context"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"testing"
	"time"
)

type forwardUIReader struct {
	api.Reader
	views map[string]api.ForwardView
}

func (r *forwardUIReader) ReadForward(_ context.Context, req api.ForwardRequest) (api.Result[api.ForwardView], error) {
	return api.Result[api.ForwardView]{State: api.Ready, Value: r.views[req.ID]}, nil
}

func TestForwardCardNestedWindowAndExit(t *testing.T) {
	v := view("forward", "human", "编的甲", "[合并转发]", t0)
	v.Segments = []api.DisplaySegment{{Kind: "forward", Ref: "outer", Forward: &api.ForwardBrief{Title: "编的甲的聊天记录", Count: 1, State: "ready", Lines: [3]string{"编的甲：编的外层"}}}}
	m := msgFromView(v.Ref.Conversation, v, charOf)
	r := &forwardUIReader{views: map[string]api.ForwardView{
		"outer": {ID: "outer", State: "ready", Nodes: []api.ForwardNode{{UID: "100", Name: "编的甲", At: t0, Segments: []api.DisplaySegment{{Kind: "reply", Summary: "编的引用"}, {Kind: "text", Text: "编的外层"}, {Kind: "image"}, {Kind: "forward", Ref: "inner", Forward: &api.ForwardBrief{Title: "编的乙的聊天记录", Count: 1, State: "ready"}}}}}},
		"inner": {ID: "inner", State: "ready", Nodes: []api.ForwardNode{{UID: "200", Name: "编的乙", At: t0, Segments: []api.DisplaySegment{{Kind: "text", Text: "编的里层"}}}}},
	}}
	posts := make(chan func(), 10)
	a := &app{reader: r, postFn: func(fn func()) { posts <- fn }}
	t.Cleanup(a.stopForward)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { a.forwardCard(c, m, m.segs[0]) })
		a.forwardWindow(c)
	}, 900, 800)
	tt.SetPreferences(ui.Preferences{ReduceMotion: false})
	pump := func() {
		t.Helper()
		select {
		case fn := <-posts:
			fn()
		case <-time.After(uiWaitLimit):
			t.Fatal("查看窗口没有收到读取结果")
		}
		tt.Frame()
		tt.Advance(time.Second)
	}
	click(t, tt, "编的甲的聊天记录")
	pump()
	if !tt.HasText("编的外层") || !tt.HasText("编的引用") || !tt.HasText("[尚无本地媒体]") {
		t.Fatalf("外层内容：%v", tt.Texts())
	}
	click(t, tt, "编的乙的聊天记录")
	pump()
	if !hasExact(tt, "编的里层") || hasExact(tt, "编的外层") {
		t.Fatalf("里层内容：%v", tt.Texts())
	}
	click(t, tt, "返回")
	if !hasExact(tt, "编的外层") {
		t.Fatal("返回后外层丢了")
	}
	click(t, tt, "关闭聊天记录")
	tt.Advance(50 * time.Millisecond)
	if !hasExact(tt, "编的外层") || !tt.HasText("编的引用") {
		t.Fatalf("退场内容先清了：%v", tt.Texts())
	}
	tt.Advance(300 * time.Millisecond)
	if tt.HasText("编的引用") {
		t.Fatal("退场结束后窗口没关")
	}
}
