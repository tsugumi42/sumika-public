package chatui

import (
	"os"
	"sumika-native/internal/segview"
	"testing"
)

// 设 CHATUI_SHOT=目录 时出图：视频卡没下载、下载中、已下载，以及文件条，浅色深色各一组。
func TestShotVideoCards(t *testing.T) {
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
		for name, dl := range map[string]MediaResult{
			"idle":        {State: MediaMissing, Progress: -1, Description: "点一下下载"},
			"downloading": {State: MediaDownloading, Progress: 0.4},
			"done":        {State: MediaDownloaded, Progress: 1},
		} {
			h := Host{T: Derive(s), Font: font, SelfChar: CharColors[0], Column: 560, NoEnter: true,
				Media: videoMedia{dl: dl, cover: MediaResult{State: MediaReady, Image: photoBitmap(640, 360)}}}
			m := videoMsg(false, videoSeg())
			m.Char = CharColors[0]
			shotMessage(t, h, m, dir, "video-"+name+suffix)
		}
		h := Host{T: Derive(s), Font: font, SelfChar: CharColors[0], Column: 560, NoEnter: true,
			Media: videoMedia{dl: MediaResult{State: MediaDownloading, Progress: 0.4}}}
		m := videoMsg(false, videoSeg())
		m.Blocks[0].Kind = segview.File
		m.Blocks[0].Video.Name = "资料合集.zip"
		shotMessage(t, h, m, dir, "file-downloading"+suffix)
	}
}
