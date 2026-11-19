package main

import (
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

func flashApp(n int) (*app, *ui.Tester) {
	a := &app{sel: "c", gen: 1, srcGen: 1}
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("第 %d 条消息", i)
		a.msgs = append(a.msgs, &msg{id: fmt.Sprintf("m%d", i), who: "访客", text: text, blocks: segview.TextBlocks(text), at: t0 + int64(i)*60000, version: 1})
	}
	a.list = a.newList()
	tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, 900, 600)
	tt.SetPreferences(ui.Preferences{ReduceMotion: false})
	tt.Frame()
	tt.Frame()
	return a, tt
}

// diffIn 两帧图片不同的像素：返回个数、总差值最大的那个点和它的差值；差异只统计 within 范围内外的数量。
func diffImages(a, b *image.RGBA, within image.Rectangle) (inside, outside int, at image.Point, peak int) {
	bounds := a.Bounds().Intersect(b.Bounds())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			p, q := a.RGBAAt(x, y), b.RGBAAt(x, y)
			d := abs(int(p.R)-int(q.R)) + abs(int(p.G)-int(q.G)) + abs(int(p.B)-int(q.B))
			if d == 0 {
				continue
			}
			if (image.Point{X: x, Y: y}).In(within) {
				inside++
			} else {
				outside++
			}
			if d > peak {
				peak, at = d, image.Point{X: x, Y: y}
			}
		}
	}
	return
}

func pixelDiff(a, b *image.RGBA, at image.Point) int {
	p, q := a.RGBAAt(at.X, at.Y), b.RGBAAt(at.X, at.Y)
	return abs(int(p.R)-int(q.R)) + abs(int(p.G)-int(q.G)) + abs(int(p.B)-int(q.B))
}

func snapshot(tt *ui.Tester) *image.RGBA {
	img := tt.Image()
	cp := image.NewRGBA(img.Bounds())
	copy(cp.Pix, img.Pix)
	return cp
}

// 点引用（jumpTo）后：目标滚到可见区；滚到位并等一小会儿以后，气泡外出现一圈强调色，
// 前一半时间满强度，后一半淡出，到点消失；强调只出现在目标那条消息的周围。
func TestJumpFlashTimeline(t *testing.T) {
	a, tt := flashApp(60)
	a.jumpTo("m5")
	for i := 0; i < 4; i++ {
		tt.Frame()
	}
	row, ok := tt.Find("消息：m5")
	if !ok {
		t.Fatalf("目标没有滚进可见区：%v", tt.Texts())
	}
	list, _ := tt.Find("聊天记录")
	if row.Y < list.Y-1 || row.Y+row.H > list.Y+list.H+1 {
		t.Fatalf("目标 %+v 不在聊天区 %+v 里", row, list)
	}
	zone := image.Rect(int(row.X)-8, int(row.Y)-8, int(row.X+row.W)+8, int(row.Y+row.H)+8)
	base := snapshot(tt) // 刚滚到位，等待期还没过：没有强调
	if a.flashID != "m5" {
		t.Fatalf("强调目标 %q", a.flashID)
	}

	tt.Advance(flashSettle + 40*time.Millisecond) // 开始，满强度
	tt.Frame()
	hold := snapshot(tt)
	in, out, peak, strength := diffImages(base, hold, zone)
	if in == 0 {
		t.Fatal("强调开始后目标周围没有变化")
	}
	if out != 0 {
		t.Fatalf("强调在目标之外也画了 %d 个像素", out)
	}

	tt.Advance(workbenchFlash/2 + 100*time.Millisecond) // 过了一半：淡出中
	tt.Frame()
	fade := snapshot(tt)
	if got := pixelDiff(base, fade, peak); got <= 0 || got >= strength {
		t.Fatalf("淡出中强调强度应介于 0 和满强度之间：满 %d，现在 %d", strength, got)
	}

	tt.Advance(workbenchFlash) // 结束
	tt.Frame()
	tt.Frame()
	end := snapshot(tt)
	if in, out, _, _ := diffImages(base, end, zone); in+out != 0 {
		t.Fatalf("到点以后强调还在：%d 个像素和原来不同", in+out)
	}
	if a.flashID != "" {
		t.Fatalf("强调结束后目标没有清掉：%q", a.flashID)
	}
}

// 目标还没滚到可见区时不开始计时：先滚过去，滚到位再等。
func TestJumpFlashWaitsForScroll(t *testing.T) {
	a, tt := flashApp(60)
	a.flashID, a.flashAt = "m5", time.Time{}
	// 没有 ScrollTo：m5 在可见区外（列表停在底部）
	for i := 0; i < 5; i++ {
		tt.Advance(300 * time.Millisecond)
		tt.Frame()
	}
	if a.flashID != "m5" || !a.flashAt.IsZero() {
		t.Fatalf("目标不在可见区，强调不该开始或结束：id=%q at=%v", a.flashID, a.flashAt)
	}
	a.list.ScrollTo(a.indexOf("m5"), ui.Center)
	tt.Frame()
	tt.Frame()
	tt.Advance(flashSettle + 50*time.Millisecond)
	tt.Frame()
	if a.flashAt.IsZero() {
		t.Fatal("滚到位以后强调没有开始")
	}
}

// 减少动态效果：不淡出，满强度到点直接消失。
func TestJumpFlashReduceMotion(t *testing.T) {
	a, tt := flashApp(60)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	a.jumpTo("m5")
	for i := 0; i < 4; i++ {
		tt.Frame()
	}
	row, _ := tt.Find("消息：m5")
	zone := image.Rect(int(row.X)-8, int(row.Y)-8, int(row.X+row.W)+8, int(row.Y+row.H)+8)
	base := snapshot(tt)
	tt.Advance(flashSettle + 40*time.Millisecond)
	tt.Frame()
	_, _, peak, strength := diffImages(base, snapshot(tt), zone)
	tt.Advance(workbenchFlash - 200*time.Millisecond)
	tt.Frame()
	if got := pixelDiff(base, snapshot(tt), peak); got != strength {
		t.Fatalf("减少动态效果时到快结束都应是满强度：满 %d，现在 %d", strength, got)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	if in, out, _, _ := diffImages(base, snapshot(tt), zone); in+out != 0 {
		t.Fatal("到点以后强调还在")
	}
}
