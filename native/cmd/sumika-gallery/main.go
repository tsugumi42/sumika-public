// sumika-gallery 是开发用的组件画廊：一个窗口、左边按族列出、右边是能点能拖的舞台（规格-设计令牌 第八节）。
// 不进 sumika.exe。启动和快捷键见同目录的 README.md。
package main

import (
	"log"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/assets"
	"sumika-native/internal/assets/fonts"
	"sumika-native/internal/chatui"
)

// 窗口默认大小（逻辑像素）：宽到状态排一行放得下，高到一族的状态排加试一试不用滚动。
const (
	windowWidth  = 1900 // 自有尺寸：画廊窗口默认宽
	windowHeight = 1000 // 自有尺寸：画廊窗口默认高
)

// loadLook 读本机 native/fonts/、native/twemoji/ 里放着的文渊圆体和 Twemoji（不进 git）；读不到退回系统字体和文字表情。
// 等宽字找 Maple Mono；找不到就用系统等宽。
func loadLook() (font, mono string) {
	if data, err := os.ReadFile(fonts.WenYuanPath()); err == nil && ui.RegisterFont(data, "WY Rounded") == nil {
		font = "WY Rounded"
	}
	for _, f := range ui.FontFamilies() {
		if font == "" && f.Name == "Microsoft YaHei UI" {
			font = f.Name
		}
		if mono == "" && strings.HasPrefix(f.Name, "Maple Mono") {
			mono = f.Name
		}
	}
	chatui.EmojiDir = assets.TwemojiDir()
	return font, mono
}

func main() {
	mygo.App.WhenReady(func() {
		g := newGallery(loadLook())
		win := mygo.NewWindow(mygo.WindowOptions{Title: "Sumika 组件画廊", Width: windowWidth, Height: windowHeight, UseContentSize: true, MinWidth: 900, MinHeight: 600, Content: ui.View(g.view)})
		win.OnClosed(func() { mygo.App.Quit() })
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
