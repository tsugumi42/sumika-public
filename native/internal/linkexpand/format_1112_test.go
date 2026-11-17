package linkexpand

import (
	"context"
	"errors"
	"image/color"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 2026-11-12 样张复核（data/dev/link-gallery-20261112/issues.md）里排版这一路的回归测试。名字、链接都是编的。

// #10 #72 #63：作者正文里连续多个空行压成一个，行尾空格和 \r 去掉，首尾空行去掉；清两遍 = 清一遍。
func TestTidyText(t *testing.T) {
	cases := map[string]string{
		"无偿分享\n\n\n实时更新\n\n\n点击":   "无偿分享\n\n实时更新\n\n点击",
		"甲。 \n乙。\t\n":              "甲。\n乙。",
		"一段。\r\n\r\n二段。\r三段。":      "一段。\n\n二段。\n三段。",
		"\n\n  缩进保留\n\n\n\n":       "  缩进保留",
		"单行":                       "单行",
		"":                         "",
		"甲\n \n　\n乙":               "甲\n\n乙",
		"#作品\n\n标题 - 20\n\nWebRip": "#作品\n\n标题 - 20\n\nWebRip",
	}
	for in, want := range cases {
		got := tidyText(in)
		if got != want {
			t.Errorf("%q：得到 %q，应为 %q", in, got, want)
		}
		if again := tidyText(got); again != got {
			t.Errorf("清两遍和清一遍不一样：%q → %q", got, again)
		}
	}
}

// 正文清理在取数结果上做：发出去的、送去翻译的是同一份；模板里写的空行（自定义模板）不动。
func TestCleanResultBeforeTemplate(t *testing.T) {
	r := siteResult("mastodon", "status", "mastodon.social/90060")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper")
	body := "第一段\n\n\n\n第二段  \n#纸船 #折纸"
	r.Text = av(body)
	r.Blocks = []linkreader.Block{{Type: "text", Text: body}}
	got := describe(t, r)
	if !strings.Contains(got, "第一段\n\n第二段」") || strings.Contains(got, "#纸船") {
		t.Fatalf("正文没清理：\n%s", got)
	}
	// 关掉去 hashtag：标签留着，空行照样压
	d := Build(&r, "", Options{KeepTrailingHashtags: true}, 0)
	if s := strings.Join(d.Describe(), "\n"); !strings.Contains(s, "第二段\n#纸船 #折纸") || strings.Contains(s, "\n\n\n") {
		t.Fatalf("关掉开关：\n%s", s)
	}
	// 自定义模板里自己写的空行不动
	d = Build(&r, "", Options{Templates: map[string]string{"misskey": "$作者\n\n\n$链接"}}, 0)
	if s := strings.Join(d.Describe(), "\n"); !strings.Contains(s, "纸云 @paper\n\n\nhttps://") {
		t.Fatalf("自定义模板的空行被改了：\n%q", s)
	}
	// 原结果不被改
	if r.Text.Value != body || r.Blocks[0].Text != body {
		t.Fatal("清理改了原结果")
	}
}

// #6：非推文平台结尾的 hashtag 也去（同 stripTrailingHashtags 开关）；送去翻译的也去过。
func TestNonTweetTrailingHashtags(t *testing.T) {
	r := tgResult("纸船下水了\n#纸船 #折纸 #纸船", 0)
	got := describe(t, r)
	if strings.Contains(got, "#纸船") || !strings.Contains(got, "纸船下水了") {
		t.Fatalf("\n%s", got)
	}
	// 只有标签时不去（不然正文就空了）
	r = tgResult("#纸船 #折纸", 0)
	if got := describe(t, r); !strings.Contains(got, "#纸船 #折纸") {
		t.Fatalf("整条只有标签不该去：\n%s", got)
	}
}

// #5：时间各平台统一成推文的写法：今年“11-01 18:30”，往年带年份，不带“+08:00”；只有日期的不补钟点。
func TestStampUnified(t *testing.T) {
	this := av(time.Date(testNow.Year(), 11, 1, 10, 30, 0, 0, time.UTC))
	old := av(time.Date(2020, 3, 4, 10, 30, 0, 0, time.UTC))
	if got := stamp(this); got != "11-01 18:30" {
		t.Fatalf("今年：%q", got)
	}
	if got := stamp(old); got != "2020-03-04 18:30" {
		t.Fatalf("往年：%q", got)
	}
	for _, c := range goldenCases() {
		r := c.build(t)
		d := Build(&r, "", goldenOptions(c.format), 0)
		if s := d.Semantic(); strings.Contains(s, "+08:00") {
			t.Errorf("%s 还带时区：%q", c.name, s)
		}
	}
	for _, c := range siteCases() {
		if strings.Contains(describe(t, c.r), "+08:00") {
			t.Errorf("%s 还带时区", c.name)
		}
	}
	// 个人主页的时间是注册日期：只写日期，标“注册”
	r := siteResult("mastodon", "account", "mastodon.social/@paper")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper")
	r.Published = av(time.Date(2016, 3, 16, 0, 0, 0, 0, time.UTC))
	if got := describe(t, r); !strings.Contains(got, "注册：2016-03-16") || strings.Contains(got, "08:00") {
		t.Fatalf("主页注册时间：\n%s", got)
	}
}

// #1：非推文平台的默认模板不写平台名（“B站直播 | ”“GitHub | ”“Steam 游戏 | ”……），$站点 字段还在。
func TestDefaultTextHasNoPlatformName(t *testing.T) {
	names := []string{"B站", "会员购 |", "GitHub", "Steam", "BOOTH", "爱发电", "Bluesky", "Telegram", "Misskey", "Mastodon", "Spotify", "TikTok 视频", "小黑盒", "网易云", "pixiv |", "抖音 |"}
	check := func(name, got string) {
		for _, n := range names {
			if strings.Contains(got, n) {
				t.Errorf("%s 带了平台名 %q：\n%s", name, n, got)
			}
		}
	}
	for _, c := range siteCases() {
		check(c.name, describe(t, c.r))
	}
	for _, c := range goldenCases() {
		if c.format == "card" {
			continue // 卡片图上的字另说（这一路只改文字版）
		}
		r := c.build(t)
		check(c.name, strings.Join(Build(&r, "", goldenOptions(c.format), 0).Describe(), "\n"))
	}
	for _, r := range biliMoreSamples() {
		check(r.Kind, describe(t, r))
	}
}

// 简介默认不写，但字段照旧取：自定义模板写 $简介 仍有内容（用户 2026-11-12）。
func TestDescFieldStillAvailable(t *testing.T) {
	cases := []struct {
		site string
		r    linkreader.Result
	}{
		{"bilibili-audio", audioResult()},
		{"bilibili-collection", collectionResult()},
		{"bilibili-course", courseResult()},
		{"bilibili-bangumi", bangumiResult()},
		{"bilibili-workshop", workshopResult()},
	}
	steam := siteResult("steam", "app", "90020")
	steam.Title, steam.Text = av("纸船物语"), av("这是编的游戏简介。")
	cases = append(cases, struct {
		site string
		r    linkreader.Result
	}{"steam", steam})
	for _, c := range cases {
		def := describe(t, c.r)
		r := c.r
		d := Build(&r, "", Options{Templates: map[string]string{c.site: "$标题\n简介：$简介\n$链接"}}, 0)
		custom := strings.Join(d.Describe(), "\n")
		if !strings.Contains(custom, "简介：") || strings.Contains(custom, "简介：未返回") {
			t.Errorf("%s：自定义模板写了 $简介 却没有：\n%s", c.site, custom)
		}
		desc := strings.TrimPrefix(custom[strings.Index(custom, "简介："):], "简介：")
		desc = strings.SplitN(desc, "\n", 2)[0]
		if strings.Contains(def, desc) {
			t.Errorf("%s：默认模板还带着简介 %q：\n%s", c.site, desc, def)
		}
	}
}

// #3：“正文未完整取得”不再接在消息后面（取数层的状态，常和“（已截取）”“请到原文继续阅读”重复）。
func TestNoIncompleteNote(t *testing.T) {
	r := goldenBase("pixiv", "novel", "90001")
	r.Title = av("纸船试航")
	r.Text = linkreader.Field[string]{State: "available", Value: strings.Repeat("甲", 50), Truncated: true}
	r.Novel = &linkreader.NovelInfo{}
	d := Build(&r, "", Options{}, 0)
	if s := strings.Join(d.Describe(), "\n"); strings.Contains(s, "未完整") || strings.Contains(s, "未返回") {
		t.Fatalf("\n%s", s)
	}
}

// #45 #46：pixiv 小说也套作品的首行（[AI]🔞 | 标题 | 作者名），下一行链接，转发的第一个节点就是它；简介取不到不写“未返回”。
func TestPixivNovelFirstLine(t *testing.T) {
	r := goldenBase("pixiv", "novel", "90001")
	r.Title, r.Author.Name = av("纸船试航"), av("纸云")
	r.Text = av("正文")
	r.Flags["ai_generated"] = av[any](true)
	r.Flags["x_restrict"] = av[any](1)
	r.Novel = &linkreader.NovelInfo{Description: linkreader.Field[string]{State: "not_returned", Reason: "interface_omitted"}}
	d := Build(&r, "", Options{}, 0)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("小说应进合并转发：%v", d.Describe())
	}
	first := d.Parts[0].Nodes[0].Body[0].Text
	if !strings.HasPrefix(first, "[AI]🔞 | 纸船试航 | 纸云\nhttps://example.invalid/pixiv/90001") {
		t.Fatalf("第一个节点：%q", first)
	}
	if s := strings.Join(d.Describe(), "\n"); strings.Contains(s, "未返回") || strings.Contains(s, "pixiv |") {
		t.Fatalf("\n%s", s)
	}
	direct := Build(&r, "", Options{Sites: map[string]SiteSetting{"pixiv-novel": {Forward: "never"}}}, 0)
	if got := textOf(t, direct); !strings.HasPrefix(got, "[AI]🔞 | 纸船试航 | 纸云\nhttps://example.invalid/pixiv/90001") {
		t.Fatalf("直发：%q", got)
	}
}

