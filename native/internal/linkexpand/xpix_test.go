package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// X 长文：标题、封面、分隔线这几种块以前都落进“这一段未能读取”，封面图还丢了（2026-11-09 盘点实测）。
// 块序照真实文章（article_title、article_cover、text、divider、image）。
func TestXArticleTitleCoverDividerRender(t *testing.T) {
	r := goldenBase("x", "post", "80001")
	cover, pic := goldenImage(0), goldenImage(1)
	r.Text = av("引导语")
	r.Article = av("正文一")
	r.Blocks = []linkreader.Block{
		{Type: "text", Text: "引导语"},
		{Type: "article_title", Text: "纸船是怎么折的"},
		{Type: "article_cover", Media: &cover},
		{Type: "text", Text: "正文一"},
		{Type: "divider"},
		{Type: "image", Media: &pic},
	}
	var kinds, texts []string
	for _, it := range contents(&r, Options{}, true, 0) {
		kinds = append(kinds, it.Kind)
		texts = append(texts, it.Text)
	}
	got := strings.Join(texts, "|")
	if strings.Contains(got, "未能读取") {
		t.Fatalf("长文的块还是读不出来：%q", got)
	}
	want := []string{types.BodyText, types.BodyText, types.BodyImage, types.BodyText, types.BodyText, types.BodyImage}
	if strings.Join(kinds, ",") != strings.Join(want, ",") || texts[1] != "《纸船是怎么折的》" || texts[4] != "———" {
		t.Fatalf("顺序或内容不对：%v %q", kinds, texts)
	}
	// 走完整的 Build：长文走合并转发，封面和正文图都在节点里
	d := Build(&r, "", Options{}, 0)
	images := 0
	for _, p := range d.Parts {
		for _, n := range p.Nodes {
			for _, it := range n.Body {
				if it.Kind == types.BodyImage {
					images++
				}
			}
		}
	}
	if images != 2 {
		t.Fatalf("封面和正文图应各占一个节点，得到 %d：%v", images, d.Describe())
	}
}

func TestXArticleMissingImageSaysSo(t *testing.T) {
	r := goldenBase("x", "post", "80001")
	r.Text = av("t")
	r.Blocks = []linkreader.Block{{Type: "text", Text: "t"}, {Type: "image", State: "not_returned", Reason: "article_media_omitted"}}
	got := contents(&r, Options{}, true, 0)
	if len(got) != 2 || got[1].Text != "这里有一张图没取到" {
		t.Fatalf("缺图悄悄消失：%+v", got)
	}
}

func TestXPollText(t *testing.T) {
	end := time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)
	p := &linkreader.PollInfo{
		Choices: []linkreader.PollChoice{{Label: "纸船", Count: av(int64(3118)), Percent: av(81.9)}, {Label: "纸鸢", Count: av(int64(690)), Percent: av(18.1)}},
		Total:   av(int64(3808)), EndsAt: av(end),
	}
	if got := pollText(p, end.Add(time.Hour)); got != "投票（共 3808 票，已结束）\n纸船：81.9%，3118 票\n纸鸢：18.1%，690 票" {
		t.Fatalf("已结束：%q", got)
	}
	if got := pollText(p, end.Add(-time.Hour)); !strings.HasPrefix(got, "投票（共 3808 票，截止 10-09 02:00）") {
		t.Fatalf("未结束：%q", got)
	}
	// 什么都缺：只写选项名，不写“未返回”，也不把没有的结束时间当成已结束
	bare := &linkreader.PollInfo{Choices: []linkreader.PollChoice{{Label: "甲"}}}
	if got := pollText(bare, end); got != "投票\n甲" {
		t.Fatalf("缺项：%q", got)
	}
}

// 投票、被引已删、更深引用三种提示都进了消息；投票排在正文后面。
func TestXPollTombstoneDeepQuoteInMessage(t *testing.T) {
	r := goldenBase("x", "post", "80001")
	r.Text = av("投哪个")
	r.Poll = &linkreader.PollInfo{Choices: []linkreader.PollChoice{{Label: "甲", Percent: av(60.0)}, {Label: "乙", Percent: av(40.0)}}}
	r.Blocks = []linkreader.Block{{Type: "text", Text: "投哪个"}, {Type: "poll"}, {Type: "quote_unavailable", Reason: "deleted"}}
	var all []string
	for _, it := range contents(&r, Options{}, true, 0) {
		all = append(all, it.Text)
	}
	got := strings.Join(all, "\n")
	for _, want := range []string{"投哪个\n投票\n甲：60%\n乙：40%", "被引用的推文已被删除"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺 %q：%q", want, got)
		}
	}
	if tombstoneText("private") != "被引用的推文是受保护的，看不到" || !strings.Contains(tombstoneText("weird"), "weird") {
		t.Fatal("墓碑原因文案")
	}

	// 被引的那条自己还引用了别人：外层直发里要写“还有更深引用”
	q := goldenBase("x", "post", "80002")
	q.Text = av("中")
	q.MoreQuotes = av(true)
	q.Blocks = []linkreader.Block{{Type: "text", Text: "中"}}
	outer := goldenBase("x", "post", "80003")
	outer.Text = av("外")
	outer.Quote = av(&q)
	outer.Blocks = []linkreader.Block{{Type: "text", Text: "外"}, {Type: "quote", Quote: &q}}
	var deep []string
	for _, it := range contents(&outer, Options{}, true, 0) {
		deep = append(deep, it.Text)
	}
	if !strings.Contains(strings.Join(deep, "\n"), "还有更深引用，看原文") {
		t.Fatalf("更深引用没提示：%v", deep)
	}
}

