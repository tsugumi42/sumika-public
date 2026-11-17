package linkexpand

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 2026-11-09 主人在测试群里挑定的默认排版：B 站视频 3b、多 P 规则、X 多图规则（规格-链接解析模板“默认排版”一节）。

func TestCompactNumbers(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 999: "999", 9999: "9999", 10000: "1万", 12345: "1.2万", 99949: "10万", 297500: "29.8万", 2975000: "297.5万", 1566000: "156.6万", 1293000: "129.3万",
		99_990_000: "9999万", 99_999_999: "1亿", 100_000_000: "1亿", 108_000_000: "1.08亿", 108_499_999: "1.08亿", 1_234_567_890: "12.35亿", -12345: "-1.2万",
	} {
		if got := compact(n); got != want {
			t.Errorf("compact(%d) = %q，应为 %q", n, got, want)
		}
	}
}

// 默认排版（规格-链接发送改版 B8）：封面、标题、UP主 | 时长、播放｜点赞、投稿时间、链接，行间不空行；简介、热评不写。
func TestBiliVideoChosenLayout(t *testing.T) {
	r := goldenBili(1)
	r.CleanURL = "https://www.bilibili.com/video/BV1xx411c7mD"
	r = withStats(r, map[string]int64{"view": 108_000_000, "like": 2_975_000, "favorite": 1_566_000, "coin": 1_293_000})
	r.Comments = av([]linkreader.Comment{{Author: "用户甲", Text: "好听", Likes: 7}})
	r.Text = av("这是编的简介")
	d := build(r, Options{})
	body := d.Parts[0].Body
	want := "纸船试航\n纸灯实验室 | 02:15\n播放 1.08亿 | 点赞 297.5万\n" + shortStamp(r.Published).text + "\nhttps://www.bilibili.com/video/BV1xx411c7mD"
	if len(body) != 2 || body[0].Kind != types.BodyImage || body[1].Text != want {
		t.Fatalf("%+v", body)
	}
	for _, bad := range []string{"B站视频", "上传", "弹幕", "转发", "评论 ", "收藏", "投币", "热评", "这是编的简介", "UP主："} {
		if strings.Contains(body[1].Text, bad) {
			t.Errorf("不该出现 %q", bad)
		}
	}
}

// 字段缺失时整行、整段自然消失：不留空行、孤立的竖线。
func TestBiliVideoLayoutDropsMissing(t *testing.T) {
	r := goldenBili(1)
	r.Stats = map[string]linkreader.Field[int64]{"coin": av(int64(10))}
	r.SelectedPart = linkreader.Field[linkreader.VideoPart]{State: "not_returned"}
	got := build(r, Options{}).Parts[0].Body[1].Text
	stamp := shortStamp(r.Published).text
	if want := "纸船试航\n纸灯实验室\n" + stamp + "\nhttps://example.invalid/bilibili/BV1xx411c7mD"; got != want {
		t.Fatalf("\n%q\n%q", got, want)
	}
	r.Published = linkreader.Field[time.Time]{State: "not_returned"}
	got = build(r, Options{}).Parts[0].Body[1].Text
	if want := "纸船试航\n纸灯实验室\nhttps://example.invalid/bilibili/BV1xx411c7mD"; got != want {
		t.Fatalf("\n%q\n%q", got, want)
	}
	// 简介为空、只有横线：不显示，也不留空行
	for _, desc := range []string{"", "-", " - - ", "——"} {
		r.Text = av(desc)
		if got = build(r, Options{}).Parts[0].Body[1].Text; strings.Contains(got, "\n\n") || strings.Contains(got, "-") || strings.Contains(got, "——") {
			t.Fatalf("简介 %q 不该显示：%q", desc, got)
		}
	}
	// 有内容的简介默认也不写（B8）
	r.Text = av("有内容的简介")
	if got = build(r, Options{}).Parts[0].Body[1].Text; strings.Contains(got, "有内容的简介") {
		t.Fatalf("简介：%q", got)
	}
	r.Text = av("")
	r.Author.Name = linkreader.Field[string]{State: "not_returned"}
	got = build(r, Options{}).Parts[0].Body[1].Text
	if want := "纸船试航\nhttps://example.invalid/bilibili/BV1xx411c7mD"; got != want {
		t.Fatalf("\n%q\n%q", got, want)
	}
}

