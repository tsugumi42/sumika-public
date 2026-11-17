package chatui

import (
	"os"
	"testing"
)

// 设 CHATUI_SHOT=目录 时出图：引用块带缩略图（图片、视频），浅色深色各一张。
func TestShotQuoteThumbnails(t *testing.T) {
	dir := os.Getenv("CHATUI_SHOT")
	if dir == "" {
		t.Skip("设 CHATUI_SHOT 才出图")
	}
	font := shotFont(t)
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		suffix := "-light.png"
		if dark {
			suffix = "-dark.png"
		}
		for _, kind := range []ThumbKind{ThumbImage, ThumbVideo} {
			h := Host{T: Derive(s), Font: font, SelfChar: CharColors[0], Column: 560, NoEnter: true,
				Media: &thumbMedia{thumb: MediaResult{State: MediaReady, Image: photoBitmap(120, 120)}}}
			m := quoteMsg(kind)
			m.Char = CharColors[0]
			m.Quote.Text = "这张是昨天拍的，傍晚的光线特别好"
			shotMessage(t, h, m, dir, "quote-"+string(kind)+suffix)
		}
	}
}
