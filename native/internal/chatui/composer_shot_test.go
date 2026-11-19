package chatui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// 设 COMPOSER_SHOT=目录 时，把输入区、选区条和发出去的引用气泡画成 PNG 给人看；平时跳过。
// 窗口按主人的屏幕：逻辑 2048×1030、像素比 1.25（物理 2560×1288），只截组件所在的区域。
// 字体在 COMPOSER_FONT（带中文的 ttf）存在时注册，不存在用系统字体。
func shotRig(t *testing.T, dark bool, draw func(c *ui.Context, th *ui.Theme, tk Tokens)) (*ui.Tester, Tokens) {
	t.Helper()
	if p := os.Getenv("COMPOSER_FONT"); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			_ = registerTestFont(data, "ShotCJK")
		}
	}
	s := DefaultSettings()
	s.Dark = dark
	tk := Derive(s)
	tt := ui.NewTester(func(c *ui.Context) {
		th := c.Theme()
		ui.Column(c).Fill().Background(th.Background).Padding(0, 24, 24, 24).Justify(ui.End).Children(func() {
			draw(c, th, tk)
		})
	}, 2048, 1030)
	tt.SetScale(1.25)
	tt.SetDark(dark)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Frame()
	return tt, tk
}

func shotColors(th *ui.Theme) ComposerColors {
	return ComposerColors{Fill: th.Surface, Card: th.Background, Line: th.Border, Text: th.Text, Muted: th.TextMuted,
		Accent: th.Accent, OnAccent: th.AccentText, Danger: th.Danger}
}

