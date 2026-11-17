package main

// 表情包保留元数据几何、透明底、实时展开和手动重试；布局由 MewTypeUI 决定。
// mface 单独成块，全部使用合成数据及内存图片。

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

func stickerJSON(hash string, w, h int, extra string) string {
	return fmt.Sprintf(`{"type":"image","hash":%q,"width":%d,"height":%d,"sticker":true%s}`, hash, w, h, extra)
}

// 已知/未知尺寸及商城贴纸在加载到达前后都保持位置与尺寸。
func TestStickerGeometryStableAcrossLoad(t *testing.T) {
	cases := []struct {
		name string
		js   string
		orig [2]int
	}{
		{"宽扁的大图", stickerJSON("s", 300, 100, ""), [2]int{300, 100}},
		{"比框小的图不缩小框", stickerJSON("s", 40, 40, ""), [2]int{40, 40}},
		{"没存宽高", `{"type":"image","hash":"s","sticker":true}`, [2]int{200, 200}},
		{"商城表情图", stickerJSON("s", 240, 240, `,"market":true`), [2]int{240, 240}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &imgNet{orig: map[string][2]int{"s": c.orig}}
			a, q := imgApp(t, n, imgMsg(false, "a", c.js))
			tt := testerOf(900, 600, func(c *ui.Context) { a.chatView(c) })
			// 图到之前和到之后框一样大（不跳）
			before, ok := tt.Find("表情包")
			if !ok {
				t.Fatalf("没画出来：%q", tt.Texts())
			}
			settleImgs(a, q, tt)
			after, _ := tt.Find("表情包")
			if before != after || after.W <= 0 || after.H <= 0 {
				t.Fatalf("贴纸到达后版面改变：%+v → %+v", before, after)
			}
		})
	}
}

