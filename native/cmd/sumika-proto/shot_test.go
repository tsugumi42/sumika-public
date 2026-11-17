package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sumika-native/internal/assets"
	"sumika-native/internal/assets/fonts"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
)

// 设 PROTO_SHOT=目录 时，按几种窗口宽度把整窗画成 PNG（看分栏）；平时跳过。
func TestShotColumns(t *testing.T) {
	dir := os.Getenv("PROTO_SHOT")
	if dir == "" {
		t.Skip("设 PROTO_SHOT 才出图")
	}
	font := ""
	if data, err := os.ReadFile(fonts.WenYuanPath()); err == nil && ui.RegisterFont(data, "WY Rounded") == nil {
		font = "WY Rounded"
	}
	chatui.EmojiDir = assets.TwemojiDir()
	defer func() { chatui.EmojiDir = "" }()
	for _, cs := range []struct {
		name    string
		w       int
		compact bool
	}{{"2048-三栏", 2048, false}, {"1100-三栏", 1100, false}, {"900-两栏", 900, false}, {"640-一栏", 640, false}, {"1400-头像模式", 1400, true}} {
		a := newApp()
		a.Font, a.Layer, a.Group = font, 1, 1
		a.Selected["local"] = "B"
		a.ListCompact, a.ListDragged = cs.compact, cs.compact
		tt := ui.NewTester(a.view, cs.w, 760)
		tt.SetScale(1.25)
		tt.Frame()
		tt.Frame()
		f, _ := os.Create(filepath.Join(dir, cs.name+".png"))
		png.Encode(f, tt.Image())
		f.Close()
	}
}

// 设 PROTO_SHOT=目录 时，把一段过程（读搜、失败的命令、等批准的改文件和命令）浅色、深色各截一张，只截聊天里过程那一块。
func TestShotProcess(t *testing.T) {
	dir := os.Getenv("PROTO_SHOT")
	if dir == "" {
		t.Skip("设 PROTO_SHOT 才出图")
	}
	font := ""
	if data, err := os.ReadFile(fonts.WenYuanPath()); err == nil && ui.RegisterFont(data, "WY Rounded") == nil {
		font = "WY Rounded"
	}
	mono := ""
	for _, f := range ui.FontFamilies() {
		if mono == "" && strings.HasPrefix(f.Name, "Maple Mono") {
			mono = f.Name
		}
	}
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Font, a.Mono, a.Layer, a.Dark = font, mono, 1, dark
		tt := ui.NewTester(a.view, 2048, 1030)
		tt.SetScale(1.25)
		cv := a.cv()
		for _, k := range []string{"cmd-fail", "approve-edit", "approve-cmd"} {
			a.E.start(processScene(k, cv, cv.Actors[0]), "run.start", "")
			tt.Frame()
			tt.Advance(4 * time.Second)
		}
		var box ui.Rect
		for _, m := range cv.Messages {
			if n := geometryTree(tt).Find(m.ID); n != nil && len(m.Steps) > 0 {
				if box.W == 0 {
					box = n.Box
				}
				x0, y0 := min(box.X, n.Box.X), min(box.Y, n.Box.Y)
				x1, y1 := max(box.X+box.W, n.Box.X+n.Box.W), max(box.Y+box.H, n.Box.Y+n.Box.H)
				box = ui.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
			}
		}
		img := tt.Image()
		const s, pad = 1.25, 24
		r := image.Rect(int((box.X-pad)*s), int((box.Y-pad)*s), int((box.X+box.W+pad)*s), int((box.Y+box.H+pad)*s)).Intersect(img.Bounds())
		name := map[bool]string{false: "过程-浅色.png", true: "过程-深色.png"}[dark]
		f, _ := os.Create(filepath.Join(dir, name))
		png.Encode(f, img.SubImage(r))
		f.Close()
		t.Logf("%s：%v", name, r)
	}
}