// BOOTH：图最多 3 张，价格下面列出规格（用户 2026-11-12）；只有一个规格不另列。
func TestBoothImagesAndVariations(t *testing.T) {
	r := siteResult("booth", "item", "90012")
	r.Title, r.Author.Name = av("纸船模型"), av("纸舟商店")
	r = withMedia(r, goldenImage(1), goldenImage(2), goldenImage(3), goldenImage(4), goldenImage(5))
	r.Booth = &linkreader.BoothInfo{Price: av("¥ 600~"), Variations: []linkreader.BoothVariation{
		{Name: av("电子版"), Price: av(int64(600))},
		{Name: av("实体版"), Price: av(int64(1500)), SoldOut: true},
	}}
	d := Build(&r, "", Options{}, 0)
	if len(d.Parts) != 1 {
		t.Fatalf("应是一条消息：%v", d.Describe())
	}
	images := 0
	for _, it := range d.Parts[0].Body {
		if it.Kind == types.BodyImage {
			images++
		}
	}
	if images != 3 {
		t.Fatalf("应发 3 张图，得到 %d", images)
	}
	want := "纸船模型\n店铺：纸舟商店\n价格：¥ 600~\n规格：\n电子版 ¥ 600\n实体版 ¥ 1,500（售罄）\nhttps://example.invalid/booth/90012"
	if got := textOf(t, d); got != want {
		t.Fatalf("得到：\n%s\n想要：\n%s", got, want)
	}
	r.Booth.Variations = r.Booth.Variations[:1]
	if got := textOf(t, Build(&r, "", Options{}, 0)); strings.Contains(got, "规格") {
		t.Fatalf("只有一个规格不该另列：\n%s", got)
	}
	if yen(1234567) != "1,234,567" || yen(0) != "0" || yen(999) != "999" {
		t.Fatal("千分位写错了")
	}
}

