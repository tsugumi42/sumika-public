package main

// 第三步的测试：T-anim-1（入场判定，纯函数）、订阅并进（applySnapshot、mergeLive）、T-anim-2（tester：位移在途、流式不回到起点）、
// T-anim-3（减少动态效果）。数据全是编的；不连 7720，不读 data/。

import (
	"fmt"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

var base0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)

func atMs(ms int) time.Time { return base0.Add(time.Duration(ms) * time.Millisecond) }

func liveMsg(id string, seq int64) *msg {
	return &msg{id: id, seq: seq, live: true, at: base0.UnixMilli() + seq}
}
func oldMsg(id string, seq int64) *msg { return &msg{id: id, seq: seq, at: base0.UnixMilli() + seq} }

// ───────────── 轮询并进 ─────────────

func pollApp() *app {
	a := chatApp([]*msg{oldMsg("a", 1), oldMsg("b", 2), oldMsg("c", 3)}, false)
	a.sel, a.gen, a.firstDone = "qq:group:1", 1, true
	return a
}

func ids(ms []*msg) string {
	s := ""
	for _, m := range ms {
		s += m.id + " "
	}
	return s
}

func TestApplyPoll(t *testing.T) {
	t.Parallel()
	t.Run("新的标 live，已有的不动、不重复，按时间插到位置上", func(t *testing.T) {
		a := pollApp()
		have := a.msgs[2]
		fresh := &msg{id: "d", seq: 4, at: have.at + 1000}
		fill := &msg{id: "f", seq: 99, at: a.msgs[0].at} // 补录的老消息：序号最大，时间和 a 同一毫秒、早于 b
		same := &msg{id: "c", seq: 3, at: have.at}
		n := a.applySnapshot(1, pageResult{msgs: []*msg{same, fresh, fresh, fill}}, pageResult{}, nil)
		if n != 2 || ids(a.msgs) != "a f b c d " {
			t.Fatalf("加了 %d 条，顺序 %s", n, ids(a.msgs))
		}
		if have.live || same.live || !fresh.live || !fill.live {
			t.Fatalf("live 标记不对：c=%v 轮询的 c=%v d=%v f=%v", have.live, same.live, fresh.live, fill.live)
		}
		if a.msgs[3] != have {
			t.Fatal("已有的消息被换成了轮询取回的那份")
		}
	})
	t.Run("会话换过（代次对不上，包括换走再换回来）就丢", func(t *testing.T) {
		a := pollApp()
		r := pageResult{msgs: []*msg{oldMsg("x", 7)}}
		if a.applySnapshot(9, r, pageResult{}, nil) != 0 || a.applySnapshot(0, r, pageResult{}, nil) != 0 || len(a.msgs) != 3 {
			t.Fatal("过期的轮询结果写进去了")
		}
		a.gen = 2 // 换走又换回同一个 key：key 一样，代次不同
		if a.applySnapshot(1, r, pageResult{}, nil) != 0 || len(a.msgs) != 3 {
			t.Fatal("同一个 key 换回来以后，旧代次的结果写进去了")
		}
	})
	t.Run("取失败不动列表、不改状态栏", func(t *testing.T) {
		a := pollApp()
		if a.applySnapshot(1, pageResult{}, pageResult{}, nil) != 0 || a.status != "" || len(a.msgs) != 3 {
			t.Fatalf("取失败改了状态：%q", a.status)
		}
	})
	t.Run("撤回通知和说过话的角色并进会话的集合", func(t *testing.T) {
		a := pollApp()
		ch := liveMsg("k", 5)
		ch.isChar, ch.actor = true, "aoi"
		a.applySnapshot(1, pageResult{msgs: []*msg{ch}, gone: map[string]bool{"a": true}}, pageResult{}, nil)
		if !a.recalled["a"] || !a.speakers["aoi"] {
			t.Fatalf("没并进去：%v %v", a.recalled, a.speakers)
		}
	})
	t.Run("乱序落地：两次轮询先后顺序不同，列表一样", func(t *testing.T) {
		mk := func(order [][]string) string {
			a := pollApp()
			for _, g := range order {
				var got []*msg
				for _, id := range g {
					got = append(got, map[string]*msg{
						"d": {id: "d", seq: 4, at: base0.UnixMilli() + 100},
						"e": {id: "e", seq: 5, at: base0.UnixMilli() + 200},
					}[id])
				}
				a.applySnapshot(1, pageResult{msgs: got}, pageResult{}, nil)
			}
			return ids(a.msgs)
		}
		x, y := mk([][]string{{"d", "e"}, {"e"}}), mk([][]string{{"e"}, {"d", "e"}})
		if x != y || x != "a b c d e " {
			t.Fatalf("先后顺序不同结果不同：%q %q", x, y)
		}
	})
}

