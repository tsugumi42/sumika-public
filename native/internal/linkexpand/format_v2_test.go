package linkexpand

import (
	"slices"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 规格-链接发送改版 B 节（文字排版）的验收测试。

// textOf 取直发第一条消息里的文字（图、视频不算）。
func textOf(t *testing.T, d Draft) string {
	t.Helper()
	if len(d.Parts) == 0 {
		t.Fatal("没有消息")
	}
	for _, it := range d.Parts[0].Body {
		if it.Kind == "text" {
			return it.Text
		}
	}
	t.Fatalf("第一条消息没有文字：%+v", d.Parts[0].Body)
	return ""
}

// B8：视频文字版。B 站、YouTube 各一份逐字对上；缺字段整行省掉；链接在投稿时间下面（用户 2026-11-12 补的那句）。
func TestVideoTextLayout(t *testing.T) {
	this := nowFunc().In(displayZone).Year()
	when := time.Date(this, 11, 7, 15, 28, 0, 0, time.UTC) // 北京时间 23:28
	bili := goldenBili(1)
	bili.CleanURL = "https://www.bilibili.com/video/BV1xx411c7mD"
	bili.Published = av(when)
	bili = withStats(bili, map[string]int64{"view": 108_000_000, "like": 2_975_000})
	bili.Text = av("这是编的简介")
	got := textOf(t, build(bili, Options{}))
	want := "纸船试航\n纸灯实验室 | 02:15\n播放 1.08亿 | 点赞 297.5万\n11-07 23:28\nhttps://www.bilibili.com/video/BV1xx411c7mD"
	if got != want {
		t.Fatalf("B 站：\n%q\n要\n%q", got, want)
	}
	lines := strings.Split(got, "\n")
	if i := slices.Index(lines, "11-07 23:28"); i < 0 || lines[i+1] != bili.CleanURL {
		t.Fatalf("链接要紧跟在投稿时间下面：%q", lines)
	}

	// UP主缺：那一行只剩时长；统计缺：整行省掉；时间缺：链接接在上一行下面
	sparse := bili
	sparse.Author.Name = nr[string]()
	sparse.Stats = map[string]linkreader.Field[int64]{}
	sparse.Published = nr[time.Time]()
	if got := textOf(t, build(sparse, Options{})); got != "纸船试航\n02:15\nhttps://www.bilibili.com/video/BV1xx411c7mD" {
		t.Fatalf("B 站缺字段：%q", got)
	}
	// 只有点赞：第四行只写点赞，不留孤立的竖线
	sparse.Stats = map[string]linkreader.Field[int64]{"like": av(int64(52))}
	if got := textOf(t, build(sparse, Options{})); !strings.Contains(got, "\n点赞 52\n") {
		t.Fatalf("只有点赞：%q", got)
	}

	yt := goldenBase("youtube", "video", "abc123")
	yt.CleanURL = "https://www.youtube.com/watch?v=abc123"
	yt.Title = av("Paper Boat Test")
	yt.Author.Name = av("Paper Lab")
	yt.Published = av(when)
	yt.Media = av([]linkreader.Media{goldenImage(1), goldenVideo()})
	yt = withStats(yt, map[string]int64{"views": 123_456, "likes": 50, "comments": 7})
	got = textOf(t, build(yt, Options{}))
	want = "Paper Boat Test\nPaper Lab | 02:15\n播放 12.3万 | 点赞 50\n11-07 23:28\nhttps://www.youtube.com/watch?v=abc123"
	if got != want {
		t.Fatalf("YouTube：\n%q\n要\n%q", got, want)
	}
	yt.Author.Name = nr[string]()
	yt.Media = av([]linkreader.Media{goldenImage(1)})
	yt.Stats = map[string]linkreader.Field[int64]{}
	if got = textOf(t, build(yt, Options{})); got != "Paper Boat Test\n11-07 23:28\nhttps://www.youtube.com/watch?v=abc123" {
		t.Fatalf("YouTube 缺字段：%q", got)
	}
}

// B2：推文文字版。链接、“名字[@ID]：”、「正文」、发帖时间（用户 2026-11-12 加的，在正文块下面、图片上面）、图的顺序逐字对上；
// 带译文的块逐字对上；纯图推文没有空「」；被引推文自己一个节点。
func TestTweetTextLayout(t *testing.T) {
	const head = "https://example.invalid/x/80002\n纸云[@paper_cloud]："
	const when = "\n11-07 23:28"
	d := build(xImages(1), Options{})
	body := d.Parts[0].Body
	if len(d.Parts) != 1 || itemKinds(body) != "text,image" || body[0].Text != head+"\n「第一行\n第二行」"+when {
		t.Fatalf("一张图：%s %q", itemKinds(body), body[0].Text)
	}
	if got := textOf(t, build(xImages(1), Options{Translation: "第一行译文"})); got != head+"\n「第一行译文」\n（翻译自中文）"+when { // 只放译文，原文不出现（B13）
		t.Fatalf("带译文：%q", got)
	}
	if strings.Count("——————", "—") != 6 {
		t.Fatal("分隔线应是 6 个 U+2014")
	}
	pure := xImages(2)
	pure.Text, pure.Blocks = av(""), pure.Blocks[1:]
	// 没有正文：作者行不挂冒号，直接接时间（用户 2026-11-13，#32）
	if got := textOf(t, build(pure, Options{})); got != strings.TrimSuffix(head, "：")+when || strings.Contains(got, "「") {
		t.Fatalf("纯图推文：%q", got)
	}
	// 账号带 @ 的线路：不出现 [@@…]；$账号 的值不带 @
	at := xImages(0)
	at.Author.Account = av("@paper_cloud")
	if got := textOf(t, build(at, Options{})); !strings.HasPrefix(got, head+"\n") {
		t.Fatalf("账号带 @：%q", got)
	}
	if got := textOf(t, build(at, Options{Templates: map[string]string{"x": "$账号"}})); got != "paper_cloud" {
		t.Fatalf("$账号：%q", got)
	}
	// 名字缺：只写 [@ID]：
	noName := xImages(0)
	noName.Author.Name = nr[string]()
	if got := textOf(t, build(noName, Options{})); !strings.HasPrefix(got, "https://example.invalid/x/80002\n[@paper_cloud]：\n") {
		t.Fatalf("名字缺：%q", got)
	}
	// 名字和账号都缺：整行省掉，不剩一个“：”。放回旧模板就红：第二行是“：”
	anon := xImages(0)
	anon.Author.Name, anon.Author.Account = nr[string](), nr[string]()
	if got := textOf(t, build(anon, Options{})); !strings.HasPrefix(got, "https://example.invalid/x/80002\n「") {
		t.Fatalf("作者全缺：%q", got)
	}
	// 没有发帖时间就不写那一行
	noWhen := xImages(1)
	noWhen.Published = nr[time.Time]()
	if got := textOf(t, build(noWhen, Options{})); got != head+"\n「第一行\n第二行」" {
		t.Fatalf("没有时间：%q", got)
	}
	// 默认不写统计、平台名
	for _, bad := range []string{"点赞", "浏览", "X 推文", "推特"} {
		if strings.Contains(body[0].Text, bad) {
			t.Fatalf("默认不该有 %q：%q", bad, body[0].Text)
		}
	}

	// 有引用：进合并转发；主推文的字一个节点、图各一个节点，被引推文的字和它的图同一个节点（同样的格式，用户 2026-11-14）
	q := goldenBase("x", "post", "90009")
	q.CleanURL = "https://x.com/i/status/90009"
	q.Author.Name, q.Author.Account = av("被引的人"), av("quoted_one")
	q.Text = av("被引的正文")
	q.Blocks = []linkreader.Block{{Type: "text", Text: "被引的正文"}}
	q = withMedia(q, goldenImage(9))
	q.Blocks = append([]linkreader.Block{{Type: "text", Text: "被引的正文"}}, q.Blocks[1:]...)
	main := xImages(1)
	main.Quote = av(&q)
	main.Blocks = append(main.Blocks, linkreader.Block{Type: "quote", Quote: &q})
	d = build(main, Options{})
	var fwd *Part
	for i := range d.Parts {
		if d.Parts[i].Kind == "forward" {
			fwd = &d.Parts[i]
		}
	}
	if fwd == nil {
		t.Fatalf("有引用应进合并转发：%v", kinds(d))
	}
	var nodes []string
	for _, n := range fwd.Nodes {
		nodes = append(nodes, n.Body[0].Kind+":"+n.Body[0].Text+n.Body[0].Media.URL.Value)
	}
	want := []string{ // 被引推文的节点在前面（用户 2026-11-12），主推文头部标着它
		"text:https://x.com/quoted_one/status/90009\n被引的人[@quoted_one]：\n「被引的正文」\n" + shortStamp(q.Published).text, // 被引推文同样带时间（今年不写年份）；链接按作者账号写，不照抄 /i/status/
		"text:" + head + "\n「第一行\n第二行」" + when,
		"image:https://i0.example.invalid/pic-1.png",
	}
	if !slices.Equal(nodes, want) {
		t.Fatalf("合并转发节点：\n%q\n要\n%q", nodes, want)
	}
	if got := itemKinds(fwd.Nodes[0].Body); got != "text,image" {
		t.Fatalf("被引推文的字和图在同一个节点：%s", got)
	}
	if fwd.Nodes[1].QuoteOf != 1 || fwd.Nodes[0].QuoteOf != 0 {
		t.Fatalf("主推文头部应指向第 1 个节点：%+v", fwd.Nodes)
	}
}

// B6：去推文结尾的 hashtag 块。结尾块去掉、中间保留、全是 hashtag 不去、去两遍 = 去一遍、关掉开关原样；
// 发出去的正文和送去翻译的原文都是去过的（同一个入口），autoForward 的字数按去过的算。
func TestStripTrailingHashtags(t *testing.T) {
	for in, want := range map[string]string{
		"Hello world #a #b":           "Hello world",
		"Hello #mid world #a\n#b  \n": "Hello #mid world",
		"开头\n\n#tag1 ＃全角":             "开头",
		"#a #b":                       "#a #b",
		"#only":                       "#only",
		"no tags here":                "no tags here",
		"text#attached":               "text#attached",
		"text # #":                    "text # #",
		"":                            "",
		"a #b c":                      "a #b c",
		"排行 #1":                       "排行 #1",
		"正文 #1 #tag":                  "正文 #1",
		"正文 #１２":                      "正文 #１２",
		"正文 #1st":                     "正文",
	} {
		got := stripTrailingHashtags(in)
		if got != want {
			t.Errorf("%q → %q，要 %q", in, got, want)
		}
		if again := stripTrailingHashtags(got); again != got {
			t.Errorf("去两遍和去一遍不一样：%q → %q → %q", in, got, again)
		}
	}

	r := xPost("Hello rockets #space #moon")
	o := Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}}
	if got := textOf(t, Build(&r, "", o, 0)); !strings.Contains(got, "「Hello rockets」") {
		t.Fatalf("发出去的正文：%q", got)
	}
	if got := TranslationSource(&r, o); got != "Hello rockets" {
		t.Fatalf("送去翻译的原文：%q", got)
	}
	if r.Text.Value != "Hello rockets #space #moon" || r.Blocks[0].Text != "Hello rockets #space #moon" {
		t.Fatal("不该改调用方传进来的结果")
	}
	keep := o
	keep.KeepTrailingHashtags = true
	if got := textOf(t, Build(&r, "", keep, 0)); !strings.Contains(got, "「Hello rockets #space #moon」") {
		t.Fatalf("关掉开关原样：%q", got)
	}
	if got := TranslationSource(&r, keep); got != "Hello rockets #space #moon" {
		t.Fatalf("关掉开关送去翻译的原文：%q", got)
	}
	// 去 hashtag 后不超过字节门槛：直发；不去时超过，转发（ADR-391：按实际发出的文字字节算）
	body := strings.Repeat("字", 290)
	long := xPost(body + " #一号标签 #二号标签 #三号标签")
	long.Media = av([]linkreader.Media{})
	capOpt := Options{Templates: map[string]string{"x": "$正文文字"}, Sites: map[string]SiteSetting{"x": {ForwardOverBytes: intp(len(body))}}}
	if d := Build(&long, "", capOpt, 0); len(d.Parts) != 1 || d.Parts[0].Kind != "message" {
		t.Fatalf("字数按去过 hashtag 的算：%v", kinds(d))
	}
	keepBytes := capOpt
	keepBytes.KeepTrailingHashtags = true
	if d := Build(&long, "", keepBytes, 0); d.Parts[0].Kind != "forward" {
		t.Fatalf("不去 hashtag 时超过字节门槛应进合并转发：%v", kinds(d))
	}
}

