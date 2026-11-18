package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

func TestForwardThreeLayerDismiss(t *testing.T) {
	for _, input := range []string{"outside", "escape"} {
		t.Run(input, func(t *testing.T) {
			r := &forwardUIReader{views: map[string]api.ForwardView{}}
			for i := range 3 {
				segs := []api.DisplaySegment{{Kind: "text", Text: fmt.Sprintf("编的第%d层正文", i)}}
				if i < 2 {
					segs = append(segs, api.DisplaySegment{Kind: "forward", Ref: fmt.Sprint(i + 1), Forward: &api.ForwardBrief{Title: fmt.Sprintf("打开编的第%d层", i+1), State: "ready"}})
				}
				r.views[fmt.Sprint(i)] = api.ForwardView{ID: fmt.Sprint(i), State: "ready", Nodes: []api.ForwardNode{{Name: "编的名字", Segments: segs}}}
			}
			posts := make(chan func(), 10)
			a := &app{reader: r, postFn: func(fn func()) { posts <- fn }}
			t.Cleanup(a.stopForward)
			tt := ui.NewTester(func(c *ui.Context) { a.forwardWindow(c) }, 2048, 1030)
			tt.SetScale(1.25)
			pump := func() {
				t.Helper()
				select {
				case fn := <-posts:
					fn()
				case <-time.After(uiWaitLimit):
					t.Fatal("转发结果没回来")
				}
				tt.Frame()
				tt.Advance(time.Second)
			}
			a.openForward(api.EventRef{}, "0", nil)
			pump()
			for i := 1; i < 3; i++ {
				click(t, tt, fmt.Sprintf("打开编的第%d层", i))
				pump()
			}
			dismiss := func() {
				if input == "outside" {
					tt.ClickAt(10, 500)
				} else {
					tt.Key(0, ui.KeyEscape)
				}
				tt.Frame()
			}
			// 点面板正文不能被背景当成返回。
			click(t, tt, "编的第2层正文")
			for depth := 2; depth >= 0; depth-- {
				if !a.forward.open || len(a.forward.stack) != depth+1 || !tt.HasText(fmt.Sprintf("编的第%d层正文", depth)) {
					t.Fatalf("%s 返回层数错误：open=%v depth=%d texts=%v", input, a.forward.open, len(a.forward.stack), tt.Texts())
				}
				dismiss()
			}
			if a.forward.open || !a.forward.visible() {
				t.Fatal("最外层没有开始退场")
			}
			for range 9 {
				tt.Advance(20 * time.Millisecond)
				if !tt.HasText("编的第0层正文") {
					t.Fatal("退场期间正文提前清空")
				}
			}
			tt.Advance(50 * time.Millisecond)
			if a.forward.visible() || len(a.forward.stack) != 0 || tt.HasText("编的第0层正文") {
				t.Fatalf("退场完成后仍留着转发：open=%v visible=%v stack=%d now=%v closing=%v text=%v", a.forward.open, a.forward.visible(), len(a.forward.stack), tt.Now(), a.forward.closing, tt.HasText("编的第0层正文"))
			}
		})
	}
}

func TestForwardWindowReopensDuringExit(t *testing.T) {
	a := &app{}
	t.Cleanup(a.stopForward)
	show := func(id, text string) {
		m := imgMsg(false, id, text)
		layer := &forwardLayer{view: api.ForwardView{State: "ready"}, msgs: []*msg{m}, rows: forwardRows([]*msg{m})}
		a.forward.open, a.forward.closing, a.forward.stack = true, time.Time{}, []*forwardLayer{layer}
	}
	show("invented-first", "编造的第一份记录")
	clicks := 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Button(c, "底下的动作").OnClick(func() { clicks++ })
		a.forwardWindow(c)
	}, 900, 800)
	tt.Frame()
	tt.Advance(time.Second)
	click(t, tt, "关闭聊天记录")
	tt.Advance(50 * time.Millisecond)
	show("invented-second", "编造的第二份记录")
	tt.Frame()
	tt.Advance(time.Second)
	if !tt.HasText("编造的第二份记录") || tt.HasText("编造的第一份记录") || !a.forward.open {
		t.Fatalf("退场中重新打开丢失或混入旧内容：%v", tt.Texts())
	}
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	click(t, tt, "关闭聊天记录")
	tt.Frame()
	tt.Advance(time.Millisecond)
	click(t, tt, "底下的动作")
	if clicks != 1 || a.forward.visible() {
		t.Fatalf("关闭后仍拦输入：clicks=%d visible=%v", clicks, a.forward.visible())
	}
}