// 订阅推来的行：版本更新才替换（撤回、发送结果这类没有新 seq 的变化），旧版本不能把新状态改回去
func TestSnapshotRowVersion(t *testing.T) {
	t.Parallel()
	a := pollApp()
	a.msgs[1].version = 5
	newer := &msg{id: "b", seq: 2, at: a.msgs[1].at, version: 6, recalled: true}
	older := &msg{id: "b", seq: 2, at: a.msgs[1].at, version: 4}
	a.applySnapshot(1, pageResult{}, pageResult{msgs: []*msg{newer}}, nil)
	if a.msgs[1] != newer || !a.recalled["b"] {
		t.Fatal("新版本的行没换上")
	}
	a.applySnapshot(1, pageResult{msgs: []*msg{older}}, pageResult{}, nil)
	if a.msgs[1] != newer {
		t.Fatal("旧版本把新状态改回去了")
	}
}

// 取页（takePage）来的消息一律不是 live：前插、整页换掉都一样
func TestTakePageNotLive(t *testing.T) {
	t.Parallel()
	a := pollApp()
	a.takePage([]*msg{oldMsg("h", 0)}, nil, true)
	a.takePage([]*msg{oldMsg("n1", 8), oldMsg("n2", 9)}, nil, false)
	for _, m := range a.msgs {
		if m.live {
			t.Fatalf("取页来的 %s 是 live", m.id)
		}
	}
}

// 换会话：代次加一、水位线和在途表清零、第一页标记清掉
func TestSwitchTo(t *testing.T) {
	t.Parallel()
	a := pollApp()
	a.arr.enter(liveMsg("x", 77), atMs(0), false)
	gen := a.gen
	a.switchTo("qq:group:2")
	if a.gen != gen+1 || a.firstDone || a.arr.seen != 0 || len(a.arr.flying) != 0 || len(a.msgs) != 0 {
		t.Fatalf("没重置干净：gen %d 第一页 %v 水位线 %d 在途 %d 消息 %d", a.gen, a.firstDone, a.arr.seen, len(a.arr.flying), len(a.msgs))
	}
}

// ───────────── T-anim-2、T-anim-3（tester） ─────────────

const winW = 900

// 一个只有聊天列表的窗口。before 在每次构建前先跑一次（滚动要在视图里调）
func animTester(a *app, reduce bool, before func()) *ui.Tester {
	tt := ui.NewTester(func(c *ui.Context) {
		if before != nil {
			before()
		}
		a.chatView(c)
	}, winW, 700)
	tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
	tt.Frame()
	return tt
}

// 一行文字所在的那一块图（文字盒子外扩 16 像素，含气泡）
func crop(tt *ui.Tester, text string) *image.RGBA { return cropPad(tt, text, 16) }

// cropPad 外扩 pad 个 DIP。比老行时用小的，免得把下面正在淡入的新气泡的边也框进来
func cropPad(tt *ui.Tester, text string, pad float32) *image.RGBA {
	r, ok := tt.Find(text)
	if !ok {
		return nil
	}
	img := tt.Image()
	s := float32(img.Bounds().Dx()) / winW
	box := image.Rect(int((r.X-pad)*s), int((r.Y-pad)*s), int((r.X+r.W+pad)*s), int((r.Y+r.H+pad)*s)).Intersect(img.Bounds())
	out := image.NewRGBA(image.Rect(0, 0, box.Dx(), box.Dy()))
	for y := 0; y < box.Dy(); y++ {
		for x := 0; x < box.Dx(); x++ {
			out.SetRGBA(x, y, img.RGBAAt(box.Min.X+x, box.Min.Y+y))
		}
	}
	return out
}

// 两块图不同的字节数；大小不同或有一块没画出来是 -1
func diff(a, b *image.RGBA) int {
	if a == nil || b == nil || a.Bounds() != b.Bounds() {
		return -1
	}
	n := 0
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			n++
		}
	}
	return n
}

func withText(m *msg, text string) *msg { m.text, m.blocks = text, segview.TextBlocks(text); return m }

func oldOnes() []*msg {
	return []*msg{withText(oldMsg("a", 1), "第一句老消息"), withText(oldMsg("b", 2), "第二句老消息")}
}

// 终点的样子：同样的消息都不是 live、从一开始就在终点，等够久
func settledCrop(t *testing.T, ms []*msg, text string) *image.RGBA {
	t.Helper()
	tt := animTester(chatApp(ms, false), false, nil)
	tt.Advance(time.Second)
	img := crop(tt, text)
	if img == nil {
		t.Fatalf("终点没画出来：%q", tt.Texts())
	}
	return img
}

func TestAnimOnScreen(t *testing.T) {
	a := chatApp(oldOnes(), false)
	p := newProbes(a)
	tt := ui.NewTester(func(c *ui.Context) { p.ctx = c; a.chatView(c) }, 900, 700)
	tt.Frame()
	tt.Advance(time.Second)
	a.msgs, _ = mergeLive(a.msgs, []*msg{withText(&msg{id: "n", seq: 3, at: base0.UnixMilli() + 3}, "新来的一句")})
	tt.Frame()
	tt.Frame()
	start := p.rect(t, "reveal:n").H
	tt.Advance(75 * time.Millisecond)
	tt.Frame()
	mid := p.rect(t, "reveal:n").H
	tt.Advance(75 * time.Millisecond)
	tt.Frame()
	end := p.box(t, "shell:n:0").H
	if start != 0 || mid <= 0 || mid >= end {
		t.Fatalf("150ms展开高度 start=%.3f mid=%.3f end=%.3f", start, mid, end)
	}
	t.Logf("Reveal高度起/中/终=%.3f/%.3f/%.3f", start, mid, end)
}