// B1 的真结果那一半：X、B 站视频按内置默认排出来的每段文字都没有空行（样张那一半在 linktemplate 的 TestTemplatesHaveNoBlankLines）。
func TestBuiltOutputHasNoBlankLines(t *testing.T) {
	for name, r := range map[string]linkreader.Result{"x": xImages(2), "bili": goldenBili(3)} {
		for _, p := range build(r, Options{}).Parts {
			for _, it := range append(p.Body, func() []Item {
				var all []Item
				for _, n := range p.Nodes {
					all = append(all, n.Body...)
				}
				return all
			}()...) {
				if strings.Contains(it.Text, "\n\n") {
					t.Errorf("%s 的输出有空行：%q", name, it.Text)
				}
			}
		}
	}
}

// B7：pixiv 首行四种组合（AI、R18、都有、都没有）逐字对上；直发是首行、作品链接（用户 2026-11-12 加的）、图，转发第一个节点是首行加干净链接。
func TestPixivFirstLine(t *testing.T) {
	art := func(ai bool, restrict int64) linkreader.Result {
		r := goldenBase("pixiv", "artwork", "90001")
		r.CleanURL = "https://www.pixiv.net/artworks/90001"
		r.Title, r.Author.Name = av("纸船试航"), av("纸云")
		r.Flags["ai_generated"] = linkreader.Field[any]{State: "available", Value: ai}
		r.Flags["x_restrict"] = linkreader.Field[any]{State: "available", Value: restrict}
		r = withMedia(r, goldenImage(1))
		return r
	}
	for _, c := range []struct {
		ai       bool
		restrict int64
		want     string
	}{
		{true, 0, "[AI] | 纸船试航 | 纸云"},
		{false, 1, "🔞 | 纸船试航 | 纸云"},
		{true, 1, "[AI]🔞 | 纸船试航 | 纸云"},
		{false, 0, "纸船试航 | 纸云"},
		{false, 2, "🔞 | 纸船试航 | 纸云"}, // R18-G 也是 🔞
	} {
		d := build(art(c.ai, c.restrict), Options{})
		body := d.Parts[0].Body
		if len(d.Parts) != 1 || itemKinds(body) != "text,image" || body[0].Text != c.want+"\nhttps://www.pixiv.net/artworks/90001" {
			t.Errorf("AI %v 分级 %d：%s %q，要 %q", c.ai, c.restrict, itemKinds(body), body[0].Text, c.want)
		}
	}
	// 标记没取到（旧缓存）：不写标记；作者缺：只有标题
	r := art(false, 0)
	r.Flags = map[string]linkreader.Field[any]{}
	r.Author.Name = nr[string]()
	if got := textOf(t, build(r, Options{})); got != "纸船试航\nhttps://www.pixiv.net/artworks/90001" {
		t.Fatalf("缺作者：%q", got)
	}
	// 首行全缺（标题、作者、标记都没取到，只有说明文字，比如旧缓存）：直发只有链接和图、转发第一个节点只有链接，不写“未返回”。
	// 标题和说明都缺时整条按没内容处理（NoContent），走不到模板。放回旧模板就红
	r = art(false, 0)
	r.Flags = map[string]linkreader.Field[any]{}
	r.Title, r.Author.Name, r.Text = nr[string](), nr[string](), av("编的作品说明")
	if d := build(r, Options{}); len(d.Parts) != 1 || itemKinds(d.Parts[0].Body) != "text,image" || d.Parts[0].Body[0].Text != "https://www.pixiv.net/artworks/90001" {
		t.Fatalf("首行全缺直发应当只剩链接和图：%+v", d.Parts)
	}
	d0 := build(r, Options{Sites: map[string]SiteSetting{"pixiv-artwork": {Forward: "always"}}})
	if f := d0.Parts[len(d0.Parts)-1]; f.Kind != "forward" || f.Nodes[0].Body[0].Text != "https://www.pixiv.net/artworks/90001" {
		t.Fatalf("首行全缺转发：%+v", d0.Parts)
	}
	// 合并转发：第一个节点是首行加干净链接，后面一图一节点
	r = art(true, 1)
	d := build(r, Options{Sites: map[string]SiteSetting{"pixiv-artwork": {Forward: "always"}}})
	fwd := d.Parts[len(d.Parts)-1]
	if fwd.Kind != "forward" || fwd.Nodes[0].Body[0].Text != "[AI]🔞 | 纸船试航 | 纸云\nhttps://www.pixiv.net/artworks/90001" || fwd.Nodes[1].Body[0].Kind != "image" {
		t.Fatalf("转发：%+v", fwd.Nodes)
	}
}