func TestForwardGIFLifecycle(t *testing.T) {
	data := gifSample(t, 256, 192, threeColors, []int{10, 10, 10}, 0)
	a, q, _ := animSetup(t, map[string][]byte{"forward-gif": data}, imgMsg(false, "background", "编的正文"))
	t.Cleanup(a.stopForward)
	m := imgMsg(false, "forward-gif-node", animJSON("forward-gif", 256, 192, false))
	layer := &forwardLayer{view: api.ForwardView{State: "ready"}, msgs: []*msg{m}, rows: forwardRows([]*msg{m})}
	a.forward.open, a.forward.stack = true, []*forwardLayer{layer}
	for i := range 60 {
		layer.msgs = append(layer.msgs, imgMsg(false, fmt.Sprintf("filler-%d", i), fmt.Sprintf("编的占位行%d", i)))
	}
	layer.rows = forwardRows(layer.msgs)
	tt := liveTester(2048, 1030, func(c *ui.Context) {
		a.chatView(c)
		// 编一个与转发无关的正文可见范围，确保旧的正文门槛会拦住 GIF。
		a.visFrame, a.visFirst, a.visLast, a.rowAt = a.is().frame, 80, 90, 0
		a.forwardWindow(c)
	})
	tt.SetScale(1.25)
	st := a.forward.imgs
	st.wg.Wait()
	q.drain()
	tt.Frame()
	e := st.peek(imgKey{"qq/forward-gif", 256, true})
	if e == nil || e.anim == nil {
		t.Fatal("转发里的 GIF 没有解成多帧")
	}
	tt.Advance(110 * time.Millisecond)
	if e.anim.cur != 1 || st.updates != 1 {
		t.Fatalf("转发 GIF 没推进：frame=%d updates=%d", e.anim.cur, st.updates)
	}
	// 正文列表的可见范围与转发列表完全不同，不能影响转发播放。
	before := st.updates
	tt.Advance(100 * time.Millisecond)
	if e.anim.cur != 2 || st.updates != before+1 {
		t.Fatalf("转发播放误用正文范围：frame=%d updates=%d", e.anim.cur, st.updates)
	}
	layer.list.ScrollToEnd()
	tt.Frame()
	tt.Frame()
	before = st.updates
	tt.Advance(time.Second)
	if st.updates != before || tt.FramePending() {
		t.Fatal("转发动图滚出可见范围后仍播放")
	}
	layer.list.ScrollTo(0, ui.Start)
	tt.Frame()
	tt.Frame()
	click(t, tt, "图片")
	if _, ok := tt.Find("图片·大图"); !ok {
		t.Fatal("转发图片没有打开大图")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if !a.forward.open || len(a.forward.stack) != 1 {
		t.Fatal("大图的 Esc 越过大图关闭了转发")
	}
	// 点大图遮罩的空白处同样只关大图（大图是顶层 Overlay，转发面板不能把这下当成点了外面）。
	click(t, tt, "图片")
	if _, ok := tt.Find("图片·大图"); !ok {
		t.Fatal("转发图片没有再次打开大图")
	}
	tt.Frame()
	tt.Advance(600 * time.Millisecond) // 等入场放完，照 viewer_test 的"点遮罩"
	tt.ClickAt(20, 500)
	tt.Frame()
	tt.Advance(time.Second)
	if _, ok := tt.Find("图片·大图"); ok {
		t.Fatal("点大图空白没有关掉大图")
	}
	if !a.forward.open || len(a.forward.stack) != 1 {
		t.Fatal("点大图空白越过大图关闭了转发")
	}
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	tt.Frame()
	if e.anim.cur != 0 {
		t.Fatal("减少动态效果没有回首帧")
	}
	tt.SetPreferences(ui.Preferences{TextScale: 1})
	tt.Frame()
	ctx := st.ctx
	cached := st.bytes
	click(t, tt, "关闭聊天记录")
	tt.Advance(50 * time.Millisecond)
	if a.forward.imgs != st || st.bytes != cached || ctx.Err() != nil {
		t.Fatal("退场期间缓存提前释放")
	}
	tt.Advance(250 * time.Millisecond)
	before = st.updates
	tt.Advance(5 * time.Second)
	if st.updates != before || tt.FramePending() || a.forward.imgs != nil || ctx.Err() != context.Canceled {
		t.Fatalf("关窗后仍播放或持有缓存：updates=%d→%d pending=%v cached=%v ctx=%v", before, st.updates, tt.FramePending(), a.forward.imgs != nil, ctx.Err())
	}
	t.Logf("GIF file=%d B decoded=%d B frame updates=%d; exit cache=0 B; closed updates=0", len(data), cached, before)
}
