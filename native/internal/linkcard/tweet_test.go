package linkcard

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"

	"sumika-native/internal/assets/fonts"
)

// 测试里的名字、账号、正文都是编的

// swatch 是一张编的图：两色渐变，让裁切和位置看得出来
func swatch(w, h int, a, b color.RGBA) *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(x+y) / float64(w+h)
			img.SetRGBA(x, y, color.RGBA{uint8(float64(a.R)*(1-t) + float64(b.R)*t), uint8(float64(a.G)*(1-t) + float64(b.G)*t), uint8(float64(a.B)*(1-t) + float64(b.B)*t), 255})
		}
	}
	return ui.NewBitmap(img)
}

var (
	sky   = color.RGBA{120, 180, 240, 255}
	peach = color.RGBA{250, 190, 150, 255}
	leaf  = color.RGBA{120, 200, 140, 255}
	plum  = color.RGBA{170, 120, 200, 255}
)

func sampleTweet() Tweet {
	return Tweet{
		Author:    TweetAuthor{Name: "纸云", Account: "paper_cloud", Verification: "blue", Avatar: swatch(96, 96, sky, plum)},
		Text:      "今天的纸船终于下水了，顺着小河漂了一整个下午。\nThe paper boat finally sailed today!",
		Published: time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC),
		Replies:   Stat{81, true}, Reposts: Stat{140, true}, Likes: Stat{10050, true}, Views: Stat{366000, true},
	}
}

// testRenderer 用本机的文渊圆体（worktree 里没有就去主工作区找）；找不到就跳过
func testRenderer(t *testing.T, o Options) *Renderer {
	t.Helper()
	font := fonts.WenYuanPath()
	if font == "" {
		t.Skip("找不到文渊圆体（native/fonts/WenYuanRoundedSCVF.ttf 不进 git），跳过要真字体的卡片测试")
	}
	if o.FontPaths == nil {
		o.FontPaths = []string{font}
	}
	if o.MaxHeight == nil {
		o.MaxHeight = func() int { return 960 } // 这里的测试按封顶 960 写；默认不封顶另有测试（TestTweetCapClamp）
	}
	r := NewWith(o)
	t.Cleanup(r.Close)
	return r
}