// B3：直接发的图数上限。默认 3 张直发同一条（图在文字下面）、4 张走合并转发；上限改成 2 时 3 张走合并转发。
// 写了 $拼图 的自定义模板仍按拼图算（拼图只算一张）。pixiv 作品、Telegram 用同一个上限。
func TestDirectImageMax(t *testing.T) {
	direct := func(d Draft) bool { return len(d.Parts) == 1 && d.Parts[0].Kind == "message" }
	if d := build(xImages(3), Options{}); !direct(d) || itemKinds(d.Parts[0].Body) != "text,image,image,image" {
		t.Fatalf("X 3 张：%v %s", kinds(d), itemKinds(d.Parts[0].Body))
	}
	if d := build(withMosaic(xImages(3)), Options{}); !direct(d) || itemKinds(d.Parts[0].Body) != "text,image,image,image" {
		t.Fatalf("X 3 张有拼图字段，内置默认也不用拼图：%v", kinds(d))
	}
	if d := build(xImages(4), Options{}); direct(d) {
		t.Fatalf("X 4 张应走合并转发：%v", kinds(d))
	}
	if d := build(xImages(3), Options{DirectImageMax: 2}); direct(d) {
		t.Fatalf("上限 2 时 X 3 张应走合并转发：%v", kinds(d))
	}
	if d := build(xImages(2), Options{DirectImageMax: 2}); !direct(d) {
		t.Fatalf("上限 2 时 X 2 张直发：%v", kinds(d))
	}
	if d := build(xImages(5), Options{DirectImageMax: 5}); !direct(d) {
		t.Fatalf("上限调大到 5：%v", kinds(d))
	}
	if d := build(withMosaic(xImages(4)), Options{Templates: map[string]string{"x": xMosaicTpl}}); !direct(d) {
		t.Fatalf("写了 $拼图 的模板，4 张拼成一张直发：%v", kinds(d))
	}

	pixiv := func(n int) linkreader.Result {
		r := goldenBase("pixiv", "artwork", "90001")
		r.Title = av("纸船试航")
		var ms []linkreader.Media
		for i := 1; i <= n; i++ {
			ms = append(ms, goldenImage(i))
		}
		r = withMedia(r, ms...)
		r.Stats["pages"] = av(int64(n))
		return r
	}
	if d := build(pixiv(3), Options{}); !direct(d) {
		t.Fatalf("pixiv 3 张直发：%v", kinds(d))
	}
	if d := build(pixiv(4), Options{}); direct(d) {
		t.Fatalf("pixiv 4 张走合并转发：%v", kinds(d))
	}
	if d := build(pixiv(3), Options{DirectImageMax: 2}); direct(d) {
		t.Fatalf("上限 2 时 pixiv 3 张走合并转发：%v", kinds(d))
	}

	tg := func(n int) linkreader.Result {
		r := goldenBase("telegram", "post", "paper/1")
		r.Text = av("频道帖子")
		var ms []linkreader.Media
		for i := 1; i <= n; i++ {
			ms = append(ms, goldenImage(i))
		}
		return withMedia(r, ms...)
	}
	if !Supports("telegram", "post") {
		t.Skip("telegram/post 不在 Supports 里")
	}
	if d := build(tg(3), Options{}); !direct(d) {
		t.Fatalf("Telegram 3 张直发：%v", kinds(d))
	}
	if d := build(tg(4), Options{}); direct(d) {
		t.Fatalf("Telegram 4 张走合并转发：%v", kinds(d))
	}
}

