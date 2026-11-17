package chatui

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// photoBitmap 编一张有渐变和斜纹的"照片"，方便看出裁切和比例。
func photoBitmap(w, h int) *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			stripe := uint8(0)
			if (x+y)/24%2 == 0 {
				stripe = 28
			}
			img.SetRGBA(x, y, color.RGBA{uint8(60+150*x/w) + stripe, uint8(90+100*y/h) + stripe, uint8(110-90*x/w+90) + stripe, 255})
		}
	}
	return ui.NewBitmap(img)
}

// 设 CHATUI_SHOT=目录 时出图（只截组件区域，浅色、深色各一组，像素比 1.25）；字体取 CHATUI_FONT。
func TestShotImageBubbles(t *testing.T) {
	dir := os.Getenv("CHATUI_SHOT")
	if dir == "" {
		t.Skip("设 CHATUI_SHOT 才出图")
	}
	font := ""
	if p := os.Getenv("CHATUI_FONT"); p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := registerTestFont(data, "WY Rounded"); err != nil {
			t.Fatal(err)
		}
		font = "WY Rounded"
	} else {
		t.Fatal("没设 CHATUI_FONT，截出来不是文渊圆体")
	}
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		tk := Derive(s)
		res := func(w, h int) MediaResult { return MediaResult{State: MediaReady, Image: photoBitmap(w, h)} }
		at := time.Date(2026, 11, 8, 21, 53, 0, 0, time.Local)
		mk := func(id string, self bool, text string, segs ...segview.Segment) *Msg {
			m := imgMsg(id, self, text, segs...)
			m.At = at
			m.Char = CharColors[0]
			return m
		}
		stk := imgSeg(0, 200, 200)
		stk.Kind, stk.Sticker = "emoji", true
		type scene struct {
			name  string
			group bool
			msg   *Msg
			w, h  int
			state MediaResult
		}
		var albumSegs []segview.Segment
		for i := 0; i < 4; i++ {
			albumSegs = append(albumSegs, imgSeg(i, 640, 480))
		}
		scenes := []scene{
			{"only-landscape", false, mk("a", false, "", imgSeg(0, 800, 500)), 800, 500, MediaResult{}},
			{"only-portrait-self", false, mk("b", true, "", imgSeg(0, 400, 800)), 400, 800, MediaResult{}},
			{"caption", false, mk("c", false, "这张是昨天拍的，傍晚的光线特别好。", imgSeg(0, 800, 500)), 800, 500, MediaResult{}},
			{"group-name", true, mk("d", false, "", imgSeg(0, 800, 500)), 800, 500, MediaResult{}},
			{"album-4", false, mk("e", true, "", albumSegs...), 640, 480, MediaResult{}},
			{"sticker", false, mk("f", false, "", stk), 200, 200, MediaResult{}},
			{"loading", false, mk("g", false, "", imgSeg(0, 800, 500)), 0, 0, MediaResult{State: MediaLoading}},
		}
		for _, sc := range scenes {
			r := sc.state
			if sc.w > 0 {
				r = res(sc.w, sc.h)
			}
			host := Host{T: tk, Font: font, SelfChar: CharColors[0], Group: sc.group, Column: 560, NoEnter: true, Media: &blockMedia{state: r}}
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c.Key("root")).Width(520).Padding(20).Background(tk.C.ChatBg).Children(func() { host.Message(c, sc.msg, nil, nil) })
			}, 520, 520)
			tt.SetScale(1.25)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.Frame()
			tt.Frame()
			bub, ok := tt.Find("气泡")
			if !ok {
				t.Fatal("没找到气泡")
			}
			box := image.Rect(int((bub.X-24)*1.25), int((bub.Y-24)*1.25), int((bub.X+bub.W+24)*1.25), int((bub.Y+bub.H+24)*1.25)).Intersect(tt.Image().Bounds())
			name := sc.name + "-light.png"
			if dark {
				name = sc.name + "-dark.png"
			}
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tt.Image().SubImage(box)); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}