func partsOf(durations ...int) linkreader.Field[[]linkreader.VideoPart] {
	var ps []linkreader.VideoPart
	for i, d := range durations {
		ps = append(ps, linkreader.VideoPart{Number: i + 1, Title: av(fmt.Sprintf("第%d局", i+1)), Duration: av(d)})
	}
	return av(ps)
}

// 多 P：链接带 ?p= 时分 P 单独一行放在标题下，信息行写“本P 时长”；
// 不带 p 时信息行写“共 8P | 总长”，不超过 10P 再在标题下列出全部分 P；超过 10P 不列。
func TestBiliVideoMultiPart(t *testing.T) {
	text := func(r linkreader.Result) string { return build(r, Options{}).Parts[0].Body[1].Text }
	base := func(n int, url string) linkreader.Result {
		r := goldenBili(1)
		r.CleanURL = url
		var ds []int
		for i := 1; i <= n; i++ {
			ds = append(ds, 3061)
		}
		r.Parts = partsOf(ds...)
		r.SelectedPart = av(r.Parts.Value[0])
		return r
	}

	// 带 p：P12/168：名字
	r := base(168, "https://www.bilibili.com/video/BV1xx411c7mD?p=12")
	r.SelectedPart = av(linkreader.VideoPart{Number: 12, Title: av("理解数据代理"), Duration: av(179)})
	got := text(r)
	if !strings.HasPrefix(got, "纸船试航\nP12/168：理解数据代理\n纸灯实验室 | 本P 02:59\n") || strings.Contains(got, "共 168P") || strings.Contains(got, "P1 ") || !strings.HasSuffix(got, "BV1xx411c7mD?p=12") {
		t.Fatalf("带 p：\n%s", got)
	}
	// 带 p=1 也一样写分 P 行
	r = base(8, "https://www.bilibili.com/video/BV1xx411c7mD?p=1")
	if got = text(r); !strings.Contains(got, "\nP1/8：第1局\n") || !strings.Contains(got, " | 本P 51:01\n") {
		t.Fatalf("p=1：\n%s", got)
	}
	// 不带 p、8P：列出全部分 P，信息行写共几 P 和总长（总长满一小时写 h:mm:ss）
	r = base(8, "https://www.bilibili.com/video/BV1xx411c7mD")
	got = text(r)
	if !strings.HasPrefix(got, "纸船试航\nP1 第1局（51:01）\nP2 第2局（51:01）\n") || !strings.Contains(got, "P8 第8局（51:01）\n纸灯实验室 | 共 8P | 总长 6:48:08\n") {
		t.Fatalf("不带 p：\n%s", got)
	}
	if strings.Contains(got, "P1/8") {
		t.Fatalf("不带 p 不写“P1/8”：\n%s", got)
	}
	// 不带 p、恰好 10P 还列；11P 不列但仍写共几 P
	if got = text(base(10, "https://www.bilibili.com/video/BV1xx411c7mD")); !strings.Contains(got, "P10 第10局（51:01）") {
		t.Fatalf("10P：\n%s", got)
	}
	got = text(base(11, "https://www.bilibili.com/video/BV1xx411c7mD"))
	if strings.Contains(got, "P1 ") || !strings.Contains(got, "共 11P | 总长 9:21:11") {
		t.Fatalf("11P：\n%s", got)
	}
	// 某个分 P 没有时长：总长不编，只写共几 P
	r = base(3, "https://www.bilibili.com/video/BV1xx411c7mD")
	r.Parts.Value[1].Duration = linkreader.Field[int]{State: "not_returned"}
	got = text(r)
	if !strings.Contains(got, "P2 第2局\n") || !strings.Contains(got, " | 共 3P\n") || strings.Contains(got, "总长") {
		t.Fatalf("缺时长：\n%s", got)
	}
	// 单 P 视频照旧：既没有分 P 行也没有列表，信息行只写时长
	r = goldenBili(1)
	if got = text(r); !strings.Contains(got, "纸灯实验室 | 02:15\n") || strings.Contains(got, "P1") || strings.Contains(got, "共 ") {
		t.Fatalf("单 P：\n%s", got)
	}
}