// B4：所有站点的内置转发模板渲染后只有一个合并转发段，前面没有前导文字段；“发过 N 次”接在第一个节点的文字后面。
// 用户自定义模板在 $合并转发 前写了字，照旧单独发一条（那是用户的选择）。
func TestForwardHasNoLeadingMessage(t *testing.T) {
	for _, s := range linktemplate.Sites() {
		if !s.HasForward() {
			continue
		}
		src := s.Source(true)
		if i := strings.Index(src, "$合并转发"); i != 0 {
			t.Errorf("%s 的内置转发模板在 $合并转发 前还有字：%q", s.Key, src[:max(i, 0)])
		}
	}
	if !strings.HasPrefix(linktemplate.XArticleForward, "$合并转发") {
		t.Errorf("X 文章的内置转发模板有前导：%q", linktemplate.XArticleForward)
	}
	always := func(key string) Options {
		return Options{Sites: map[string]SiteSetting{key: {Forward: "always"}}}
	}
	cases := map[string]linkreader.Result{"x": xImages(2), "bilibili-dynamic": dynamicWith("一句话", 1), "bilibili-article": articleResult()}
	for key, r := range cases {
		d := Build(&r, "-71", always(key), 2)
		if len(d.Parts) != 1 || d.Parts[0].Kind != "forward" {
			t.Fatalf("%s：应只有一个合并转发段：%v", key, kinds(d))
		}
		first := d.Parts[0].Nodes[0].Body
		if last := first[len(first)-1]; last.Kind != "text" || !strings.HasSuffix(last.Text, "\n发过 2 次") {
			t.Fatalf("%s：“发过 N 次”应接在第一个节点后面：%+v", key, first)
		}
	}
	// 自定义转发模板在 $合并转发 前写了字：照旧发成一条
	r := xImages(2)
	o := always("x")
	o.Templates = map[string]string{"x-forward": "看合并转发$合并转发$链接$节点$图片"}
	if got := strings.Join(kinds(Build(&r, "", o, 0)), ","); got != "message,forward" {
		t.Fatalf("自定义前导：%s", got)
	}
}

