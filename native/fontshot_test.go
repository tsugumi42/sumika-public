package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/winshell"
)

func shotMsgs() []*chatui.Msg {
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	a := chatui.CharColors[0]
	return []*chatui.Msg{
		{ID: "m1", Sender: "r1", Name: "晴枝", Char: a, At: at, Text: "窗边的纸灯亮起来了。今天想聊些什么？可以从早上发生的小事说起，也可以随便聊聊别的。"},
		{ID: "m2", Sender: "r1", Name: "晴枝", Char: a, At: at.Add(5 * time.Second), Text: "字体和字号都能在设置里改，立即生效。", Markdown: false},
		{ID: "m3", Sender: "self", Self: true, Name: "我", At: at.Add(30 * time.Second), Text: "那我把字号调大一点试试，看看圆润的字好不好读。"},
	}
}

// 只在 SUMIKA_FONT_SHOT 指定目录时写图：设置里的字体字号两行，以及同一段聊天在 14、18 和系统字体下的样子。
// 文渊圆体在 exe 旁没有时尝试 native/fonts；都没有就是系统字体。
func TestFontShot(t *testing.T) {
	dir := os.Getenv("SUMIKA_FONT_SHOT")
	if dir == "" {
		t.Skip("没设 SUMIKA_FONT_SHOT，不截图")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got := loadBundledFont(".", bundledFamily, ui.RegisterFont)
	t.Logf("文渊圆体：%+v", got)
	write := func(name string, img *image.RGBA, r image.Rectangle) {
		t.Helper()
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img.SubImage(r).(*image.RGBA)); err != nil {
			t.Fatal(err)
		}
	}

	// 设置页：字体、字号两行
	a := &app{rail: 4}
	tt := ui.NewTester(a.view, 1100, 1030)
	tt.SetTitleBar(win11Bar)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetScale(1.25)
	tt.Advance(time.Second)
	f, ok1 := tt.Find("字体")
	s, ok2 := tt.Find("字号")
	if !ok1 || !ok2 {
		t.Fatal("设置页找不到字体或字号行")
	}
	r := image.Rect(int(f.X*1.25)-40, int(f.Y*1.25)-130, int(1100*1.25), int((s.Y+s.H)*1.25)+30)
	write("settings-font-size.png", tt.Image(), r.Intersect(tt.Image().Bounds()))

	chat := func(name string, size float32, font string) {
		b := &app{}
		b.initLook()
		b.setTextSize(size)
		b.setFontFamily(font)
		set := chatui.DefaultSettings()
		set.Text = b.look.light.Typography().Body().Size() // 设置里选的字号
		tok := winshell.NewKit(set, "").T
		host := chatui.Host{T: tok, Font: b.look.light.Typography().Font(), SelfChar: chatui.CharColors[0], Column: 520, NoEnter: true}
		msgs := shotMsgs()
		cv := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("root")).Width(560).Padding(20).Background(tok.C.Bg).Children(func() {
				for i, m := range msgs {
					var prev, next *chatui.Msg
					if i > 0 {
						prev = msgs[i-1]
					}
					if i+1 < len(msgs) {
						next = msgs[i+1]
					}
					host.Message(c, m, prev, next)
				}
			})
		}, 560, 300)
		cv.SetScale(1.25)
		cv.Frame()
		cv.Frame()
		write(name, cv.Image(), cv.Image().Bounds())
	}
	chat("chat-14.png", 14, bundledFamily)
	chat("chat-18.png", 18, bundledFamily)
	chat("chat-16-system.png", 16, desksettings.SystemFont)
	_ = theme.Light
}
