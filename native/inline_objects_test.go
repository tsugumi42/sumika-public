package main

import (
	"fmt"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// 非 BMP 字符、组合字符和链接都先于图片；用实际对象位置核对图片、复制和父裁切。
func TestInlineObjectsUnicodeLinksTailAndClip(t *testing.T) {
	const prefix = "😀a\u0301 https://example.com/u "
	const middle = " 中\n第二行 "
	for _, width := range []int{240, 500} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			message := faceMsg(false, "objects", prefix, "#14", middle, "#21", " 收到")
			net := &fakeNet{png: map[string][]byte{"14": facePNG(64, 64, red), "21": facePNG(64, 64, blue)}}
			a, queue := faceApp(t, net, message)
			a.fs().table = testTable
			var bounds ui.Rect
			var objects []ui.TextObject
			drawCheck = func(r ui.Rect, _ []ui.Span, boxes []ui.TextObject) {
				bounds = r
				objects = append(objects[:0], boxes...)
			}
			defer func() { drawCheck = nil }()
			clipHeight := float32(460)
			tt := testerOf(width, 500, func(c *ui.Context) {
				page(c, func() {
					ui.Column(c).Height(clipHeight).Clip().Label("裁切正文").Children(func() {
						a.messageBody(c, message, true, true, time.UnixMilli(t0))
					})
				})
			})
			a.fs().wg.Wait()
			queue.drain()
			tt.Frame()
			first := utf8.RuneCountInString(prefix)
			second := first + 1 + utf8.RuneCountInString(middle)
			if len(objects) != 2 || objects[0].Index != first || objects[1].Index != second {
				t.Fatalf("对象应在 rune %d/%d，实际 %+v", first, second, objects)
			}
			points := [2]ui.Point{}
			image := tt.Image()
			for i := range 2 {
				points[i] = ui.Point{X: bounds.X + objects[i].Rect.X + faceSize(14)/2, Y: bounds.Y + objects[i].Rect.Y + faceSize(14)/2}
				want := red
				if i == 1 {
					want = blue
				}
				if pixel := image.RGBAAt(int(points[i].X), int(points[i].Y)); pixel != want {
					t.Fatalf("对象 %d 的图像像素为 %v，应为 %v", i, pixel, want)
				}
			}
			tt.Press(bounds.X-2, bounds.Y+1)
			tt.Move(bounds.X+bounds.W+2, bounds.Y+bounds.H-1)
			tt.Release(bounds.X+bounds.W+2, bounds.Y+bounds.H-1)
			tt.Key(ui.Cmd, ui.KeyC)
			if copied, want := clipText(tt), plainNamed(message.blocks[0], testTable); copied != want {
				t.Fatalf("复制得到 %q，应为 %q（不含时间槽）", copied, want)
			}
			clip, _ := tt.Find("裁切正文")
			clipHeight = points[1].Y - faceSize(14)/2 - clip.Y - 1
			tt.Frame()
			cropped := tt.Image()
			if pixel := cropped.RGBAAt(int(points[0].X), int(points[0].Y)); pixel != red {
				t.Fatalf("父裁切误伤第一张图：%v", pixel)
			}
			if pixel := cropped.RGBAAt(int(points[1].X), int(points[1].Y)); pixel == blue {
				t.Fatal("第二张图越过父裁切仍然可见")
			}
		})
	}
}

func TestCopyBidiRangesKeepParagraphs(t *testing.T) {
	t.Parallel()
	const text = "甲😀\uFFFCאב\uFFFC乙"
	var cp copyMap
	cp.add(1, []string{"[微笑]", "[可爱]"})
	data := cp.restore([]ui.SelectedText{
		{ID: 1, Text: text, Start: 0, End: 3},
		{ID: 1, Text: text, Start: 4, End: 6},
		{ID: 2, Text: "后段", Start: 0, End: 2},
	})
	bytes, err := data.Read(transfer.Text)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(bytes), "甲😀[微笑]ב[可爱]\n\n后段"; got != want {
		t.Fatalf("分段复制=%q，应为 %q", got, want)
	}
}