// B5：非推文站点接了翻译后，有译文时只出译文、不带原文；推文仍是原文加译文。假装登记一个非推文站点来测。
func TestNonTweetTranslationOnly(t *testing.T) {
	translatableSites["fake-blog"] = true
	t.Cleanup(func() { delete(translatableSites, "fake-blog") })
	site := linktemplate.Site{Key: "fake-blog", Label: "编的博客", Set: linktemplate.NewSet(
		linktemplate.Field{ID: "url", Names: []string{"链接"}, Kind: linktemplate.KindText},
		linktemplate.Field{ID: "text", Names: []string{"简介"}, Kind: linktemplate.KindText},
		linktemplate.Field{ID: "translation", Names: []string{"译文"}, Kind: linktemplate.KindText},
	)}
	r := goldenBase("blog", "post", "1")
	r.Text = av("Hello rockets")
	render := func(o Options) string {
		tpl, err := linktemplate.Parse("[$简介\n][$译文\n]$链接", site.Set)
		if err != nil {
			t.Fatal(err)
		}
		b := &builder{r: &r, o: o, site: site}
		return textOf(t, Draft{Parts: b.assemble(tpl.Render(b), "")})
	}
	if got := render(Options{Translation: "你好，火箭"}); got != "你好，火箭\nhttps://example.invalid/blog/1" {
		t.Fatalf("非推文有译文时只出译文：%q", got)
	}
	if got := render(Options{}); got != "Hello rockets\nhttps://example.invalid/blog/1" {
		t.Fatalf("没有译文时照旧写原文：%q", got)
	}
	// 模板没把 $简介 套进可选段：有译文时它输出空串，不写“未返回”。放回旧代码（返回没取到）就红
	bare := func(o Options) string {
		tpl, err := linktemplate.Parse("$简介$译文", site.Set)
		if err != nil {
			t.Fatal(err)
		}
		b := &builder{r: &r, o: o, site: site}
		return textOf(t, Draft{Parts: b.assemble(tpl.Render(b), "")})
	}
	if got := bare(Options{Translation: "你好，火箭"}); got != "你好，火箭" {
		t.Fatalf("裸写的 $简介 有译文时应当是空串：%q", got)
	}
	if translationOnly("x") || !translationOnly("fake-blog") || translationOnly("telegram") {
		t.Fatal("translationOnly 的判断不对")
	}
	if got := textOf(t, build(xImages(0), Options{Translation: "译文"})); !strings.Contains(got, "「译文」\n（翻译自中文）") || strings.Contains(got, "第一行") {
		t.Fatalf("推文只放译文：%q", got)
	}
}

