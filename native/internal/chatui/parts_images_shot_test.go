package chatui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 设 CHATUI_SHOT=目录 和 CHATUI_FONT=字体文件 时出图：输入框里的图片缩略图、发出去的分块消息里的图（浅色、深色各一组）。平时跳过。

// fixedMedia 按序号给不同的图（模拟图片缓存），可以让某几张取不到。
type fixedMedia struct {
	blockMedia
	byPos map[int]MediaResult
}

func (m *fixedMedia) Image(_ string, pos int, _ bool, _ int) MediaResult {
	if r, ok := m.byPos[pos]; ok {
		return r
	}
	return MediaResult{State: MediaFailed}
}

func saveShot(t *testing.T, tt *ui.Tester, dir, name string, r ui.Rect, pad float32) {
	t.Helper()
	box := image.Rect(int((r.X-pad)*1.25), int((r.Y-pad)*1.25), int((r.X+r.W+pad)*1.25), int((r.Y+r.H+pad)*1.25)).Intersect(tt.Image().Bounds())
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image().SubImage(box)); err != nil {
		t.Fatal(err)
	}
}

func TestShotPartsImages(t *testing.T) {
	dir, fontPath := os.Getenv("CHATUI_SHOT"), os.Getenv("CHATUI_FONT")
	if dir == "" || fontPath == "" {
		t.Skip("设 CHATUI_SHOT 和 CHATUI_FONT 才出图")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fontPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerTestFont(data, "WY Rounded"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPOSER_FONT", fontPath)
	at := time.Date(2026, 11, 9, 21, 53, 0, 0, time.Local)
	land := MediaResult{State: MediaReady, Image: photoBitmap(800, 600)}
	tall := MediaResult{State: MediaReady, Image: photoBitmap(450, 800)}
	wide := MediaResult{State: MediaReady, Image: photoBitmap(960, 400)}
	for _, dark := range []bool{false, true} {
		tone := map[bool]string{false: "light", true: "dark"}[dark]
		s := DefaultSettings()
		s.Dark = dark
		tk := Derive(s)
		quote := &PartQuote{Target: "t", Name: "铃花", Snippet: "那张图是在哪里拍的呀，光线很好看"}
		scenes := []struct {
			name  string
			parts []Part
			media map[int]MediaResult
		}{
			{"only-landscape", []Part{{Atts: []PartAtt{picAtt("风景.png", 0, 800, 600)}}}, map[int]MediaResult{0: land}},
			{"only-portrait", []Part{{Atts: []PartAtt{picAtt("竖图.png", 0, 450, 800)}}}, map[int]MediaResult{0: tall}},
			{"image-text", []Part{{Text: "这张是昨天拍的，傍晚的光线特别好。", Atts: []PartAtt{picAtt("风景.png", 0, 800, 600)}}}, map[int]MediaResult{0: land}},
			{"quote-image", []Part{{Quote: quote, Text: "在公园，你看这个角度", Atts: []PartAtt{picAtt("风景.png", 0, 800, 600)}}}, map[int]MediaResult{0: land}},
			{"album-3", []Part{{Atts: []PartAtt{picAtt("a.png", 0, 800, 600), picAtt("b.png", 1, 450, 800), picAtt("c.png", 2, 960, 400)}}}, map[int]MediaResult{0: land, 1: tall, 2: wide}},
			{"album-4-text-file", []Part{{Text: "四张一起发", Atts: []PartAtt{picAtt("a.png", 0, 800, 600), picAtt("b.png", 1, 800, 600), picAtt("c.png", 2, 800, 600), picAtt("d.png", 3, 800, 600), {Name: "记录.txt"}}}}, map[int]MediaResult{0: land, 1: land, 2: land, 3: land}},
			{"missing", []Part{{Text: "这张图的文件被删了", Atts: []PartAtt{picAtt("丢了的图.png", 0, 800, 600)}}}, nil},
			{"short-file", []Part{{Atts: []PartAtt{{Name: "笔记.txt"}}}}, nil},
			{"short-text-file", []Part{{Text: "好", Atts: []PartAtt{{Name: "笔记.txt"}}}}, nil},
		}
		for _, sc := range scenes {
			m := partsMsg("m-"+sc.name, sc.parts...)
			m.At, m.Char = at, CharColors[0]
			host := Host{T: tk, Font: "WY Rounded", SelfChar: CharColors[0], Column: 560, NoEnter: true, Media: &fixedMedia{byPos: sc.media}}
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c.Key("root")).Width(560).Padding(20).Background(tk.C.ChatBg).Justify(ui.End).Children(func() { host.Message(c, m, nil, nil) })
			}, 560, 760)
			tt.SetScale(1.25)
			tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			tt.Frame()
			tt.Frame()
			bub := mustFind(t, tt, "气泡")
			saveShot(t, tt, dir, "bubble-"+sc.name+"-"+tone+".png", bub, 16)
		}

		// 输入框
		p := NewComposer()
		p.Main.Text = "看看这几张"
		for _, im := range []struct {
			name string
			w, h int
		}{{"风景.png", 800, 600}, {"竖图.png", 450, 800}, {"横幅.png", 960, 400}} {
			p.Attach(nil, Att{Kind: AttImage, Name: im.name, Path: "D:/假目录/" + im.name, Thumb: photoBitmap(im.w, im.h), Width: im.w, Height: im.h})
		}
		p.Attach(nil, Att{Kind: AttImage, Name: "读不出的图.png", Path: "D:/假目录/读不出的图.png"})
		p.Attach(nil, Att{Kind: AttImage, Name: "保存中.png", Pending: true})
		p.Attach(nil, Att{Kind: AttFile, Name: "笔记.txt", Path: "D:/假目录/笔记.txt"})
		p.Attach(nil, Att{Kind: AttFolder, Name: "资料", Path: "D:/假目录/资料"})
		p.Attach(nil, Att{Kind: AttText, Name: pasteName, Text: "一大段粘贴进来的文字……"})
		tt, _ := shotRig(t, dark, func(c *ui.Context, th *ui.Theme, tk Tokens) {
			ui.Column(c).Width(760).Children(func() { ComposerHost{T: tk, C: shotColors(th), Font: "ShotCJK"}.Draw(c, p) })
		})
		box := mustFind(t, tt, "输入区")
		saveShot(t, tt, dir, "composer-"+tone+".png", box, 16)
		// 单张图
		q := NewComposer()
		q.Attach(nil, Att{Kind: AttImage, Name: "风景.png", Path: "D:/假目录/风景.png", Thumb: photoBitmap(800, 600), Width: 800, Height: 600})
		tt, _ = shotRig(t, dark, func(c *ui.Context, th *ui.Theme, tk Tokens) {
			ui.Column(c).Width(760).Children(func() { ComposerHost{T: tk, C: shotColors(th), Font: "ShotCJK"}.Draw(c, q) })
		})
		saveShot(t, tt, dir, "composer-one-"+tone+".png", mustFind(t, tt, "输入区"), 16)
	}
}