// 爱发电免费视频：有能下载的视频就发视频（单独一条）；创作者主页标题就是作者名时不再写“作者：”。
func TestAfdianVideoAndCreator(t *testing.T) {
	r := siteResult("afdian", "post", "90032")
	r.Title, r.Author.Name = av("纸船试航视频"), av("纸云")
	r = withMedia(r, goldenVideo())
	d := Build(&r, "", Options{}, 0)
	got := strings.Join(d.Describe(), "\n")
	if !strings.Contains(got, "[视频，单独一条消息]") {
		t.Fatalf("没有视频：\n%s", got)
	}
	c := siteResult("afdian", "creator", "paper")
	c.Title, c.Author.Name = av("纸云"), av("纸云")
	if got := describe(t, c); strings.Contains(got, "作者：") {
		t.Fatalf("创作者主页重复写了作者：\n%s", got)
	}
}

// #84 #51 #58 #72（#65 见 TestSteamDeveloperOnlyForApps）：小黑盒标题取不到游戏名时不写标题行（2026-11-13 起，以前写“游戏 编号”，见 newsites_test）；
// 引用头只写一行；名字本身就是账号时不重复。
func TestSmallLayoutFixes(t *testing.T) {
	q := siteResult("bluesky", "post", "90070/b")
	q.Author.Name, q.Author.Account = av("纸云"), av("paper.example.invalid")
	q.Text = av("被引的话")
	r := siteResult("bluesky", "post", "90070/a")
	r.Author.Name, r.Author.Account = av("纸灯"), av("lamp.example.invalid")
	r.Text = av("主帖")
	r.Quote = av(&q)
	got := describe(t, r)
	if !strings.Contains(got, "引用 纸云 @paper.example.invalid：\n被引的话") || strings.Contains(got, "外部内容") || strings.Count(got, "@paper.example.invalid") != 1 {
		t.Fatalf("引用头：\n%s", got)
	}
	b := siteResult("booth", "item", "90013")
	b.Title, b.Author.Name, b.Author.Account = av("纸船"), av("@paper_shop"), av("paper_shop")
	if got := genericCardOf(t, b).Author; got != "@paper_shop" {
		t.Fatalf("店铺名重复：%q", got)
	}
}