// 展示的链接保留 ?p= 和时间点（取数用的干净链接就是展示的链接）。
func TestBiliVideoLinkKeepsPartAndTime(t *testing.T) {
	r := goldenBili(3)
	r.CleanURL = "https://www.bilibili.com/video/BV1xx411c7mD?p=2&t=90"
	got := build(r, Options{}).Parts[0].Body[1].Text
	if !strings.HasSuffix(got, "\nhttps://www.bilibili.com/video/BV1xx411c7mD?p=2&t=90") || strings.Contains(got, "\n\n") {
		t.Fatalf("\n%s", got)
	}
	l, ok := linkreader.Recognize("https://www.bilibili.com/video/BV1xx411c7mD/?spm_id_from=x&p=12&t=30&vd_source=y")
	if !ok || l.Clean != "https://www.bilibili.com/video/BV1xx411c7mD?p=12&t=30" {
		t.Fatalf("干净链接：%+v", l)
	}
}

func xImages(n int) linkreader.Result {
	r := goldenBase("x", "post", "80002")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper_cloud")
	r.Published = av(time.Date(nowFunc().Year(), 11, 7, 15, 28, 0, 0, time.UTC)) // 北京时间 23:28
	r.Text = av("第一行\n第二行")
	r.Expansion = av("short")
	r = withStats(r, map[string]int64{"likes": 1005, "reposts": 140, "replies": 81, "quotes": 3, "bookmarks": 9, "views": 366_000})
	r.Blocks = []linkreader.Block{{Type: "text", Text: "第一行\n第二行"}}
	var ms []linkreader.Media
	for i := 1; i <= n; i++ {
		m := goldenImage(i)
		ms = append(ms, m)
	}
	r.Media = av(ms)
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "image", Media: &r.Media.Value[i]})
	}
	return r
}

func itemKinds(items []Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Kind)
	}
	return strings.Join(out, ",")
}

// 发帖时间一行在「正文」下面（用户 2026-11-12 定）；xImages 的发帖时间是今年 11-07 23:28（北京时间）
const xChosenText = "https://example.invalid/x/80002\n纸云[@paper_cloud]：\n「第一行\n第二行」\n11-07 23:28"

// xMosaicTpl 是写了 $拼图 的自定义模板：内置默认不再用拼图（规格-链接发送改版 B2、B3），写了才发。
const xMosaicTpl = "$拼图$作者\n$链接$图片$视频"

// 默认排版（规格-链接发送改版 B2）：0 到 2 张图，一条消息，链接、名字[@ID]：、「正文」、发帖时间，图在下；统计不写。
func TestXChosenLayoutUpToTwoImages(t *testing.T) {
	for n := 0; n <= 2; n++ {
		d := build(xImages(n), Options{})
		if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
			t.Fatalf("%d 张图：%+v", n, kinds(d))
		}
		body := d.Parts[0].Body
		want := "text" + strings.Repeat(",image", n)
		if itemKinds(body) != want || body[0].Text != xChosenText {
			t.Fatalf("%d 张图：%s\n%s", n, itemKinds(body), body[0].Text)
		}
	}
	// 不带统计的帖子：一样，没有残留
	r := xImages(1)
	r.Stats = map[string]linkreader.Field[int64]{}
	got := build(r, Options{}).Parts[0].Body[0].Text
	if got != xChosenText {
		t.Fatalf("\n%q\n%q", got, xChosenText)
	}
	// 只有图没有文字：没有空的「」，时间照旧
	r = xImages(1)
	r.Text, r.Blocks = av(""), r.Blocks[1:]
	if got = build(r, Options{}).Parts[0].Body[0].Text; got != "https://example.invalid/x/80002\n纸云[@paper_cloud]\n11-07 23:28" { // 没有正文不挂冒号（用户 2026-11-13）
		t.Fatalf("\n%q", got)
	}
}

func TestShortStampOmitsCurrentYear(t *testing.T) {
	now := nowFunc().In(displayZone)
	cur := av(time.Date(now.Year(), 3, 4, 5, 6, 0, 0, displayZone))
	if got := shortStamp(cur).text; got != "03-04 05:06" {
		t.Fatalf("今年：%q", got)
	}
	if got := shortStamp(av(time.Date(2020, 3, 4, 5, 6, 0, 0, displayZone))).text; got != "2020-03-04 05:06" {
		t.Fatalf("往年：%q", got)
	}
	if shortStamp(linkreader.Field[time.Time]{State: "not_returned"}).ok {
		t.Fatal("没取到不该有值")
	}
}

func withMosaic(r linkreader.Result) linkreader.Result {
	r.Mosaic = av(linkreader.Media{Type: "image", URL: av("https://mosaic.fxtwitter.com/jpeg/80002/a/b/c")})
	return r
}

