package chatui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

func fitHost() Host {
	return Host{T: Derive(DefaultSettings()), Column: 800, NoEnter: true, MD: NewMD()}
}

func fitMsg(id, text string, md bool) *Msg {
	return &Msg{ID: id, Self: true, Markdown: md, Text: text, At: time.Date(2026, 11, 8, 11, 36, 0, 0, time.Local),
		Blocks: segview.TextBlocks(text)}
}

func bubbleRect(t *testing.T, h Host, m *Msg) ui.Rect {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) { h.Message(c, m, nil, nil) }, 900, 700)
	tt.Frame()
	r, ok := tt.Find("气泡")
	if !ok {
		t.Fatal("没找到气泡")
	}
	return r
}

// 分段消息（宿主现在都走这条）的气泡宽度跟随内容；时间挂在最后一行，短消息只有单行高。
func TestBlockBubbleShrinksToContent(t *testing.T) {
	long := "这是一段很长很长的话，" + "它需要折成好几行才能放进气泡里面去，" + "不然就超出最大宽度了，" + "继续写下去直到足够长为止，再多写一点点。"
	for _, md := range []bool{false, true} {
		h := fitHost()
		short := bubbleRect(t, h, fitMsg("s", "早", md))
		lng := bubbleRect(t, h, fitMsg("l", long, md))
		maxW := h.Column * h.T.BubbleShare
		t.Logf("md=%v short=%.1fx%.1f long=%.1fx%.1f max=%.1f", md, short.W, short.H, lng.W, lng.H, maxW)
		if short.W > 120 {
			t.Fatalf("md=%v 短消息气泡宽 %.1f，应该只比“早”加时间宽一点", md, short.W)
		}
		if lng.W <= short.W*2 || lng.W > maxW+0.5 {
			t.Fatalf("md=%v 长消息气泡宽 %.1f，应在 (短消息×2, %.1f] 里", md, lng.W, maxW)
		}
		// 时间和文字同一行：气泡高度 = 上下内边距 + 一行字高
		if want := h.T.BubblePadV*2 + h.T.BodyLine; short.H > want+1 {
			t.Fatalf("md=%v 短消息气泡高 %.1f，时间多占了一行（单行应为 %.1f）", md, short.H, want)
		}
	}
}

// 最后一行快满：时间换到下一行，靠右。
func TestBlockBubbleTimeWrapsWhenLastLineFull(t *testing.T) {
	h := fitHost()
	maxW := h.Column * h.T.BubbleShare
	textW := maxW - h.T.BubblePadH*2
	// 一行写到离右边只剩约 10 DIP，放不下时间
	text := ""
	for {
		text += "字"
		ws := shapeLines(text+"字", h.Font, h.T.Body, 1.6, 1e6)
		if ws[0] > textW-10 {
			break
		}
	}
	m := fitMsg("f", text, false)
	r := bubbleRect(t, h, m)
	one := bubbleRect(t, h, fitMsg("o", "早", false))
	if r.H < one.H+8 {
		t.Fatalf("最后一行快满，时间应换行：气泡高 %.1f，单行带时间是 %.1f", r.H, one.H)
	}
}

// 以图片结尾：时间另起一行（在图片下面），气泡宽度跟图片，不拉满。
func TestBlockBubbleImageEnd(t *testing.T) {
	h := fitHost()
	h.Media = &blockMedia{state: MediaResult{State: MediaLoading}}
	m := &Msg{ID: "i", Self: true, At: time.Now(), Blocks: []segview.Block{
		{Kind: segview.Inline, Runs: []segview.Run{{Text: "看"}}},
		{Kind: segview.Images, Imgs: []segview.Segment{{Position: 1, Width: 100, Height: 100}}},
	}}
	r := bubbleRect(t, h, m)
	a := albumLayout(m.Blocks[1].Imgs, 1e6) // 图不在最前面也按相册排法占位（缩在内边距里）
	w, ht := a.W, a.H
	if r.W > w+h.T.BubblePadV*2+1 || r.W < w {
		t.Fatalf("气泡宽 %.1f，应跟图宽 %.1f", r.W, w)
	}
	want := h.T.BubblePadV*2 + h.T.BodyLine + h.T.S2 + ht + (h.T.BubblePadH-h.T.BubblePadV)*0
	if r.H < want+8 { // 时间行至少几个像素
		t.Fatalf("图片结尾的消息高 %.1f，应比文字加图 %.1f 多出时间行", r.H, want)
	}
}