func saveCrop(t *testing.T, tt *ui.Tester, dir, name string, r ui.Rect) {
	t.Helper()
	img := tt.Image()
	const scale = 1.25
	x0, y0 := int(r.X*scale), int(r.Y*scale)
	x1, y1 := int((r.X+r.W)*scale), int((r.Y+r.H)*scale)
	b := img.Bounds()
	crop := image.Rect(max(x0, b.Min.X), max(y0, b.Min.Y), min(x1, b.Max.X), min(y1, b.Max.Y))
	out := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	for y := crop.Min.Y; y < crop.Max.Y; y++ {
		for x := crop.Min.X; x < crop.Max.X; x++ {
			out.Set(x-crop.Min.X, y-crop.Min.Y, img.At(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s：裁切区域 %.0f×%.0f 逻辑像素，PNG %d×%d 物理像素", name, r.W, r.H, out.Bounds().Dx(), out.Bounds().Dy())
}

func grow(r ui.Rect, by float32) ui.Rect {
	return ui.Rect{X: r.X - by, Y: r.Y - by, W: r.W + 2*by, H: r.H + 2*by}
}

func thumb(t *testing.T, c1, c2 color.RGBA) ui.ImageSource {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 112, 112))
	for y := range 112 {
		for x := range 112 {
			f := float64(x+y) / 224
			img.Set(x, y, color.RGBA{R: uint8(float64(c1.R)*(1-f) + float64(c2.R)*f), G: uint8(float64(c1.G)*(1-f) + float64(c2.G)*f), B: uint8(float64(c1.B)*(1-f) + float64(c2.B)*f), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	src, err := ui.DecodeImage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestComposerShots(t *testing.T) {
	dir := os.Getenv("COMPOSER_SHOT")
	if dir == "" {
		t.Skip("设 COMPOSER_SHOT 才出图")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		tone := map[bool]string{false: "浅", true: "深"}[dark]

		// 一：两张引用卡加普通文字
		p := NewComposer()
		a := p.AddQuote(Quoted{MessageID: "m1", Name: "铃花", Text: strings.Repeat("第二句在那，这一段稍微长一点，用来看三行的省略效果。", 9)}, false)
		b := p.AddQuote(Quoted{MessageID: "m2", Name: "铃花", Text: "第三句收尾"}, false)
		a.Text, b.Text, p.Main.Text = "这句是什么意思", "这句我懂了", "另外今晚吃什么"
		tt, _ := shotRig(t, dark, func(c *ui.Context, th *ui.Theme, tk Tokens) {
			ui.Column(c).Width(1100).Children(func() { ComposerHost{T: tk, C: shotColors(th), Font: "ShotCJK"}.Draw(c, p) })
		})
		box, ok := tt.Find("输入区")
		if !ok {
			t.Fatal("没有输入区")
		}
		saveCrop(t, tt, dir, "引用卡-两张加普通文字-"+tone+".png", grow(box, 16))

		// 二：附件条（图片缩略图、文件、文件夹、长文件名）；一张卡上也有图
		p2 := NewComposer()
		card := p2.AddQuote(Quoted{MessageID: "m1", Name: "铃花", Text: "这张图里的字太小了"}, false)
		card.Text = "放大这一块看看"
		p2.Attach(card, Att{Kind: AttImage, Name: "截图.png", Path: "D:/files/截图.png", Thumb: thumb(t, color.RGBA{R: 90, G: 140, B: 230, A: 255}, color.RGBA{R: 240, G: 160, B: 200, A: 255})})
		p2.Attach(p2.Main, Att{Kind: AttImage, Name: "粘贴的图片.png", Path: "D:/data/go/media/local/ab/ab", Thumb: thumb(t, color.RGBA{R: 40, G: 170, B: 120, A: 255}, color.RGBA{R: 240, G: 220, B: 90, A: 255})})
		p2.Attach(p2.Main, Att{Kind: AttImage, Name: "保存中.png", Pending: true})
		p2.Attach(p2.Main, Att{Kind: AttFile, Name: "分镜脚本-第三稿.txt", Path: "D:/files/分镜脚本-第三稿.txt"})
		p2.Attach(p2.Main, Att{Kind: AttFolder, Name: "角色设定", Path: "D:/samples/persona"})
		p2.Attach(p2.Main, Att{Kind: AttFile, Name: "一个名字特别特别特别特别特别特别特别特别特别特别长的文件.psd", Path: "D:/files/长.psd"})
		p2.Main.Text = "把这些一起看一下"
		tt2, _ := shotRig(t, dark, func(c *ui.Context, th *ui.Theme, tk Tokens) {
			ui.Column(c).Width(1100).Children(func() { ComposerHost{T: tk, C: shotColors(th), Font: "ShotCJK"}.Draw(c, p2) })
		})
		box2, _ := tt2.Find("输入区")
		saveCrop(t, tt2, dir, "附件条-"+tone+".png", grow(box2, 16))

		// 二之二：一张引用卡加两个附件（一张图、一个文件夹）；粘贴图片、拖入文件后输入区的样子
		p3 := NewComposer()
		card3 := p3.AddQuote(Quoted{MessageID: "m1", Name: "铃花", Text: "这张图里的字太小了，你放大看看"}, false)
		card3.Text = "这一块放大看"
		p3.Attach(p3.Main, Att{Kind: AttImage, Name: "粘贴的图片.png", Path: "D:/data/go/media/local/ab/ab", Thumb: thumb(t, color.RGBA{R: 90, G: 140, B: 230, A: 255}, color.RGBA{R: 240, G: 160, B: 200, A: 255})})
		p3.Attach(p3.Main, Att{Kind: AttFolder, Name: "角色设定", Path: "D:/samples/persona"})
		p3.Main.Text = "顺便看看这个"
		tt3a, _ := shotRig(t, dark, func(c *ui.Context, th *ui.Theme, tk Tokens) {
			ui.Column(c).Width(1100).Children(func() { ComposerHost{T: tk, C: shotColors(th), Font: "ShotCJK"}.Draw(c, p3) })
		})
		box3a, ok := tt3a.Find("输入区")
		if !ok {
			t.Fatal("没有输入区")
		}
		saveCrop(t, tt3a, dir, "引用卡加两个附件-"+tone+".png", grow(box3a, 16))

		// 三：选区浮层
		var bar SelectionBar
		const text = "第一句在这。第二句在那。第三句收尾。"
		m := &Msg{ID: "m1", Sender: "铃花", Name: "铃花", Text: text, Blocks: segview.TextBlocks(text), At: time.UnixMilli(1000)}
		tt3, tk3 := shotRig(t, dark, func(c *ui.Context, th *ui.Theme, tk Tokens) {
			host := Host{T: tk, Font: "ShotCJK", Column: 760, NoEnter: true, Selection: &bar, Colors: &MessageColors{
				InBackground: th.Surface, InText: th.Text, InTime: th.TextMuted, InLink: th.Accent, OutBackground: th.Accent, OutText: th.AccentText, OutTime: th.AccentText, OutLink: th.AccentText},
				OnReply: func(Selected, bool) {}}
			ui.Column(c).Height(300).Justify(ui.Start).Children(func() { host.Message(c, m, nil, nil) })
		})
		_ = tk3
		rect, ok := tt3.Find(text)
		if !ok {
			t.Fatal("没有消息正文")
		}
		y := rect.Y + rect.H/2
		tt3.Press(rect.X+rect.W*0.08, y)
		tt3.Move(rect.X+rect.W*0.3, y)
		tt3.Move(rect.X+rect.W*0.55, y)
		tt3.Release(rect.X+rect.W*0.55, y)
		tt3.Frame()
		tt3.Advance(selectionSettle + 20*time.Millisecond)
		tt3.Frame()
		tt3.Frame()
		tool, ok := tt3.Find("选区操作")
		if !ok {
			t.Fatalf("没有弹出选区条：%v", tt3.Texts())
		}
		region := ui.Rect{X: rect.X - 24, Y: rect.Y - 24, W: rect.W + 48, H: tool.Y + tool.H - rect.Y + 48}
		saveCrop(t, tt3, dir, "选区浮层-"+tone+".png", region)

		// 四：发出去的分块气泡
		tt4, _ := shotRig(t, dark, func(c *ui.Context, th *ui.Theme, tk Tokens) {
			host := Host{T: tk, Font: "ShotCJK", Column: 760, NoEnter: true, SelfChar: CharColors[0], Colors: &MessageColors{
				InBackground: th.Surface, InText: th.Text, InTime: th.TextMuted, InLink: th.Accent, OutBackground: th.Accent.Alpha(0.16), OutText: th.Text, OutTime: th.TextMuted, OutLink: th.Accent}}
			me := &Msg{ID: "me1", Sender: "owner", Self: true, Name: "我", At: time.UnixMilli(1000), Parts: []Part{
				{Quote: &PartQuote{Target: "m1", Name: "铃花", Snippet: "第二句在那，这一段稍微长一点，用来看三行的省略效果。第二句在那，这一段稍微长一点，用来看三行的省略效果。"}, Text: "这句是什么意思", Atts: []PartAtt{{Name: "截图.png", Image: true}}},
				{Quote: &PartQuote{Target: "m2", Name: "铃花", Snippet: "第三句收尾"}, Text: "这句我懂了"},
				{Text: "另外今晚吃什么"},
			}}
			ui.Column(c).Width(1000).Children(func() { host.Message(c, me, nil, nil) })
		})
		bubble, _ := tt4.Find("气泡")
		saveCrop(t, tt4, dir, "发出的引用气泡-"+tone+".png", grow(bubble, 16))
	}
}