// 自定义模板写了 $拼图（以前的“推 7”）：三张及以上，拼图在上、文字在下，一条消息；拼图取不到就走合并转发。
// 内置默认不写 $拼图，有拼图字段也不用。
func TestXMosaicLayout(t *testing.T) {
	mo := Options{Templates: map[string]string{"x": xMosaicTpl}, ForwardVideo: true} // 视频留在转发里（生产默认），这里只看容器
	for _, n := range []int{3, 4} {
		d := build(withMosaic(xImages(n)), mo)
		if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
			t.Fatalf("%d 张图应发一条消息：%v", n, kinds(d))
		}
		body := d.Parts[0].Body
		if itemKinds(body) != "image,text" || body[0].Media.URL.Value != "https://mosaic.fxtwitter.com/jpeg/80002/a/b/c" || body[1].Text != "纸云 @paper_cloud\nhttps://example.invalid/x/80002" {
			t.Fatalf("%d 张图：%+v", n, body)
		}
	}
	// 内置默认：拼图字段在也不发拼图
	for _, it := range build(withMosaic(xImages(2)), Options{}).Parts[0].Body {
		if it.Media.URL.Value == "https://mosaic.fxtwitter.com/jpeg/80002/a/b/c" {
			t.Fatal("内置默认不该用拼图")
		}
	}
	// 没有拼图：合并转发，和以前一样
	d := build(xImages(4), mo)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("没有拼图：%v", kinds(d))
	}
	// 拼图字段在，但地址没通过检查（主机不在名单里之类）：也走合并转发
	r := withMosaic(xImages(4))
	r.Mosaic.Value.URL = linkreader.Field[string]{State: "failed", Reason: "unsafe_target"}
	if d = build(r, mo); len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("拼图地址不安全：%v", kinds(d))
	}
	// 两张图有拼图字段也不用拼图
	d = build(withMosaic(xImages(2)), mo)
	if itemKinds(d.Parts[0].Body) != "text,image,image" {
		t.Fatalf("两张图：%s", itemKinds(d.Parts[0].Body))
	}
	// 有视频不用拼图（视频、长文、引用、投票的规则不变）
	r = withMosaic(xImages(3))
	v := goldenVideo()
	r.Media.Value = append(r.Media.Value, v)
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "video", Media: &v})
	if d = build(r, mo); len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("图加视频仍走合并转发：%v", kinds(d))
	}
}

// 设置里的合并转发模式和手写模板仍然优先。
func TestXSettingsStillWin(t *testing.T) {
	r := withMosaic(xImages(4))
	d := build(r, Options{Sites: map[string]SiteSetting{"x": {Forward: "always"}}})
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("总是合并转发：%v", kinds(d))
	}
	for _, n := range d.Parts[0].Nodes {
		for _, it := range n.Body {
			if it.Media.URL.Value == "https://mosaic.fxtwitter.com/jpeg/80002/a/b/c" {
				t.Fatal("合并转发里不放拼图")
			}
		}
	}
	// 从不合并转发、没有拼图：只留前 3 张，不再拆成两条（用户 2026-11-13，R-217；以前 4 张拆成 3 + 1）
	d = build(xImages(4), Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}})
	if len(d.Parts) != 1 || itemKinds(d.Parts[0].Body) != "text,image,image,image" || d.Parts[0].imageTotal != 4 {
		t.Fatalf("从不：%v %s", kinds(d), itemKinds(d.Parts[0].Body))
	}
	// 从不合并转发、有拼图：内置默认照样一张张发（同样只留前 3 张）；写了 $拼图 的模板照拼图排版
	d = build(r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}})
	if len(d.Parts) != 1 || itemKinds(d.Parts[0].Body) != "text,image,image,image" || d.Parts[0].imageTotal != 4 {
		t.Fatalf("从不（有拼图、内置默认）：%v %s", kinds(d), itemKinds(d.Parts[0].Body))
	}
	d = build(r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}, Templates: map[string]string{"x": xMosaicTpl}})
	if len(d.Parts) != 1 || itemKinds(d.Parts[0].Body) != "image,text" {
		t.Fatalf("从不（有拼图）：%v", kinds(d))
	}
	// 手写模板照写的排：用 $正文 的老写法仍然是文字图片按原顺序，不被新排版改动
	d = build(xImages(2), Options{Templates: map[string]string{"x": "$作者[\n$发布时间]$正文$链接"}})
	if got := itemKinds(d.Parts[0].Body); got != "text,text,image,image,text" {
		t.Fatalf("手写模板：%s", got)
	}
}

