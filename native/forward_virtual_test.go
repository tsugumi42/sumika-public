package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"testing"
	"time"
)

func TestForwardWindowBuildsVisibleBlocks(t *testing.T) {
	nodes := make([]api.ForwardNode, 200)
	for i := range nodes {
		nodes[i].Name = fmt.Sprintf("编的节点%d", i)
		nodes[i].UID = fmt.Sprint(i)
		for j := range 100 {
			nodes[i].Segments = append(nodes[i].Segments, api.DisplaySegment{Kind: "file", Name: fmt.Sprintf("编的块%d-%d", i, j)})
		}
	}
	r := &forwardUIReader{views: map[string]api.ForwardView{"large": {ID: "large", State: "ready", Nodes: nodes}}}
	posts := make(chan func(), 10)
	a := &app{reader: r, postFn: func(fn func()) { posts <- fn }}
	t.Cleanup(a.stopForward)
	tt := ui.NewTester(func(c *ui.Context) { a.forwardWindow(c) }, 2048, 1030)
	tt.SetScale(1.25)
	a.openForward(api.EventRef{}, "large", nil)
	select {
	case fn := <-posts:
		fn()
	case <-time.After(uiWaitLimit):
		t.Fatal("没有读取树")
	}
	tt.Frame()
	if !tt.HasText("编的块0-0") {
		t.Fatal("首屏没有第一块")
	}
	if tt.HasText("编的块0-99") || tt.HasText("编的节点199") {
		t.Fatal("首屏构建了节点内或整棵树的离屏内容")
	}
	// 在同一节点里继续往下滚，后面的块应能出现。
	for i := 0; i < 30 && !tt.HasText("编的块0-99"); i++ {
		tt.Scroll(1024, 500, 0, 500)
		tt.Frame()
	}
	if !tt.HasText("编的块0-99") {
		t.Fatalf("分块后后半节点无法滚到：%v", tt.Texts())
	}
	if tt.HasText("编的节点199") {
		t.Fatal("滚到第一节点尾部却构建了最后节点")
	}
}