func pixivSeriesResult(kind, seriesType string) linkreader.Result {
	r := goldenBase("pixiv", kind, "90003")
	r.Title = av("纸船连载")
	r.Text = av("每周一话")
	r.Author.Name = av("编造画师甲")
	r.Author.Account = linkreader.Field[string]{State: "not_returned", Reason: "interface_omitted"}
	r.Flags["ai_generated"] = av[any](false)
	r.Flags["x_restrict"] = av[any](0)
	r.Stats["total"] = av(int64(70))
	r.Series = &linkreader.SeriesInfo{Type: seriesType, ID: "90003", Title: r.Title, Total: av(int64(70)), FirstID: "90010", LatestID: "90030"}
	r.Media = av([]linkreader.Media{goldenImage(0)})
	return r
}

// 系列页展开成一条消息：首行同作品（[AI]🔞 | 标题 | 作者名，用户 2026-11-12）、下一行链接、共几话、封面；不写平台名和简介。
func TestPixivSeriesPageBuild(t *testing.T) {
	for _, c := range []struct {
		kind, seriesType string
		restrict         int
		ai               bool
		want             string
	}{
		{"series", "manga", 0, false, "纸船连载 | 编造画师甲\nhttps://example.invalid/pixiv/90003\n共70话"},
		{"novel_series", "novel", 0, false, "纸船连载 | 编造画师甲\nhttps://example.invalid/pixiv/90003\n共70篇"},
		// 回归：R18 的系列以前首行没有 🔞（P-14）
		{"novel_series", "novel", 1, true, "[AI]🔞 | 纸船连载 | 编造画师甲\nhttps://example.invalid/pixiv/90003\n共70篇"},
	} {
		r := pixivSeriesResult(c.kind, c.seriesType)
		r.Flags["x_restrict"] = av[any](c.restrict)
		r.Flags["ai_generated"] = av[any](c.ai)
		d := Build(&r, "", Options{}, 0)
		if len(d.Parts) != 1 {
			t.Fatalf("%s 应是一条消息：%v", c.kind, d.Describe())
		}
		got := d.Describe()[0]
		if !strings.HasPrefix(got, c.want) || !strings.HasSuffix(got, "[图片]") {
			t.Errorf("%s：\n%s", c.kind, got)
		}
		// 卡片样式也能出（genericCard 认得系列站点）
		card := Build(&r, "", Options{Templates: map[string]string{"pixiv-series": "@卡片"}}, 0).Describe()
		if len(card) != 1 || !strings.Contains(card[0], "[卡片：纸船连载]") {
			t.Errorf("%s 卡片样式：%v", c.kind, card)
		}
	}
	// 什么都缺：只有标题也要能发，缺的行不写“未返回”
	bare := goldenBase("pixiv", "series", "90003")
	bare.Title = av("纸船连载")
	got := Build(&bare, "", Options{}, 0).Describe()
	if len(got) != 1 || strings.Contains(got[0], "未返回") || !strings.Contains(got[0], "纸船连载") {
		t.Fatalf("缺项：%v", got)
	}
}

