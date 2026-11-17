package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sumika-native/internal/assets/fonts"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 设 PROTO_SHOT=目录 时，用固定的一段对话把聊天消息区浅色、深色各截一张（和正式程序新界面的 palette-workbench-*.png 对照）；平时跳过。
func TestShotPalette(t *testing.T) {
	dir := os.Getenv("PROTO_SHOT")
	if dir == "" {
		t.Skip("设 PROTO_SHOT 才出图")
	}
	font := ""
	if data, err := os.ReadFile(fonts.WenYuanPath()); err == nil && ui.RegisterFont(data, "WY Rounded") == nil {
		font = "WY Rounded"
	}
	reply := "哼，早什么早，都快十二点了哦。"
	dialog := [][2]string{{"self", "早"}, {"r1", reply}, {"self", "中午好"}, {"r1", reply}}
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Font, a.Layer, a.Dark = font, 1, dark
		cv := a.cv()
		cv.Messages = nil
		for i, d := range dialog {
			cv.Messages = append(cv.Messages, &message{ID: "pal-" + string(rune('a'+i)), Actor: d[0], Text: d[1], State: "complete"})
		}
		tt := ui.NewTester(a.view, 2048, 1030)
		tt.SetScale(1.25)
		tt.Frame()
		tt.Frame()
		root := geometryTree(tt)
		first, last := root.Find("pal-a"), root.Find("pal-d")
		if first == nil || last == nil {
			t.Fatal("找不到要截的消息")
		}
		// 只截消息区这一块：左右到聊天区边，上下到第一、最后一条加边距
		const s, pad = 1.25, 24
		img := tt.Image()
		r := image.Rect(int((first.Box.X-pad)*s), int((first.Box.Y-pad)*s), int((last.Box.X+last.Box.W+pad)*s), int((last.Box.Y+last.Box.H+pad)*s)).Intersect(img.Bounds())
		name := map[bool]string{false: "palette-proto-light.png", true: "palette-proto-dark.png"}[dark]
		f, _ := os.Create(filepath.Join(dir, name))
		png.Encode(f, img.SubImage(r))
		f.Close()
		t.Logf("%s：%v", name, r)
	}
}