// 回归：转发数以前写错了统计键（模板读 retweets，取数端存的是 reposts），真实帖子的“转推”永远不显示。
func TestXRepostsFieldReadsRealStatKey(t *testing.T) {
	r := xImages(0)
	if got := build(r, Options{}).Parts[0].Body[0].Text; strings.Contains(got, "转推") {
		t.Fatalf("默认模板不写转发数：\n%s", got)
	}
	// 显式写上 $统计 / $转发 才有；键是取数端存的 reposts
	if got := build(r, Options{Templates: map[string]string{"x": "$作者[\n转推 $转发]\n$链接"}}).Parts[0].Body[0].Text; !strings.Contains(got, "转推 140") {
		t.Fatalf("\n%s", got)
	}
	if s := xStats(&r, textSep); !strings.Contains(s, "转发 140") || !strings.Contains(s, "浏览 36.6万") {
		t.Fatalf("统计行：%s", s)
	}
}

// 视频直发时仍是独立一条消息，文字在前（QQ 的视频消息不能带文字）。
func TestXVideoStaysStandalone(t *testing.T) {
	r := xImages(0)
	v := goldenVideo()
	r.Media = av([]linkreader.Media{v})
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "video", Media: &r.Media.Value[0]})
	d := build(r, Options{})
	if len(d.Parts) != 1 || itemKinds(d.Parts[0].Body) != "text,video" || !d.Parts[0].Body[1].Standalone || d.Parts[0].Body[0].Text != xChosenText {
		t.Fatalf("%v %+v", kinds(d), d.Parts[0].Body)
	}
}

// 动图在 QQ 里是图，直发时和文字同一条，不像视频那样单独一条（用户 2026-11-13）：X 和照推文排的帖子类站点都一样。
func TestGIFInlineWithText(t *testing.T) {
	gif := linkreader.Media{Type: types.BodyGIF, URL: av("https://v.example.invalid/loop.mp4"), Thumbnail: av("https://i0.example.invalid/loop.png")}
	r := xImages(0)
	r.Media = av([]linkreader.Media{gif})
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "gif", Media: &r.Media.Value[0]})
	d := build(r, Options{})
	if len(d.Parts) != 1 || itemKinds(d.Parts[0].Body) != "text,gif" || d.Parts[0].Body[1].Standalone {
		t.Fatalf("X：%v %+v", kinds(d), d.Parts[0].Body)
	}
	p := goldenBase("bluesky", "post", "90002")
	p.Author.Name, p.Author.Account = av("纸云"), av("paper.example")
	p.Text = av("今天的纸船")
	p.Media = av([]linkreader.Media{gif})
	p.Blocks = []linkreader.Block{{Type: "text", Text: "今天的纸船"}, {Type: "gif", Media: &p.Media.Value[0]}}
	d = build(p, Options{})
	if len(d.Parts) != 1 || !strings.HasSuffix(itemKinds(d.Parts[0].Body), "gif") || d.Parts[0].Body[len(d.Parts[0].Body)-1].Standalone {
		t.Fatalf("Bluesky：%v %+v", kinds(d), d.Parts[0].Body)
	}
}

// Mastodon 之类的帖子，正文开头只是一条链接时，卡片标题不写网址。
func TestCardTitleNotBareURL(t *testing.T) {
	r := goldenBase("mastodon", "status", "90001")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper")
	r.Text = av("https://example.invalid/watch?v=1")
	r.LinkCard = av(linkreader.LinkPreview{Title: av("纸船试航视频")})
	d := build(r, Options{Templates: map[string]string{"misskey": "@卡片"}})
	if c := d.Parts[0].Body[0].Card; c == nil || c.Title != "纸船试航视频" {
		t.Fatalf("%+v", d.Parts[0].Body[0])
	}
	r.LinkCard = linkreader.Field[linkreader.LinkPreview]{State: "not_returned"}
	d = build(r, Options{Templates: map[string]string{"misskey": "@卡片"}})
	if c := d.Parts[0].Body[0].Card; c == nil || c.Title != "纸云 @paper" {
		t.Fatalf("没有外链卡用作者：%+v", d.Parts[0].Body[0])
	}
	r.Text = av("正文开头 https://example.invalid/x 后面还有")
	d = build(r, Options{Templates: map[string]string{"misskey": "@卡片"}})
	if c := d.Parts[0].Body[0].Card; c == nil || c.Title != "正文开头 https://example.invalid/x 后面还有" {
		t.Fatalf("有别的字的正文照旧：%+v", d.Parts[0].Body[0])
	}
}