// 气泡里、文字左边 6 DIP 处的一个像素：那里是气泡的底色，颜色只随入场的透明度变（文字内容变了会有交叉淡入，整块比图会被它干扰）
func bubblePx(tt *ui.Tester, text string) color.RGBA {
	r, _ := tt.Find(text)
	img := tt.Image()
	s := float32(img.Bounds().Dx()) / winW
	return img.RGBAAt(int((r.X-6)*s), int((r.Y+r.H/2)*s))
}

// 流式：同一条消息改文字重建以后，入场还在原来的进度上——不跳到终点，也不回到起点。
// 对照是同一时刻只重建、不改字的那一条
func TestAnimStreamingKeeps(t *testing.T) {
	a := chatApp(oldOnes(), false)
	p := newProbes(a)
	tt := ui.NewTester(func(c *ui.Context) { p.ctx = c; a.chatView(c) }, 900, 700)
	tt.Frame()
	n := withText(&msg{id: "n", seq: 3, at: base0.UnixMilli() + 3}, "流式开头")
	a.msgs, _ = mergeLive(a.msgs, []*msg{n})
	tt.Frame()
	tt.Frame()
	tt.Advance(40 * time.Millisecond)
	tt.Frame()
	before := p.rect(t, "reveal:n").H
	n.blocks = segview.TextBlocks("流式开头，继续收到内容")
	tt.Frame()
	tt.Frame()
	after := p.rect(t, "reveal:n").H
	if before <= 0 || !approx(before, after, 0.5) {
		t.Fatalf("流式重启展开 %.3f→%.3f", before, after)
	}
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("继续收到内容") {
		t.Fatal("流式正文没有更新")
	}
}

// 新到的行滚出去、又在 200 到 300 毫秒之间滚回：第五节第 33 条的边界，记录实际表现，不断言（预期从头播一次）
func TestAnimScrollOutAndBack(t *testing.T) {
	var ms []*msg
	for i := 1; i <= 60; i++ {
		ms = append(ms, withText(oldMsg(fmt.Sprint("o", i), int64(i)), fmt.Sprintf("老消息第 %d 条，字多一点免得一行放得下两条", i)))
	}
	a := chatApp(ms, false)
	var act func()
	tt := animTester(a, false, func() {
		if act != nil {
			f := act
			act = nil
			f()
		}
	})
	a.msgs, _ = mergeLive(a.msgs, []*msg{withText(&msg{id: "n", seq: 61, at: base0.UnixMilli() + 61}, "最新来的一句")})
	tt.Frame()
	settled := settledCrop(t, append(append([]*msg{}, ms...), withText(oldMsg("n", 61), "最新来的一句")), "最新来的一句")
	if crop(tt, "最新来的一句") == nil {
		t.Fatalf("新消息没画出来")
	}
	act = func() { a.list.ScrollTo(0, ui.Start) }
	tt.Frame()
	tt.Advance(100 * time.Millisecond)
	if _, ok := tt.Find("最新来的一句"); ok {
		t.Fatal("没滚出去，测不了")
	}
	tt.Advance(120 * time.Millisecond) // 共 220 毫秒
	act = func() { a.list.ScrollToEnd() }
	tt.Frame()
	tt.Frame()
	back := crop(tt, "最新来的一句")
	if back == nil {
		t.Fatalf("没滚回来：%q", tt.Texts())
	}
	t.Logf("220 毫秒滚回：与终点相差 %d 个字节（0 是没重播，非 0 是重播了一次）；在途表 %d 条", diff(back, settled), len(a.arr.flying))
	tt.Advance(time.Second)
	if d := diff(crop(tt, "最新来的一句"), settled); d != 0 {
		t.Fatalf("再过一秒仍不在终点：%d", d)
	}
}

// 减少动态效果：消息不发起入场，水位线照推。顶栏副标题（Caption）的条数变了会淡一下，那是一次性的：
// 过一秒窗口就不能再要帧（防止有东西一直在重画、空闲时也吃 CPU）
func TestAnimReduceMotion(t *testing.T) {
	a := chatApp(oldOnes(), false)
	tt := animTester(a, true, nil)
	a.msgs, _ = mergeLive(a.msgs, []*msg{withText(&msg{id: "n", seq: 3, at: base0.UnixMilli() + 3}, "新来的一句")})
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("减少动态效果时，一秒以后窗口还在要帧")
	}
	if len(a.arr.flying) != 0 || a.arr.seen != 3 {
		t.Fatalf("在途表 %d 条，水位线 %d", len(a.arr.flying), a.arr.seen)
	}
}