func fieldOf(t *testing.T, out Output, name string) FieldLayout {
	t.Helper()
	for _, f := range out.Layout {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("没有 %s：%+v", name, out.Layout)
	return FieldLayout{}
}

// 量版块的位置，和规格附录 C 的数值表（CSS 像素 × 720/550）比，误差不超过 1 像素。
// 行高照表里的“实际行高”：取 CSS 值和字体自身行高里大的那个（mygo 一行至少有字体的上下伸展高），用同一套字体量出来。
func TestTweetCardLayout(t *testing.T) {
	r := testRenderer(t, Options{Theme: func() string { return ThemeLight }})
	tw := sampleTweet()
	tw.Media = []TweetMedia{{Image: swatch(400, 300, sky, leaf)}, {Image: swatch(300, 400, peach, plum), Video: true}}
	tw.Quote = &Tweet{Author: TweetAuthor{Name: "纸灯", Account: "paper_lamp", Verification: "business", Avatar: swatch(40, 40, leaf, sky)}, Text: "被引用的那条。"}
	out, err := r.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	m := r.fonts.measurer()
	hl := float64(m.lineHeight(15, 20, 700)) // 头部一行（名字、@ID、引用头）
	ql := float64(m.lineHeight(15, 20, 400)) // 引用正文、时间一行
	_, bh32, _ := m.block(tw.Text, 20, 24, 400, 516, 0)
	bh := float64(bh32)
	if hl < 20 || ql < 20 || bh < 48 {
		t.Fatalf("实际行高不该比表里的 CSS 行高还小：%v %v %v", hl, ql, bh)
	}
	s := float64(720) / 550
	px := func(v float64) int { return int(math.Round(v * s)) }
	near := func(name string, got, want int) {
		if d := got - want; d < -1 || d > 1 {
			t.Errorf("%s：%d，表里是 %d", name, got, want)
		}
	}
	check := func(name string, x, y, w, h float64) FieldLayout {
		f := fieldOf(t, out, name)
		near(name+".x", f.X, px(x))
		near(name+".y", f.Y, px(y))
		if w > 0 {
			near(name+".w", f.W, px(x+w)-px(x))
		}
		if h > 0 {
			near(name+".h", f.H, px(y+h)-px(y))
		}
		return f
	}
	// 附录 C：卡片边框 1、内边距 12/16；头像 48 在 (17,13)；名字、@ID 各一行，左边 73，两行一起竖直居中于头像
	check("avatar", 17, 13, 48, 48)
	top := 13 + (48-2*hl)/2
	name := check("name", 73, top, 0, hl)
	check("handle", 73, top+hl, 0, hl)
	// 认证标 18.75，在名字右边 2，竖直居中于名字那一行
	b := fieldOf(t, out, "badge")
	near("badge.x", b.X, name.X+name.W+px(2))
	near("badge.y", b.Y, px(top+(hl-18.75)/2))
	near("badge.w", b.W, px(18.75))
	// 正文从 73 起（13+48+12），宽 516
	body := check("body", 17, 73, 516, bh)
	// 媒体：正文下 12，宽 516（含 1 像素框），两张时内高 514×0.5625，两格之间 2
	mediaY := 73 + bh + 12
	mediaH := math.Round(514 * 0.5625)
	check("media", 17, mediaY, 516, mediaH+2)
	check("media-0", 18, mediaY+1, 256, mediaH)
	check("media-1", 18+256+2, mediaY+1, 256, mediaH)
	// 引用框：媒体下 12，宽 516；里面内边距 12：头一行在框顶 +13，头像 20 在 30，名字在 58；正文在头一行下 4
	quoteY := mediaY + mediaH + 2 + 12
	check("quote", 17, quoteY, 516, 13+hl+4+ql+12+1)
	check("quote-avatar", 30, quoteY+13+(hl-20)/2, 20, 20)
	check("quote-name", 58, quoteY+13, 0, hl)
	check("quote-body", 30, quoteY+13+hl+4, 490, ql)
	// 时间：引用框下 12 再加 2
	tm := check("time", 17, quoteY+13+hl+4+ql+12+1+12+2, 516, ql)
	if fieldOf(t, out, "quote-badge").Text != "business" || b.Text != "blue" {
		t.Fatal("认证标种类不对")
	}
	// 照 X 网页单条推文页：浏览数跟在时间后面（底部互动排仍有浏览那一项）
	if tm.Text != "下午6:30 · 2026年11月1日 · 36.6万 查看" {
		t.Fatalf("时间按北京时间写、后面跟浏览数：%q", tm.Text)
	}
	// 互动行：时间下 4 一条 1 像素的线，再 4；每项 28 高
	reply := fieldOf(t, out, "action-reply")
	actionsY := quoteY + 13 + hl + 4 + ql + 12 + 1 + 12 + 2 + ql + 4 + 1 + 4
	near("action.y", reply.Y, px(actionsY))
	near("action.h", reply.H, px(actionsY+28)-px(actionsY))
	var acts []FieldLayout
	for _, a := range []struct{ name, text string }{{"action-reply", "81"}, {"action-repost", "140"}, {"action-like", "1万"}, {"action-views", "36.6万"}} {
		f := fieldOf(t, out, a.name)
		if f.Text != a.text {
			t.Errorf("%s：%q", a.name, f.Text)
		}
		acts = append(acts, f)
	}
	// 四项两端对齐、等距：第一项图标框在内容左边 -4（13），最后一项数字右端贴内容右边（533），空隙一样大
	near("action-first.x", acts[0].X, px(13))
	near("action-last.right", acts[3].X+acts[3].W, px(533))
	for i := 1; i < len(acts)-1; i++ {
		g0 := acts[i].X - (acts[i-1].X + acts[i-1].W)
		g1 := acts[i+1].X - (acts[i].X + acts[i].W)
		near(fmt.Sprintf("action-gap-%d", i), g1, g0)
	}
	// 底边：互动行下 12 内边距和 1 像素框
	near("height", out.Height, px(actionsY+28+12+1))
	if out.Width != 720 || out.Height > 960 {
		t.Fatalf("尺寸：%dx%d", out.Width, out.Height)
	}
	// 图解得开、正文那块看得到深色字
	img, _, err := image.Decode(bytes.NewReader(out.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	ink := 0
	for y := body.Y; y < body.Y+body.H; y++ {
		for x := body.X; x < body.X+body.W; x++ {
			if r, g, b, _ := img.At(x, y).RGBA(); r < 20000 && g < 20000 && b < 20000 {
				ink++
			}
		}
	}
	if ink < 200 {
		t.Fatalf("正文没画出来：%d", ink)
	}
}

// 取不到的互动数不画（不画成 0），没有认证信息不画认证标
func TestTweetCardOmitsMissing(t *testing.T) {
	r := testRenderer(t, Options{})
	tw := sampleTweet()
	tw.Author.Verification = ""
	tw.Reposts, tw.Views = Stat{}, Stat{}
	tw.Published = time.Time{}
	out, err := r.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Layout {
		switch f.Name {
		case "badge", "action-repost", "action-views", "time":
			t.Fatalf("取不到的 %s 不该画：%+v", f.Name, f)
		}
	}
	if fieldOf(t, out, "action-like").Text != "1万" {
		t.Fatal("点赞")
	}
	// 没有时间、有浏览数：时间行只写浏览数；有时间、没有浏览数：只写时间
	tw.Views = Stat{182, true}
	if out, err = r.RenderTweet(context.Background(), tw); err != nil {
		t.Fatal(err)
	}
	if f := fieldOf(t, out, "time"); f.Text != "182 查看" {
		t.Fatalf("只有浏览数：%q", f.Text)
	}
	tw.Views, tw.Published = Stat{}, time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC)
	if out, err = r.RenderTweet(context.Background(), tw); err != nil {
		t.Fatal(err)
	}
	if f := fieldOf(t, out, "time"); f.Text != "下午6:30 · 2026年11月1日" {
		t.Fatalf("只有时间：%q", f.Text)
	}
}

// 相邻的全角标点挤掉半个字宽（chws），单个标点、汉字、拉丁字母的宽度不变；量和画同一套
func TestTweetCardPunctTrim(t *testing.T) {
	r := testRenderer(t, Options{})
	if _, err := r.RenderTweet(context.Background(), sampleTweet()); err != nil { // 让字体加载
		t.Fatal(err)
	}
	m := r.fonts.measurer()
	plain := func(s string) float32 {
		w, _ := ui.ShapeText(s, ui.Font{Family: m.family, Size: 20}, 0).Size()
		return w
	}
	got := m.width("謎？！A", 20, 400)
	if d := plain("謎？！A") - got; d < 9 || d > 11 {
		t.Fatalf("“？！”应挤掉半个字宽（10）：挤了 %.2f", d)
	}
	// 拉丁文那句含 fi 连字：只开 chws 会把默认的 liga 关掉、宽度变（DirectWrite 的特性表是整张替换）
	for _, s := range []string{"なぜ？謎", "今天的纸船终于下水了，顺着小河漂了一整个下午。", "The paper boat finally sailed today!", "office -> != www"} {
		if d := plain(s) - m.width(s, 20, 400); d < -0.01 || d > 0.01 {
			t.Errorf("%q 不该变宽窄：%.2f", s, d)
		}
	}
}

// 超长正文、四图加引用：收正文行数，最后一行加“…”，整张不超过 960
func TestTweetCardLongFits(t *testing.T) {
	r := testRenderer(t, Options{})
	tw := sampleTweet()
	tw.Text = strings.Repeat("很长很长的一段正文，中英混排 mixed text 也要排得下。", 40)
	tw.Media = []TweetMedia{{Image: swatch(200, 200, sky, leaf)}, {Image: swatch(200, 200, peach, plum)}, {}, {Image: swatch(200, 200, plum, sky)}, {Image: swatch(200, 200, leaf, peach)}}
	tw.Quote = &Tweet{Author: TweetAuthor{Name: "纸灯", Account: "paper_lamp"}, Text: strings.Repeat("引用的正文也很长。", 30)}
	out, err := r.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Height > 960 {
		t.Fatalf("超高：%d", out.Height)
	}
	body := fieldOf(t, out, "body")
	if lines := int(math.Round(float64(body.H) / (24 * 720.0 / 550))); lines < 3 {
		t.Fatalf("正文至少留 3 行：%d", lines)
	}
	if fieldOf(t, out, "media-3").W == 0 {
		t.Fatal("四格")
	}
}

// 05:59 深、06:00 浅、17:59 浅、18:00 深（北京时间）；本机时区设成别的也一样；设置写死就照设置
func TestTweetCardThemeByBeijingTime(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("UTC-7", -7*3600)
	defer func() { time.Local = old }()
	at := func(h, m int) time.Time { return time.Date(2026, 11, 1, h, m, 0, 0, beijing).In(time.Local) }
	for _, c := range []struct {
		h, m int
		want string
	}{{5, 59, ThemeDark}, {6, 0, ThemeLight}, {17, 59, ThemeLight}, {18, 0, ThemeDark}, {0, 0, ThemeDark}, {12, 0, ThemeLight}} {
		if got := ThemeAt(ThemeAuto, at(c.h, c.m)); got != c.want {
			t.Errorf("%02d:%02d：%s，应为 %s", c.h, c.m, got, c.want)
		}
	}
	if ThemeAt(ThemeLight, at(23, 0)) != ThemeLight || ThemeAt(ThemeDark, at(12, 0)) != ThemeDark || ThemeAt("", at(12, 0)) != ThemeLight {
		t.Fatal("固定配色、没写")
	}
	// 渲染器用注入的时钟和设置：画出来的底色跟着变
	now := at(5, 59)
	setting := ThemeAuto
	r := testRenderer(t, Options{Now: func() time.Time { return now }, Theme: func() string { return setting }})
	bg := func() (string, color.Color) {
		out, err := r.RenderTweet(context.Background(), sampleTweet())
		if err != nil {
			t.Fatal(err)
		}
		img, _, _ := image.Decode(bytes.NewReader(out.Bytes))
		return out.Theme, img.At(360, 6)
	}
	if th, c := bg(); th != ThemeDark || !sameColor(c, color.RGBA{21, 32, 43, 255}) {
		t.Fatalf("05:59 应深色：%s %v", th, c)
	}
	now = at(6, 0)
	if th, c := bg(); th != ThemeLight || !sameColor(c, color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("06:00 应浅色：%s %v", th, c)
	}
	setting = ThemeDark
	if th, _ := bg(); th != ThemeDark {
		t.Fatal("写死 dark")
	}
}

func sameColor(a color.Color, b color.RGBA) bool {
	r, g, bb, _ := a.RGBA()
	d := func(x uint32, y uint8) bool { return math.Abs(float64(x>>8)-float64(y)) <= 2 }
	return d(r, b.R) && d(g, b.G) && d(bb, b.B)
}

// 缺字检查：文渊圆体缺的字走 Maple Mono，再缺交系统字体；表情单独数。逻辑用 Go 自带的小字体测（不靠本机字体）
func TestTweetCardGlyphFallback(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// goregular、gomono 都有拉丁字母、都没有汉字：两个都缺的汉字要落到“交系统”；两份字表不同的那些字用来测第二层。
	primary := write("primary.ttf", goRegular())
	fallback := write("fallback.ttf", goMono())
	var registered []string
	reg := func(_ []byte, family string) error { registered = append(registered, family); return nil }
	fs := loadFonts(Options{FontPaths: []string{filepath.Join(dir, "missing.ttf"), primary}, FallbackFontPaths: []string{fallback}}, reg, "Theme Font")
	if fs.primary == nil || fs.fallback == nil || fs.family != familyPrimary+", "+familyFallback+", Theme Font" {
		t.Fatalf("字体：%+v", fs)
	}
	cov := fs.check("Hello 世界 😀", "第二行也查 ≥")
	if cov.Primary == 0 || cov.Emoji != 1 {
		t.Fatalf("覆盖：%+v", cov)
	}
	for _, r := range "世界第二行也查" {
		if !strings.ContainsRune(string(cov.Uncovered), r) {
			t.Fatalf("%c 两个字体都没有，应交系统：%+v", r, cov)
		}
	}
	// 主字体没有、第二个字体有的才算“Maple 兜住”：goregular 和 gomono 字表一样，这一层用替身字表测
	layered := &fontSet{primary: &fontFace{cover: func(r rune) bool { return r < 0x80 }}, fallback: &fontFace{cover: func(r rune) bool { return r == '世' || r == '★' }}}
	cov = layered.check("A世界★😀", "第二行的字也查：A")
	if string(cov.ByFallback) != "★世" || !strings.ContainsRune(string(cov.Uncovered), '界') || !strings.ContainsRune(string(cov.Uncovered), '查') || cov.Emoji != 1 || cov.Primary != 1 {
		t.Fatalf("分层不对：%+v", cov)
	}
	// 文渊圆体没加载、只有 Maple：等宽的 Maple 不能排第一，主题字体在前（原来正文整段画成等宽，#41）
	monoOnly := loadFonts(Options{FontPaths: []string{filepath.Join(dir, "a.ttf")}, FallbackFontPaths: []string{fallback}, EmojiDir: dir}, reg, "Theme Font")
	if monoOnly.family != "Theme Font, "+familyFallback {
		t.Fatalf("只有 Maple 时的字族：%q", monoOnly.family)
	}
	// 字体全找不到：有日志说明，画字退回主题字体
	// （Twemoji 目录也指到一个空目录：三条加载问题，表情交字体画）
	none := loadFonts(Options{FontPaths: []string{filepath.Join(dir, "a.ttf")}, FallbackFontPaths: []string{}, EmojiDir: dir}, reg, "Theme Font")
	if none.family != "Theme Font" || len(none.issues) != 3 || !strings.Contains(none.issues[0], "文渊圆体没加载") || !strings.Contains(none.issues[2], "Twemoji") || none.emoji != nil {
		t.Fatalf("找不到字体要留原因：%+v", none)
	}
	if c := none.check("字😀"); len(c.Uncovered) != 1 || len(c.Issues) != 3 || c.Emoji != 1 {
		t.Fatalf("%+v", c)
	}
	// 真字体：文渊圆体 + Maple 的实际覆盖（本机没有就跳过）
	font := fonts.WenYuanPath()
	maple := fallbackCandidates(Options{})
	if font == "" {
		t.Skip("找不到文渊圆体，真字体那段跳过")
	}
	real := loadFonts(Options{FontPaths: []string{font}}, reg, "")
	if real.primary == nil {
		t.Fatalf("文渊圆体读不了：%v", real.issues)
	}
	if real.fallback == nil {
		t.Logf("没装 Maple Mono NF CN（找了 %v）", maple)
	}
	c := real.check("文渊圆体 Hello ㊗ ⑳ 𠀋 ™ ★ ←→ ✓ ①")
	t.Logf("真字体：%s；Maple 兜住 %q；交系统 %q；加载问题 %v", c.Summary(), string(c.ByFallback), string(c.Uncovered), real.issues)
}

// scanCoverage 按字符区段数文渊圆体缺多少、Maple 兜住多少（样图报告用）
func scanCoverage(fs *fontSet) string {
	var b strings.Builder
	for _, blk := range []struct {
		name     string
		from, to rune
	}{
		{"基本拉丁和拉丁补充", 0x20, 0x24F}, {"希腊、西里尔", 0x370, 0x4FF}, {"常用标点", 0x2000, 0x206F}, {"字母式符号、数字形式", 0x2100, 0x218F},
		{"箭头", 0x2190, 0x21FF}, {"数学运算符", 0x2200, 0x22FF}, {"杂项技术符号", 0x2300, 0x23FF}, {"带圈字母数字", 0x2460, 0x24FF},
		{"制表符、方块", 0x2500, 0x259F}, {"几何图形", 0x25A0, 0x25FF}, {"杂项符号", 0x2600, 0x26FF}, {"装饰符号", 0x2700, 0x27BF},
		{"盲文", 0x2800, 0x28FF}, {"中日韩符号和标点", 0x3000, 0x303F}, {"平假名", 0x3040, 0x309F}, {"片假名", 0x30A0, 0x30FF},
		{"注音", 0x3100, 0x312F}, {"带圈中日韩字", 0x3200, 0x32FF}, {"中日韩兼容", 0x3300, 0x33FF}, {"汉字扩展 A", 0x3400, 0x4DBF},
		{"汉字基本区", 0x4E00, 0x9FFF}, {"韩文音节", 0xAC00, 0xD7A3}, {"私用区（Nerd Font 图标）", 0xE000, 0xF8FF}, {"兼容汉字", 0xF900, 0xFAFF},
		{"竖排、兼容形式", 0xFE10, 0xFE4F}, {"全角半角", 0xFF00, 0xFFEF}, {"汉字扩展 B", 0x20000, 0x2A6DF},
	} {
		total, missing, maple := 0, 0, 0
		var sample []rune
		for r := blk.from; r <= blk.to; r++ {
			if skipRune(r) || !unicode.IsPrint(r) && !(r >= 0xE000 && r <= 0xF8FF) {
				continue
			}
			total++
			if fs.primary.has(r) {
				continue
			}
			missing++
			if fs.fallback.has(r) {
				maple++
			} else if len(sample) < 12 {
				sample = append(sample, r)
			}
		}
		fmt.Fprintf(&b, "  %s（U+%04X–U+%04X）：%d 个字，文渊圆体缺 %d，Maple 兜住 %d，两个都缺 %d（例：%s）\n", blk.name, blk.from, blk.to, total, missing, maple, missing-maple, string(sample))
	}
	return b.String()
}

// 样图：设了 TWEETCARD_OUT 才写（浅色、深色、带图、带引用、长文、中日英混排、表情），外加字体覆盖报告
func TestTweetCardSamples(t *testing.T) {
	dir := os.Getenv("TWEETCARD_OUT")
	if dir == "" {
		t.Skip("设 TWEETCARD_OUT=<目录> 才出样图")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	theme := ThemeLight
	r := testRenderer(t, Options{Theme: func() string { return theme }})
	base := sampleTweet()
	withMedia := base
	withMedia.Media = []TweetMedia{{Image: swatch(1200, 800, sky, leaf)}, {Image: swatch(800, 1200, peach, plum), Video: true}, {Image: swatch(800, 800, plum, sky)}}
	withQuote := base
	withQuote.Text = "转一下这条，说得太对了 👇"
	withQuote.Quote = &Tweet{Author: TweetAuthor{Name: "纸灯 Lantern", Account: "paper_lamp", Verification: "government", Avatar: swatch(40, 40, leaf, sky)},
		Text: "引用里的正文：明天下午三点在河边集合，记得带上纸和剪刀。", Media: []TweetMedia{{Image: swatch(1200, 675, peach, sky)}}}
	long := base
	long.Text = strings.Repeat("这是一条很长的推文，用来看长文截断和行距。Long tweets get clipped with an ellipsis. ", 30)
	long.Media = []TweetMedia{{Image: swatch(600, 600, sky, plum)}, {Image: swatch(600, 600, leaf, peach)}, {}, {Image: swatch(600, 600, plum, leaf)}, {Image: swatch(600, 600, peach, sky)}}
	mixed := base
	mixed.Author = TweetAuthor{Name: "かみふうせん", Account: "kami_fusen", Verification: "business", Avatar: swatch(96, 96, peach, leaf)}
	mixed.Text = "中文、日本語（ひらがな・カタカナ）、English mixed。\n全角标点：「引号」『书名』——破折号……省略号。\n罕见字：𠀋𪚥𰻞 ㊗ ⑳ ™ ★ ✓ ←→"
	emoji := base
	emoji.Text = "表情测试 😀😂🥹👍🏻👨‍👩‍👧 🇯🇵 ❤️ ✨🎉 #纸船 @paper_lamp"
	type sample struct {
		name  string
		theme string
		t     Tweet
	}
	var report strings.Builder
	for _, s := range []sample{{"light", ThemeLight, base}, {"dark", ThemeDark, base}, {"media", ThemeLight, withMedia}, {"quote", ThemeDark, withQuote}, {"long", ThemeLight, long}, {"mixed", ThemeLight, mixed}, {"emoji", ThemeDark, emoji}} {
		theme = s.theme
		began := time.Now()
		out, err := r.RenderTweet(context.Background(), s.t)
		took := time.Since(began)
		if err != nil {
			t.Fatal(s.name, err)
		}
		ext := ".png"
		if out.MIME == "image/jpeg" {
			ext = ".jpg"
		}
		if err := os.WriteFile(filepath.Join(dir, s.name+ext), out.Bytes, 0o644); err != nil {
			t.Fatal(err)
		}
		c := out.Coverage
		fmt.Fprintf(&report, "%s：%dx%d %s %d 字节，渲染 %d 毫秒；字 %d 个，文渊圆体有 %d，表情 %d，文渊圆体缺 %d（Maple 兜住 %d：%s；交系统 %d：%s）\n",
			s.name, out.Width, out.Height, out.MIME, len(out.Bytes), took.Milliseconds(), c.Total, c.Primary, c.Emoji, len(c.Missing), len(c.ByFallback), string(c.ByFallback), len(c.Uncovered), string(c.Uncovered))
	}
	report.WriteString("\n字体：" + r.fonts.family + "\n文渊圆体：" + r.fonts.primary.path + "\n")
	if r.fonts.fallback != nil {
		report.WriteString("Maple：" + r.fonts.fallback.path + "\n")
	}
	for _, s := range r.fonts.issues {
		report.WriteString("加载问题：" + s + "\n")
	}
	report.WriteString("按区段的缺字（文渊圆体缺的字，Maple Mono NF CN 兜住多少）：\n" + scanCoverage(r.fonts))
	if err := os.WriteFile(filepath.Join(dir, "coverage.txt"), []byte(report.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + report.String())
}

func goRegular() []byte { return goregular.TTF }
func goMono() []byte    { return gomono.TTF }

// 正文因高度封顶排不下：不再硬夹在几行里（末行被省略号吃掉半句），而是截在最近的标点后，补“……”，最后一行写“完整内容见原文”
// （用户 2026-11-13，R-220）；排得下的短推文原样画。
func TestTweetCardLongBodyCutsAtPunctuation(t *testing.T) {
	r := testRenderer(t, Options{})
	tw := sampleTweet()
	tw.Text = strings.Repeat("很长很长的一段正文，中英混排，mixed text，也要排得下。", 40)
	tw.Media = []TweetMedia{{Image: swatch(200, 200, sky, leaf)}, {Image: swatch(200, 200, peach, plum)}}
	tw.Quote = &Tweet{Author: TweetAuthor{Name: "纸灯", Account: "paper_lamp"}, Text: strings.Repeat("引用的正文也很长。", 30)}
	out, err := r.RenderTweet(context.Background(), tw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Height > 960 {
		t.Fatalf("超高：%d", out.Height)
	}
	for _, name := range []string{"body", "quote-body"} {
		got := fieldOf(t, out, name).Text
		if !strings.HasSuffix(got, "……\n完整内容见原文") {
			t.Fatalf("%s 要截在标点后补省略号和提示：%q", name, got)
		}
		head := strings.TrimSuffix(got, "……\n完整内容见原文")
		if !strings.HasSuffix(head, "。") && !strings.HasSuffix(head, "，") {
			t.Fatalf("%s 的截点应在标点后：%q", name, head[max(0, len(head)-30):])
		}
	}
	short := sampleTweet()
	out, err = r.RenderTweet(context.Background(), short)
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldOf(t, out, "body").Text; got != short.Text || strings.Contains(got, "完整内容见原文") {
		t.Fatalf("短推文不该动：%q", got)
	}
}