func genericCardOf(t *testing.T, r linkreader.Result) *linkcard.Data {
	t.Helper()
	key := linktemplate.SiteKey(r.Platform, r.Kind)
	d := Build(&r, "", Options{Templates: map[string]string{key: "@卡片"}}, 0)
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Card != nil {
				return it.Card
			}
		}
	}
	t.Fatalf("没有卡片：%v", d.Describe())
	return nil
}

// #11 #12：直播间人气 0 不写；“关注”和“粉丝”是同一个数，取数端只给 followers（attention 没取到），文字和卡片都只写粉丝。
func TestLiveRoomStats(t *testing.T) {
	r := siteResult("bilibili", "live", "90080")
	r.Title, r.Author.Name = av("纸船直播"), av("纸灯")
	r = withStats(r, map[string]int64{"online": 0, "followers": 3181602})
	r.Stats["attention"] = linkreader.Field[int64]{State: "not_returned", Reason: "same_as_followers"}
	if got := describe(t, r); strings.Contains(got, "人气") || strings.Contains(got, "关注") || !strings.Contains(got, "粉丝 318.2万") {
		t.Fatalf("\n%s", got)
	}
	c := genericCardOf(t, r)
	if s := strings.Join(c.Stats, ""); strings.Contains(s, "关注") || strings.Contains(s, "人气") || !strings.Contains(s, "粉丝 318.2万") {
		t.Fatalf("卡片：%v", c.Stats)
	}
	// 用户自己写的模板里还有 $关注：当粉丝的别名，照样出数，不是空的
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-live": "关注 $关注\n$链接"}}, 0)
	if got := strings.Join(d.Describe(), "\n"); !strings.Contains(got, "关注 318.2万") {
		t.Fatalf("$关注：\n%s", got)
	}
}

// #78 #77：Spotify 卡片的日期照文字版只写日期，不补“08:00”，两边不差一天。
func TestSpotifyCardDate(t *testing.T) {
	r := siteResult("spotify", "episode", "0000000000000000000009")
	r.Title, r.Author.Name = av("第九集"), av("纸舟电台")
	r.Published = av(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)) // 只有日期的数据（接口给 UTC 零点）：日期一律按北京时间取，当天 08:00 日期不变
	text := describe(t, r)
	c := genericCardOf(t, r)
	if !strings.Contains(text, "发行：2026-11-06") || c.DateDuration != "2026-11-06" {
		t.Fatalf("文字 %q，卡片 %q", text, c.DateDuration)
	}
}

// #18：B 站纯图动态的“分享图片”是 B 站自己填的占位，不当正文。
func TestDynamicSharePlaceholder(t *testing.T) {
	r := dynamicWith("分享图片", 1)
	if got := textOf(t, build(r, Options{})); strings.Contains(got, "分享图片") {
		t.Fatalf("\n%s", got)
	}
	r = dynamicWith("分享图片", 0)
	if got := textOf(t, build(r, Options{})); !strings.Contains(got, "分享图片") {
		t.Fatalf("没有图时它就是正文：\n%s", got)
	}
}