func TestPixivNavigationLine(t *testing.T) {
	r := goldenBase("pixiv", "artwork", "90001")
	r.Title = av("纸船试航")
	r.Stats["pages"] = av(int64(1))
	r.Media = av([]linkreader.Media{goldenImage(0)})
	r.Blocks = []linkreader.Block{{Type: "image", Media: &r.Media.Value[0]}}
	r.Series = &linkreader.SeriesInfo{Type: "manga", ID: "90003", Title: av("纸船连载"), Order: av(int64(68)),
		Prev: &linkreader.SeriesLink{ID: "90023", Title: av("上一话"), Order: av(int64(67))}}
	// 内置默认只有首行和图（规格-链接发送改版 B7）；$系列 字段还在，模板写上就有
	series := Options{Templates: map[string]string{"pixiv-artwork": "$标题[\n系列：$系列]$图片"}}
	got := Build(&r, "", series, 0).Describe()[0]
	if !strings.Contains(got, "系列：纸船连载 第68话") {
		t.Fatalf("没有系列行：%q", got)
	}
	// 自定义模板可以带上一话、下一话
	custom := Build(&r, "", Options{Templates: map[string]string{"pixiv-artwork": "$标题[\n上一话：$上一话][\n下一话：$下一话]"}}, 0).Describe()[0]
	if !strings.Contains(custom, "上一话：第67话 上一话 https://www.pixiv.net/artworks/90023") || strings.Contains(custom, "下一话") {
		t.Fatalf("导航字段：%q", custom)
	}
	// 没有系列的作品不多出一行
	r.Series = nil
	if got = Build(&r, "", series, 0).Describe()[0]; strings.Contains(got, "系列") {
		t.Fatalf("无系列却写了系列：%q", got)
	}
}

// 小说封面放在元信息之后的单独节点里；没有封面就没有那个节点。
func TestPixivNovelCoverNode(t *testing.T) {
	r := goldenBase("pixiv", "novel", "90010")
	r.Title = av("编造的清晨散步")
	r.Text = av("正文开头")
	r.Tags = av([]string{"编造日常"})
	r.Novel = &linkreader.NovelInfo{Description: av("简介"), CharacterCount: av(int64(1580)), Cover: linkreader.Field[linkreader.Media]{State: "not_returned", Reason: "interface_omitted"}}
	count := func(d Draft) int {
		n := 0
		for _, p := range d.Parts {
			for _, node := range p.Nodes {
				for _, it := range node.Body {
					if it.Kind == types.BodyImage {
						n++
					}
				}
			}
		}
		return n
	}
	if n := count(Build(&r, "", Options{}, 0)); n != 0 {
		t.Fatalf("没有封面却有图：%d", n)
	}
	cover := goldenImage(9)
	cover.Referer = "https://www.pixiv.net/"
	r.Novel.Cover = av(cover)
	d := Build(&r, "", Options{}, 0)
	if count(d) != 1 {
		t.Fatalf("封面应占一个节点：%v", d.Describe())
	}
	// 封面不是正文插图：只放 Media 里的才是插图，所以不会重复出现
	r.Media = av([]linkreader.Media{goldenImage(1)})
	r.Blocks = []linkreader.Block{{Type: "text", Text: "正文开头"}, {Type: "image", Media: &r.Media.Value[0]}}
	if n := count(Build(&r, "", Options{}, 0)); n != 2 {
		t.Fatalf("封面加一张插图应是两张图，得到 %d", n)
	}
}

// X-22：长文走合并转发时译文要真的出现，不能“记了已翻译、输出全是英文”（用户 2026-11-13 复核新 2-1）：
// 第一个节点是作者行加文章标题，不是只剩“名字[@ID]：”（新 2-2）。2026-11-14 起长文的译文在原位换掉原文，不再在末尾另起“——————译文”节点
// （详细验收见 translation_followup_test.go 的 TestXArticleForwardTranslatedInPlace）。
func TestXArticleForwardShowsTranslation(t *testing.T) {
	r := goldenBase("x", "post", "80002")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper_cloud")
	r.Text = av("引导语")
	r.Article = av("正文一")
	r.Blocks = []linkreader.Block{
		{Type: "article_title", Text: "How paper boats float"},
		{Type: "text", Text: "Paper boats float because of air."},
	}
	d := Build(&r, "", Options{ArticleTranslation: "纸船为什么能浮起来\n%%\n纸船能浮起来是因为空气。"}, 0)
	nodes := nodeTexts(d)
	all := strings.Join(nodes, "\n")
	if len(nodes) < 1 || !strings.Contains(nodes[0], "纸云[@paper_cloud]：") || !strings.Contains(nodes[0], "《纸船为什么能浮起来》") {
		t.Fatalf("第一个节点应有作者行和标题（译文）：%q", nodes)
	}
	if !strings.Contains(all, "纸船能浮起来是因为空气。") || strings.Contains(all, "Paper boats float") || strings.Contains(all, "——————") {
		t.Fatalf("只放译文，原文和分隔线都不出现：%q", nodes)
	}
	// 没有译文：不出分隔线
	d = Build(&r, "", Options{}, 0)
	for _, n := range nodeTexts(d) {
		if strings.Contains(n, "——————") {
			t.Fatalf("没有译文不该有分隔线：%q", n)
		}
	}
}
