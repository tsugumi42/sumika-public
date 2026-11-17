package chatui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// 图片消息的所有组合：布局测试和截图共用一份清单，改排法时两边一起变。

type imgCombo struct {
	Name    string
	Group   bool
	Msg     *Msg
	Deco    bool   // 带引用、表情回应或注释（以前走旧排法的那几种）
	Inset   bool   // 图不在最前面：图文混排，图缩进在内边距里（不铺满）
	Caption string // 配文（要检查它没越出气泡）
}

var comboAt = time.Date(2026, 11, 8, 21, 53, 0, 0, time.Local)

func comboReactions() []Reaction {
	return []Reaction{{Emoji: "👍", Count: 5}, {Emoji: "😂", Count: 2, Mine: true}}
}

func comboQuote(thumb ThumbKind) *Quote {
	return &Quote{Name: "乙", Text: "昨天说好的纸灯，今天带来了吗", Target: "orig", Thumb: thumb}
}

func comboPartQuote() *PartQuote {
	return &PartQuote{Target: "orig", Name: "乙", Snippet: "昨天说好的纸灯，今天带来了吗"}
}

func comboMsg(id string, self bool, text string, segs ...segview.Segment) *Msg {
	m := imgMsg(id, self, text, segs...)
	m.At = comboAt
	m.Char = CharColors[0]
	return m
}

func comboAlbum(n int) []segview.Segment {
	var segs []segview.Segment
	for i := 0; i < n; i++ {
		segs = append(segs, imgSeg(i, 640, 480))
	}
	return segs
}

func comboParts(self bool, parts ...Part) *Msg {
	return &Msg{ID: "p", Self: self, Name: "甲", Sender: "甲", Char: CharColors[0], At: comboAt, Parts: parts}
}

func imageCombos() []imgCombo {
	land := func() segview.Segment { return imgSeg(0, 800, 500) }
	stk := imgSeg(0, 200, 200)
	stk.Kind, stk.Sticker = "emoji", true
	with := func(m *Msg, f func(*Msg)) *Msg { f(m); return m }
	quote := func(m *Msg) { m.Quote = comboQuote("") }
	react := func(m *Msg) { m.Reactions = comboReactions() }
	note := func(m *Msg) { m.Notes = []string{"已编辑"} }
	textFirst := comboMsg("tf", false, "", land())
	textFirst.Blocks = []segview.Block{{Kind: segview.Inline, Runs: []segview.Run{{Text: "先说一句，再看图。"}}}, textFirst.Blocks[0]}
	picPart := func() Part { return Part{Atts: []PartAtt{picAtt("风景.png", 0, 800, 500)}} }
	return []imgCombo{
		{Name: "only", Msg: comboMsg("a", false, "", land())},
		{Name: "only-self", Msg: comboMsg("b", true, "", imgSeg(0, 400, 800))},
		{Name: "caption", Caption: "这张是昨天拍的，傍晚的光线特别好。", Msg: comboMsg("c", false, "这张是昨天拍的，傍晚的光线特别好。", land())},
		{Name: "album-4", Msg: comboMsg("e", true, "", comboAlbum(4)...)},
		{Name: "sticker", Msg: comboMsg("f", false, "", stk)},
		{Name: "group-name", Group: true, Msg: comboMsg("d", false, "", land())},
		{Name: "quote", Deco: true, Msg: with(comboMsg("g", false, "", land()), quote)},
		{Name: "quote-caption", Caption: "好看，在哪拍的？", Deco: true, Msg: with(comboMsg("h", true, "好看，在哪拍的？", land()), quote)},
		{Name: "quote-thumb", Deco: true, Msg: with(comboMsg("i", false, "", land()), func(m *Msg) { m.Quote = comboQuote(ThumbImage) })},
		{Name: "group-quote", Group: true, Deco: true, Msg: with(comboMsg("j", false, "", land()), quote)},
		{Name: "reactions", Deco: true, Msg: with(comboMsg("k", false, "", land()), react)},
		{Name: "reactions-caption", Caption: "这张是昨天拍的。", Deco: true, Msg: with(comboMsg("l", true, "这张是昨天拍的。", land()), react)},
		{Name: "notes", Deco: true, Msg: with(comboMsg("m", false, "", land()), note)},
		{Name: "narrow-notes", Deco: true, Msg: with(comboMsg("n", true, "", imgSeg(0, 100, 900)), note)},
		{Name: "narrow-reactions", Deco: true, Msg: with(comboMsg("n2", false, "", imgSeg(0, 100, 900)), react)},
		{Name: "all", Caption: "几样都带上。", Group: true, Deco: true, Msg: with(comboMsg("o", false, "几样都带上。", land()), func(m *Msg) { quote(m); react(m); note(m) })},
		{Name: "album-reactions", Deco: true, Msg: with(comboMsg("p", true, "", comboAlbum(3)...), react)},
		{Name: "sticker-quote", Deco: true, Msg: with(comboMsg("q", false, "", stk), quote)},
		{Name: "sticker-reactions", Deco: true, Msg: with(comboMsg("r", false, "", stk), react)},
		{Name: "text-first", Inset: true, Msg: textFirst},
		{Name: "parts-only", Msg: comboParts(true, picPart())},
		{Name: "parts-caption", Caption: "看这个", Msg: comboParts(true, Part{Text: "看这个", Atts: picPart().Atts})},
		{Name: "parts-quote", Caption: "回复", Deco: true, Msg: comboParts(true, Part{Quote: comboPartQuote(), Text: "回复", Atts: picPart().Atts})},
		{Name: "parts-reactions", Deco: true, Msg: with(comboParts(true, picPart()), react)},
		{Name: "parts-notes", Deco: true, Msg: with(comboParts(true, picPart()), note)},
		{Name: "parts-second", Inset: true, Msg: comboParts(true, Part{Text: "先说话"}, picPart())},
	}
}

func comboHost(tk Tokens, font string, c imgCombo, res MediaResult) Host {
	media := &thumbMedia{blockMedia: blockMedia{state: res}, thumb: res}
	return Host{T: tk, Font: font, SelfChar: CharColors[0], Group: c.Group, Column: 560, NoEnter: true, Media: media}
}

func comboRender(tk Tokens, h Host, m *Msg) *ui.Tester {
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("root")).Width(520).Padding(20).Background(tk.C.ChatBg).Children(func() { h.Message(c, m, nil, nil) })
	}, 520, 640)
	tt.SetScale(1.25)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	return tt
}

// 设 CHATUI_SHOT=目录 和 CHATUI_FONT=字体文件 时出图：每种组合浅色、深色各一张，只截气泡区域。平时跳过。
func TestShotImageCombos(t *testing.T) {
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
	for _, dark := range []bool{false, true} {
		s := DefaultSettings()
		s.Dark = dark
		tk := Derive(s)
		for _, c := range imageCombos() {
			res := MediaResult{State: MediaReady, Image: photoBitmap(800, 500)}
			if strings.HasPrefix(c.Name, "sticker") {
				res.Image = photoBitmap(200, 200) // 真贴纸是方的
			}
			tt := comboRender(tk, comboHost(tk, "WY Rounded", c, res), c.Msg)
			bub, ok := tt.Find("气泡")
			if !ok {
				t.Fatalf("%s：没找到气泡", c.Name)
			}
			box := image.Rect(int((bub.X-24)*1.25), int((bub.Y-24)*1.25), int((bub.X+bub.W+24)*1.25), int((bub.Y+bub.H+24)*1.25)).Intersect(tt.Image().Bounds())
			name := c.Name + "-light.png"
			if dark {
				name = c.Name + "-dark.png"
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