// 图片透明边缘应露出页面底色，中间仍显示原图。
func TestStickerTransparent(t *testing.T) {
	bitmap := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 10; y < 90; y++ {
		for x := 10; x < 190; x++ {
			bitmap.SetRGBA(x, y, opaqueRed)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, bitmap); err != nil {
		t.Fatal(err)
	}
	n := &imgNet{files: map[string][]byte{"s": encoded.Bytes()}}
	a, q := imgApp(t, n, imgMsg(false, "a", stickerJSON("s", 200, 100, "")))
	tt := testerOf(900, 600, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	r, _ := tt.Find("表情包")
	img := tt.Image()
	s := float32(img.Bounds().Dx()) / 900
	px := func(x, y float32) (r8, g8, b8 uint8) {
		c := img.RGBAAt(int(x*s), int(y*s))
		return c.R, c.G, c.B
	}
	bgR, bgG, bgB := px(2, 2)
	for _, p := range [][2]float32{{r.X + 3, r.Y + 3}, {r.X + r.W - 4, r.Y + 3}, {r.X + 3, r.Y + r.H - 4}, {r.X + r.W - 4, r.Y + r.H - 4}} {
		if cr, cg, cb := px(p[0], p[1]); cr != bgR || cg != bgG || cb != bgB {
			t.Fatalf("框的角 %v 不是页面底色：(%d,%d,%d)，页面 (%d,%d,%d)；表情包不该有底或气泡", p, cr, cg, cb, bgR, bgG, bgB)
		}
	}
	if cr, cg, cb := px(r.X+r.W/2, r.Y+r.H/2); cr < 200 || cg > 60 || cb > 60 {
		t.Fatalf("框中心不是图的红色：(%d,%d,%d)", cr, cg, cb)
	}
}

// 实时入场展开布局空间；历史直接完整显示。图像本身不缩放，按行高验证。
func TestStickerArriveOnlyWhenLive(t *testing.T) {
	build := func(live bool) *ui.Tester {
		n := &imgNet{orig: map[string][2]int{"s": {120, 120}}}
		m := imgMsg(false, "sticker", stickerJSON("s", 120, 120, ""))
		m.seq, m.live, m.at = 3, live, base0.UnixMilli()+3
		a, q := imgApp(t, n, oldOnes()...)
		tt := animTester(a, false, nil)
		tt.Advance(time.Second)
		a.msgs = append(a.msgs, m)
		tt.Frame()
		settleImgs(a, q, tt)
		return tt
	}
	old, live := build(false), build(true)
	old.Advance(20 * time.Millisecond)
	live.Advance(20 * time.Millisecond)
	full, exists := old.Find("表情包")
	partial, opening := live.Find("新消息展开")
	if !exists || !opening || partial.H <= 0 || partial.H >= full.H {
		t.Fatalf("历史完整框 %+v，实时展开框 %+v opening=%v", full, partial, opening)
	}
	live.Advance(time.Second)
	after, exists := live.Find("表情包")
	if !exists || after.W != full.W || after.H != full.H {
		t.Fatalf("入场后尺寸不一致：历史 %+v，实时 %+v", full, after)
	}
}

// 取不到：框里写说明，点一下重取；重取到了图画出来
func TestStickerFailRetry(t *testing.T) {
	n := &imgNet{}
	a, q := imgApp(t, n, imgMsg(false, "a", stickerJSON("s", 120, 120, "")))
	tt := testerOf(900, 600, func(c *ui.Context) { a.chatView(c) })
	settleImgs(a, q, tt)
	if !tt.HasText("图片没取到，点一下再试") {
		t.Fatalf("没有说明：%q", tt.Texts())
	}
	for range 3 { // 不自动重试
		tt.Frame()
	}
	if got := len(n.tiers("s")); got != 1 {
		t.Fatalf("没点就请求了 %d 次", got)
	}
	n.mu.Lock()
	n.orig = map[string][2]int{"s": {120, 120}}
	n.mu.Unlock()
	if err := tt.Click("表情包"); err != nil {
		t.Fatal(err)
	}
	settleImgs(a, q, tt)
	if tt.HasText("图片没取到") || len(n.tiers("s")) != 2 {
		t.Fatalf("重取以后：请求 %d 次，文字 %q", len(n.tiers("s")), tt.Texts())
	}
}

// shellGroups：无本地媒体的商城表情各占一个壳，其余连着的合成一个，顺序不变
func TestShellGroups(t *testing.T) {
	t.Parallel()
	in := segview.BlocksOf(segmentViews(displaySegs(api.DisplaySegment{Kind: "text", Text: "甲"}, api.DisplaySegment{Kind: "voice"}, api.DisplaySegment{Kind: "emoji", Ref: "market-a", Name: "[a]"}, api.DisplaySegment{Kind: "emoji", Ref: "market-b", Name: "[b]"}, api.DisplaySegment{Kind: "text", Text: "乙"})))
	var got [][]string
	for _, g := range shellGroups(in) {
		got = append(got, descr(g))
	}
	want := [][]string{{"行内:甲", "占位:[语音]"}, {"表情块:[a]"}, {"表情块:[b]"}, {"行内:乙"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("得 %q，要 %q", got, want)
	}
	if len(shellGroups(nil)) != 0 {
		t.Fatal("没有块应没有壳")
	}
}

// mface 自己一个小壳：前后的字不和它挤在同一个气泡里（旧代码三块在一个壳里，间距只有 4 DIP）
func TestMfaceOwnShell(t *testing.T) {
	for _, mine := range []bool{false, true} {
		t.Run(fmt.Sprint("mine=", mine), func(t *testing.T) {
			m := imgMsg(mine, "a", "看这个", `{"type":"mface","name":"[挥手]"}`, "好吧")
			a := chatApp([]*msg{m}, false)
			tt := testerOf(900, 600, func(c *ui.Context) { a.chatView(c) })
			r1, ok1 := tt.Find("看这个")
			r2, ok2 := tt.Find("[挥手]")
			r3, ok3 := tt.Find("好吧")
			if !ok1 || !ok2 || !ok3 {
				t.Fatalf("三块没都画出来：%q", tt.Texts())
			}
			// 壳的上下内边距各 8，所以隔开的两块文字至少差 16 DIP 再加行间距
			if g := r2.Y - (r1.Y + r1.H); g < 16 {
				t.Fatalf("[挥手] 和前面的字只隔 %v DIP，还在同一个气泡里", g)
			}
			if g := r3.Y - (r2.Y + r2.H); g < 16 {
				t.Fatalf("[挥手] 和后面的字只隔 %v DIP，还在同一个气泡里", g)
			}
		})
	}
}

// 只有 mface 的消息：显示灰字，不 panic；末尾的小字（被打断）跟在最后一个壳里
func TestMfaceAlone(t *testing.T) {
	m := imgMsg(false, "a", `{"type":"mface"}`)
	m.aborted = true
	a := chatApp([]*msg{m}, false)
	tt := testerOf(900, 600, func(c *ui.Context) { a.chatView(c) })
	if !tt.HasText("[表情包]") || !tt.HasText("被打断") {
		t.Fatalf("文字 %q", tt.Texts())
	}
}
