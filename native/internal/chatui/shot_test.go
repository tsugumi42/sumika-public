package chatui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 设 CHATUI_SHOT=目录 时，把浅深 × 私聊群聊的消息区画成 PNG 给人看；平时跳过。
// 字体文件在 CHATUI_FONT（文渊圆体）存在时注册，不存在用系统字体。
func TestShot(t *testing.T) {
	dir := os.Getenv("CHATUI_SHOT")
	if dir == "" {
		t.Skip("设 CHATUI_SHOT 才出图")
	}
	font := ""
	if p := os.Getenv("CHATUI_FONT"); p != "" {
		if data, err := os.ReadFile(p); err == nil && registerTestFont(data, "WY Rounded") == nil {
			font = "WY Rounded"
		}
	}
	EmojiDir = os.Getenv("CHATUI_TWEMOJI")
	defer func() { EmojiDir = "" }()
	for _, dark := range []bool{false, true} {
		for _, group := range []bool{false, true} {
			s := DefaultSettings()
			s.Dark = dark
			tk := Derive(s)
			host := Host{T: tk, Font: font, SelfChar: CharColors[0], Group: group, Column: 640, NoEnter: true}
			msgs := scene()
			var wp Wallpaper
			view := func(c *ui.Context) {
				ui.Column(c.Key("root")).Size(720, 640).Padding(24, 40).Background(tk.C.ChatBg).
					Draw(func(p *ui.Painter, r ui.Rect) { wp.Paint(p, r, tk, CharColors[0]) }).Children(func() {
					for i, m := range msgs {
						var prev, next *Msg
						if i > 0 {
							prev = msgs[i-1]
						}
						if i+1 < len(msgs) {
							next = msgs[i+1]
						}
						host.Message(c, m, prev, next)
					}
					host.Typing(c, "typing", CharColors[1])
				})
			}
			tt := ui.NewTester(view, 720, 640)
			scale := float32(1.25)
			if os.Getenv("CHATUI_SHOT_ZOOM") != "" {
				scale = 3 // 放大看细节
			}
			tt.SetScale(scale)
			tt.Frame()
			tt.Frame()
			name := map[bool]string{false: "浅", true: "深"}[dark] + map[bool]string{false: "私聊", true: "群聊"}[group] + ".png"
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, tt.Image())
			f.Close()
		}
	}
}