// #13：混合样式的文字部分去掉封面槽位时不留空行（会员购的“封面文字”里封面夹在两行中间）。
func TestMixedStyleNoBlankLine(t *testing.T) {
	site, _, _ := linktemplate.SiteByKey("bilibili-mall")
	src, err := site.Resolve("@混合")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src, "\n\n") {
		t.Fatalf("混合样式里有空行：%q", src)
	}
}

// #39：投票的选项也送去翻译（只送选项名，票数和截止留在原文那块）。
func TestPollChoicesTranslated(t *testing.T) {
	r := goldenBase("x", "post", "80020")
	r.Text = av("Which one?")
	r.Blocks = []linkreader.Block{{Type: "text", Text: "Which one?"}, {Type: "poll"}}
	r.Poll = &linkreader.PollInfo{Choices: []linkreader.PollChoice{{Label: "Paper boat", Count: av(int64(3))}, {Label: "Paper kite"}}, Total: av(int64(3))}
	src := TranslationSource(&r, Options{})
	if src != "Which one?\nPaper boat\nPaper kite" {
		t.Fatalf("送去翻译的：%q", src)
	}
}

// #3：我们自己的限制（大小上限、下载范围、本机工具）只进运行记录；平台那边的原因照旧写。
func TestQuietMediaFailure(t *testing.T) {
	for _, e := range []error{linkreader.ErrMediaLimit, &linkreader.FetchError{Kind: "unsafe_target"}, ErrMediaTools, ErrContainer, ErrPlayback} {
		if !quietMediaFailure(e) {
			t.Errorf("%v 应当不进群", e)
		}
	}
	for _, e := range []error{&linkreader.FetchError{Kind: "login_required", Status: 403}, ErrVideoCapability, ErrGIFCapability, errImageCapability, errors.New("编的错")} {
		if quietMediaFailure(e) {
			t.Errorf("%v 应当照旧写", e)
		}
	}
}

// quietLoader 是假的媒体加载：图片按地址给结果，视频一律超过大小上限。
type quietLoader struct {
	png  []byte
	errs map[string]error
	note string
}

func (l quietLoader) Image(_ context.Context, _ string, m linkreader.Media) (Loaded, error) {
	if e := l.errs[m.URL.Value]; e != nil {
		return Loaded{}, e
	}
	return Loaded{Kind: types.BodyImage, Resource: types.MemoryMedia("x", "image/png", 4, 4, l.png), Note: l.note}, nil
}
func (quietLoader) Video(context.Context, string, *linkreader.Result, linkreader.Media, VideoLimits, bool) (Loaded, error) {
	return Loaded{}, linkreader.ErrMediaLimit
}
func (quietLoader) Ugoira(context.Context, *linkreader.UgoiraInfo) (Loaded, error) {
	return Loaded{}, linkreader.ErrMediaLimit
}