// 超长推文进转发：“发过 N 次”“还有 N 条没处理”接在主推文正文（拆成几个节点）的末尾，不落进正文中间，
// 每个节点不超过节点的字节上限，被引推文的 QuoteOf 不变。bug：提示接在第一个节点后面，落在正文中间、那个节点超过上限
func TestLongTweetNoteAfterBody(t *testing.T) {
	r := caseNamed(t, "x-quote")
	long := strings.Repeat("甲", 5600) // 16800 字节，要拆成两个 node
	r.Text = av(long)
	r.Blocks[0].Text = long
	plain := Build(&r, "-71", Options{ForwardVideo: true}, 0)
	d := Build(&r, "-71", Options{ForwardVideo: true}, 1)
	d.AppendNote("还有 1 条没处理")
	p := d.Parts[len(d.Parts)-1]
	if p.Kind != types.PartForward || len(p.Nodes) != len(plain.Parts[len(plain.Parts)-1].Nodes) {
		t.Fatalf("提示接进已有节点，不多出节点：%+v", p)
	}
	end := -1
	for i, n := range p.Nodes {
		for _, it := range n.Body {
			if it.Kind != types.BodyText {
				continue
			}
			if c := len(it.Text); c > linktemplate.DefaultForwardOverBytes {
				t.Fatalf("第 %d 个节点 %d 字节，超过 %d", i, c, linktemplate.DefaultForwardOverBytes)
			}
			if strings.Contains(it.Text, "甲\n发过") {
				t.Fatalf("提示落进正文中间：第 %d 个节点", i)
			}
			if strings.Contains(it.Text, "甲」") && end < 0 {
				end = i
				if !strings.HasSuffix(it.Text, "\n发过 1 次\n还有 1 条没处理") {
					t.Fatalf("正文末尾的节点要接上提示：%q", it.Text[max(0, len(it.Text)-120):])
				}
			}
		}
	}
	if end <= 0 {
		t.Fatalf("正文应拆成几个节点、末尾在第 %d 个", end)
	}
	quoteOf := func(nodes []Node) (at, q int) {
		for i, n := range nodes {
			if n.QuoteOf > 0 {
				return i, n.QuoteOf
			}
		}
		return -1, 0
	}
	at, q := quoteOf(p.Nodes)
	pat, pq := quoteOf(plain.Parts[len(plain.Parts)-1].Nodes)
	if q != 1 || at != pat || q != pq {
		t.Fatalf("被引推文在最前面、主推文的 QuoteOf 不变：第 %d 个节点 %d（原 %d、%d）", at, q, pat, pq)
	}
}

