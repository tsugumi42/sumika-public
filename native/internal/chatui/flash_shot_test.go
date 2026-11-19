package chatui

import (
	"os"
	"testing"
)

// 设 CHATUI_SHOT=目录 时出图：跳转强调的满强度和淡出到一半，浅色深色各一张。
func TestShotJumpFlash(t *testing.T) {
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
		for name, a := range map[string]float32{"full": 1, "half": 0.4} {
			h := Host{T: Derive(s), Font: font, SelfChar: CharColors[0], Column: 560, NoEnter: true, Media: &thumbMedia{},
				Flash: func(string) float32 { return a }}
			m := quoteMsg("")
			m.Char = CharColors[0]
			shotMessage(t, h, m, dir, "flash-"+name+suffix)
		}
	}
}
