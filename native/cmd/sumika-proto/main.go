package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"log"
	"os"
	"strings"
	"sumika-native/internal/assets"
	"sumika-native/internal/assets/fonts"
	"sumika-native/internal/chatui"
)

// loadLook 读本机 native/fonts/、native/twemoji/ 里放着的文渊圆体（OFL）和 Twemoji（CC-BY 4.0），不进 git；读不到退回系统字体和文字表情。
// 临时：正式界面打包时要把这两样放进 Sumika 自己的资源目录，见待办"界面资源打包"。
func loadLook(a *app) {
	if data, err := os.ReadFile(fonts.WenYuanPath()); err == nil && ui.RegisterFont(data, "WY Rounded") == nil {
		a.Font = "WY Rounded"
	}
	chatui.EmojiDir = assets.TwemojiDir()
}

func main() {
	a := newApp()
	mygo.App.WhenReady(func() {
		for _, font := range ui.FontFamilies() {
			if font.Name == "Microsoft YaHei UI" {
				a.Font = font.Name
			}
			if a.Mono == "" && strings.HasPrefix(font.Name, "Maple Mono") { // 等宽字照 Bun 用 Maple Mono（规格-过程组件 3.7）
				a.Mono = font.Name
			}
		}
		loadLook(a)
		win := mygo.NewWindow(mygo.WindowOptions{Title: "Sumika · 交互原型", Width: 2048, Height: 1030, UseContentSize: true, MinWidth: 480, MinHeight: 560, Frameless: true, CornerRadius: 8, Content: ui.View(a.view)})
		a.Close = func() { win.Close() }
		win.OnClosed(func() { mygo.App.Quit() })
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