// #3 #49 #60 #44 #68：准备消息时，内部原因和调试说明不进群；封面没取到的卡片画成没有封面的样子，不画“封面不可用”。
func TestPrepareKeepsInternalNotesOut(t *testing.T) {
	png := mediaPNG(t, color.RGBA{G: 255, A: 255})
	var notices []string
	prep := func(l quietLoader, r linkreader.Result, o Options) string {
		cards := linkcard.New()
		defer cards.Close()
		p := Preparer{Media: l, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true, Video: true, GIF: true}, Forward: types.MediaCaps{Image: true, ImageBase64: true, Video: true, GIF: true},
			Limits: VideoLimits{Enabled: true, Timeout: time.Second, Bytes: 1 << 20}, Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil },
			Notice: func(s string) { notices = append(notices, s) }}
		d := Build(&r, "", o, 0)
		prepared, err := p.Prepare(context.Background(), &r, &d)
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Release()
		var out []string
		for _, part := range prepared.Parts {
			for _, seg := range part.Body {
				out = append(out, seg.Text)
				if seg.Semantic != "" {
					out = append(out, "[卡片]"+seg.Semantic)
					if seg.Media != nil && seg.Media.Height > 500 {
						out = append(out, "[卡片上有封面区]")
					}
				}
			}
		}
		return strings.Join(out, "\n")
	}
	// 图片超过大小上限、地址不在下载范围：不写“图片未下载”
	r := tgResult("纸船", 2)
	l := quietLoader{png: png, errs: map[string]error{r.Media.Value[0].URL.Value: linkreader.ErrMediaLimit, r.Media.Value[1].URL.Value: &linkreader.FetchError{Kind: "unsafe_target"}}}
	if got := prep(l, r, Options{}); strings.Contains(got, "未下载") || strings.Contains(got, "图床") {
		t.Fatalf("内部原因进了群：\n%s", got)
	}
	// 需要登录：照旧写
	l.errs = map[string]error{r.Media.Value[0].URL.Value: &linkreader.FetchError{Kind: "login_required", Status: 403}}
	if got := prep(l, r, Options{}); !strings.Contains(got, "图片未下载：图片需要登录才能看") {
		t.Fatalf("平台原因应当照旧写：\n%s", got)
	}
	// 视频超过大小上限：不写“视频未下载”
	v := tgResult("纸船视频", 0)
	v = withMedia(v, goldenVideo())
	if got := prep(quietLoader{png: png}, v, Options{}); strings.Contains(got, "未下载") {
		t.Fatalf("视频的内部原因进了群：\n%s", got)
	}
	// 原图退档的说明只进日志
	notices = nil
	pix := tgResult("纸船", 1)
	if got := prep(quietLoader{png: png, note: "原图超过20 MB，使用regular"}, pix, Options{}); strings.Contains(got, "regular") {
		t.Fatalf("调试说明进了群：\n%s", got)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "regular") {
		t.Fatalf("说明应当进日志：%v", notices)
	}
	// 卡片封面没取到：卡片照出，卡片上不写原因，消息里也不写
	g := caseNamed(t, "github")
	g = withMedia(g, goldenImage(9))
	l = quietLoader{png: png, errs: map[string]error{goldenImage(9).URL.Value: linkreader.ErrMediaLimit}}
	got := prep(l, g, Options{Templates: map[string]string{"github": "@卡片"}})
	if !strings.Contains(got, "[卡片]") || strings.Contains(got, "封面") || strings.Contains(got, "卡片未生成") {
		t.Fatalf("封面失败的说明进了群：\n%s", got)
	}
}

// 回归（B-22）：专栏、opus 一段一个 text 块、空段落是单独的空块；逐块清理会把段落之间的空行全清掉。
// 相邻文字块拼起来再压：段落之间留一个空行，多个空段落也只留一个。
func TestParagraphBlocksKeepOneBlankLine(t *testing.T) {
	r := siteResult("bilibili", "article", "cv900010")
	r.Title, r.Author.Name = av("纸船笔记"), av("纸灯")
	r.Text = av("第一段")
	r.Blocks = []linkreader.Block{{Type: "text", Text: "第一段"}, {Type: "text", Text: ""}, {Type: "text", Text: "  "}, {Type: "text", Text: "第二段"}, {Type: "text", Text: "第三段"}}
	got := strings.ReplaceAll(describe(t, r), "\n    ", "\n") // 去掉 Describe 给节点加的缩进
	if !strings.Contains(got, "第一段\n\n第二段\n第三段") || strings.Contains(got, "第一段\n\n\n") {
		t.Fatalf("段落空行不对：\n%q", got)
	}
}