// 提示接上会超过节点的字节上限：在头部末尾的节点后面另起一个节点，后面节点的 QuoteOf 跟着挪，下一句接在新节点后面
func TestForwardNoteOverflowNewNode(t *testing.T) {
	full := strings.Repeat("乙", 1999) // 5997 字节
	d := Draft{Parts: []Part{{Kind: types.PartForward, Nodes: []Node{
		{Name: NodeName, Body: []Item{text("头")}, QuoteOf: 4},
		{Name: NodeName, Body: []Item{text(full)}},
		{Name: NodeName, Body: []Item{{Kind: types.BodyImage}}},
		{Name: NodeName, Body: []Item{text("被引")}},
	}}}, nodeBytes: 6000}
	markNoteEnd(d.Parts[0].Nodes[:2])
	d.AppendNote("发过 1 次")
	d.AppendNote("还有 1 条没处理")
	n := d.Parts[0].Nodes
	if len(n) != 5 || n[1].Body[0].Text != full || n[2].Body[0].Text != "发过 1 次\n还有 1 条没处理" || n[4].Body[0].Text != "被引" {
		t.Fatalf("另起一个节点放提示：%d 个节点", len(n))
	}
	if n[0].QuoteOf != 5 {
		t.Fatalf("QuoteOf 跟着挪到 5：%d", n[0].QuoteOf)
	}
}