// 设 PROTO_SHOT 时把一条带格式的回复（粗体、行内代码、列表、代码块）浅色、深色各截一张。
func TestShotMarkdown(t *testing.T) {
	dir := os.Getenv("PROTO_SHOT")
	if dir == "" {
		t.Skip("设 PROTO_SHOT 才出图")
	}
	font := ""
	if data, err := os.ReadFile(fonts.WenYuanPath()); err == nil && ui.RegisterFont(data, "WY Rounded") == nil {
		font = "WY Rounded"
	}
	mono := ""
	for _, f := range ui.FontFamilies() {
		if mono == "" && strings.HasPrefix(f.Name, "Maple Mono") {
			mono = f.Name
		}
	}
	text := "好呀，我把**今天要做的**理一下：\n\n- 先把 `notes/纸灯.md` 补完\n- 再跑一遍测试\n\n```go\nfunc lamp() float64 { return 0.6 }\n```\n\n弄完我们接着聊。"
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Font, a.Mono, a.Layer, a.Dark = font, mono, 1, dark
		cv := a.cv()
		cv.Messages = append(cv.Messages, &message{ID: "md-shot", Actor: "r1", Text: "第一行\n第二行，单个换行就是换行。", State: "complete"}, &message{ID: "md-shot-2", Actor: "r1", Text: text, State: "complete"})
		tt := ui.NewTester(a.view, 2048, 1030)
		tt.SetScale(1.25)
		tt.Frame()
		tt.Frame()
		root := geometryTree(tt)
		r1, r2 := root.Find("md-shot"), root.Find("md-shot-2")
		if r1 == nil || r2 == nil {
			t.Fatal("找不到要截的消息")
		}
		const s, pad = 1.25, 24
		x0, y0 := min(r1.Box.X, r2.Box.X)-pad, r1.Box.Y-pad
		x1, y1 := max(r1.Box.X+r1.Box.W, r2.Box.X+r2.Box.W)+pad, r2.Box.Y+r2.Box.H+pad
		img := tt.Image()
		name := map[bool]string{false: "格式-浅色.png", true: "格式-深色.png"}[dark]
		f, _ := os.Create(filepath.Join(dir, name))
		png.Encode(f, img.SubImage(image.Rect(int(x0*s), int(y0*s), int(x1*s), int(y1*s)).Intersect(img.Bounds())))
		f.Close()
	}
}

// 放大十倍（ADR-056）：一万条历史、她的消息全部走 markdown，打开、滚到最上面再滚回来，量每一帧的耗时。
func TestScaleMarkdownHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("整套才跑")
	}
	a := newApp()
	a.Layer = 1
	cv := a.cv()
	a.E.history(cv, 10000)
	for i, m := range cv.Messages {
		if i%3 == 0 {
			m.Text = "带**格式**的一条，`code` 和列表：\n\n- 一\n- 二"
		}
	}
	tt := ui.NewTester(a.view, 2048, 1030)
	tt.SetScale(1.25)
	var worst time.Duration
	frame := func() {
		start := time.Now()
		tt.Frame()
		worst = max(worst, time.Since(start))
	}
	frame()
	open := worst
	r := a.read(cv.ID)
	r.List.ScrollToEnd()
	for i := 0; i < 20; i++ {
		frame()
	}
	r.List.ScrollTo(0, ui.Start)
	for i := 0; i < 20; i++ {
		frame()
	}
	t.Logf("一万条（三分之一带 markdown 块）：打开一帧 %v，滚动中最慢一帧 %v，markdown 状态 %d 条", open, worst, a.md().Len())
	if worst > 200*time.Millisecond {
		t.Errorf("最慢一帧 %v 超过 200ms", worst)
	}
}

// 设 PROTO_SHOT 时把插件页（展开了链接解析、改了一项要重启的记忆设置）浅色、深色各截一张，只截设置主区。
func TestShotPlugins(t *testing.T) {
	dir := os.Getenv("PROTO_SHOT")
	if dir == "" {
		t.Skip("设 PROTO_SHOT 才出图")
	}
	font := ""
	if data, err := os.ReadFile(fonts.WenYuanPath()); err == nil && ui.RegisterFont(data, "WY Rounded") == nil {
		font = "WY Rounded"
	}
	for _, dark := range []bool{false, true} {
		a := newApp()
		a.Font, a.Page, a.Layer, a.Dark, a.SettingsCat = font, "settings", 1, dark, "plugins"
		s := a.pluginState()
		s.Open["link-reader"] = true
		s.Vals["memory.autoConfirm"] = true
		tt := ui.NewTester(a.view, 2048, 1400)
		tt.SetScale(1.25)
		tt.Frame()
		tt.Advance(time.Second)
		box := geometryTree(tt).Find("plugins-page")
		if box == nil {
			t.Fatal("找不到插件页")
		}
		const sc, pad = 1.25, 24
		img := tt.Image()
		r := image.Rect(int((box.Box.X-pad)*sc), int((box.Box.Y-pad)*sc), int((box.Box.X+box.Box.W+pad)*sc), int((box.Box.Y+box.Box.H+pad)*sc)).Intersect(img.Bounds())
		name := map[bool]string{false: "插件-浅色.png", true: "插件-深色.png"}[dark]
		f, _ := os.Create(filepath.Join(dir, name))
		png.Encode(f, img.SubImage(r))
		f.Close()
	}
}