// 安静的失败不留空节点：合并转发里一图一节点，那张图超过大小上限、不再写原因时，节点空了就整个去掉（KeepNodes）。
func TestQuietFailureDropsEmptyForwardNode(t *testing.T) {
	png := mediaPNG(t, color.RGBA{B: 255, A: 255})
	run := func(t *testing.T, n int) Prepared {
		r := goldenBase("pixiv", "artwork", "90090")
		r.Title, r.Author.Name = av("纸船连作"), av("纸云")
		var imgs []linkreader.Media
		for i := 1; i <= n; i++ {
			imgs = append(imgs, goldenImage(i))
		}
		r = withMedia(r, imgs...)
		r.Stats["pages"] = av(int64(n))
		l := quietLoader{png: png, errs: map[string]error{goldenImage(2).URL.Value: linkreader.ErrMediaLimit}}
		caps := types.MediaCaps{Image: true, ImageBase64: true}
		p := Preparer{Media: l, Direct: caps, Forward: caps, Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
		d := Build(&r, "", Options{}, 0)
		if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward || len(d.Parts[0].Nodes) != n+1 {
			t.Fatalf("应是 1 个文字节点加 %d 张图：%v", n, d.Describe())
		}
		prepared, err := p.Prepare(context.Background(), &r, &d)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = prepared.Release() })
		return prepared
	}
	noReason := func(t *testing.T, segs []types.BodySegment) {
		for _, seg := range segs {
			if strings.Contains(seg.Text, "未下载") {
				t.Fatalf("内部原因进了消息：%q", seg.Text)
			}
		}
	}
	t.Run("下到的图仍超过 3 张，照旧转发", func(t *testing.T) {
		prepared := run(t, 5)
		nodes := prepared.Parts[0].Nodes
		if prepared.Parts[0].Kind != types.PartForward || len(nodes) != 5 {
			t.Fatalf("失败那张的节点应当去掉：%s，%d 个节点", prepared.Parts[0].Kind, len(nodes))
		}
		for i, n := range nodes {
			if len(n.Body) == 0 {
				t.Fatalf("第 %d 个节点是空的", i+1)
			}
			noReason(t, n.Body)
		}
	})
	// 和“只因图多进的转发、下到不超过 3 张就收回成一条”叠在一起时，收回的那条也不带内部原因
	t.Run("下到 3 张，收回成一条", func(t *testing.T) {
		prepared := run(t, 4)
		if len(prepared.Parts) != 1 || prepared.Parts[0].Kind != types.PartMessage {
			t.Fatalf("应收回成一条消息：%+v", prepared.Parts)
		}
		images := 0
		for _, seg := range prepared.Parts[0].Body {
			if seg.Kind == types.BodyImage {
				images++
			}
		}
		if images != 3 {
			t.Fatalf("应带 3 张图，实际 %d", images)
		}
		noReason(t, prepared.Parts[0].Body)
	})
}

// 回归（#38）：长推文在空行处切成几个节点时，空行不留在前一个节点末尾。
func TestNodeSplitTrimsBlankLines(t *testing.T) {
	para := strings.Repeat("纸", 1500) // 4500 字节
	nodes := toNodes([]Item{text(para + "\n\n" + para)}, 5000)
	if len(nodes) != 2 {
		t.Fatalf("应切成两个节点：%d", len(nodes))
	}
	for i, n := range nodes {
		if s := n.Body[0].Text; s != para {
			t.Fatalf("节点 %d 首尾带了空行：%q…%q", i, s[:6], s[len(s)-6:])
		}
	}
}

// #18：纯图动态进合并转发时，“分享图片”也不能成为一个节点；送去翻译的也不带它。
func TestDynamicSharePlaceholderForward(t *testing.T) {
	r := dynamicWith("分享图片", 4)
	r.Blocks = append([]linkreader.Block{{Type: "text", Text: "分享图片"}}, r.Blocks...)
	key := linktemplate.SiteKey(r.Platform, r.Kind)
	d := build(r, Options{Sites: map[string]SiteSetting{key: {Forward: linktemplate.ForwardAlways}}})
	if _, ok := forwardOf(d); !ok {
		t.Fatalf("应该转发：%v", d.Describe())
	}
	if got := strings.Join(d.Describe(), "\n"); strings.Contains(got, "分享图片") {
		t.Fatalf("转发里还有占位正文：\n%s", got)
	}
	if got := textOf(t, build(r, Options{Sites: map[string]SiteSetting{key: {Forward: linktemplate.ForwardNever}}})); strings.Contains(got, "分享图片") {
		t.Fatalf("直发（带 text 块）：\n%s", got)
	}
	if src := TranslationSource(&r, Options{}); strings.Contains(src, "分享图片") {
		t.Fatalf("送去翻译的：%q", src)
	}
}

// #18：动态直发和转发首节点的时间写法一样，都不带“发布：”。
func TestDynamicTimeLabelSame(t *testing.T) {
	site, _, _ := linktemplate.SiteByKey("bilibili-dynamic")
	for _, style := range []string{site.Direct, site.Forward} {
		if strings.Contains(style, "发布：") {
			t.Fatalf("时间带了标签：%q", style)
		}
	}
}

// #65：Steam 只有游戏、礼包写“开发商”；个人主页是玩家本人，创意工坊是上传者。
func TestSteamDeveloperOnlyForApps(t *testing.T) {
	for kind, want := range map[string]bool{"app": true, "package": true, "profile": false, "sharedfile": false} {
		r := siteResult("steam", kind, "90065")
		r.Title, r.Author.Name = av("纸船"), av("纸舟工作室")
		got := describe(t, r)
		if strings.Contains(got, "开发商") != want {
			t.Errorf("%s：应当写开发商 = %v\n%s", kind, want, got)
		}
	}
}
