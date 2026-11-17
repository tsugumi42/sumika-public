package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktranslate"
)

// 2026-11-14（规格-链接发送改版 B14）：被引推文、X 长文也翻译并只放译文；投票选项也只写译文。

func quotePost(mainText, quoteText string) linkreader.Result {
	q := goldenBase("x", "post", "90010")
	q.CleanURL = "https://x.com/i/status/90010"
	q.Author.Name, q.Author.Account = av("被引的人"), av("quoted_one")
	q.Text = av(quoteText)
	q.Blocks = []linkreader.Block{{Type: "text", Text: quoteText}}
	r := xPost(mainText)
	r.Quote = av(&q)
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "quote", Quote: &q})
	return r
}

// 被引推文有译文：直发里 $被引推文 只放译文，下一行（翻译自日语），原文不出现；没有译文照旧写原文。
func TestQuoteTranslationReplacesOriginal(t *testing.T) {
	r := quotePost("冬コミ受かりました！", "こちらが参加サークルの一覧です")
	never := map[string]SiteSetting{"x": {Forward: "never"}}
	plain := descTr(&r, Options{Sites: never, Translation: "冬季同人展上了！"})
	if !strings.Contains(plain, "こちらが参加サークルの一覧です") {
		t.Fatalf("没有被引推文的译文照旧写原文：%q", plain)
	}
	with := descTr(&r, Options{Sites: never, Translation: "冬季同人展上了！", QuoteTranslation: "这是参加社团的名单"})
	if strings.Contains(with, "参加サークル") {
		t.Fatalf("被引推文有译文时原文不出现：%q", with)
	}
	if !strings.Contains(with, "被引的人[@quoted_one]：\n「这是参加社团的名单」\n（翻译自日语）") {
		t.Fatalf("被引推文排法不对：%q", with)
	}
	// 合并转发：被引推文自己一个节点，同样只放译文
	fwd := descTr(&r, Options{Translation: "冬季同人展上了！", QuoteTranslation: "这是参加社团的名单"})
	if !strings.Contains(fwd, "「这是参加社团的名单」\n    （翻译自日语）") || strings.Contains(fwd, "参加サークル") { // Describe 给节点里的行缩进四格
		t.Fatalf("转发里的被引推文：%q", fwd)
	}
	// 译文和原文一样（模型原样回了）：不写“翻译自”
	same := descTr(&r, Options{Sites: never, QuoteTranslation: "こちらが参加サークルの一覧です"})
	if strings.Contains(same, "翻译自") {
		t.Fatalf("译文和原文一样不该写翻译自：%q", same)
	}
}

// 被引推文要不要翻独立于主推文：主推文纯图（没有正文）也照翻被引推文；模板里没有 $被引推文 也没有卡片时不翻；
// 主推文和被引推文的送翻译文本各是各的，互不夹带。
func TestQuoteTranslationSourceIsIndependent(t *testing.T) {
	r := quotePost("冬コミ受かりました！", "こちらが参加サークルの一覧です")
	src := TranslationSources(&r, Options{})
	if src.Main != "冬コミ受かりました！" || src.Quote != "こちらが参加サークルの一覧です" || src.Article != "" {
		t.Fatalf("三段来源：%+v", src)
	}
	img := quotePost("", "こちらが参加サークルの一覧です")
	img.Text = av("")
	img.Blocks = img.Blocks[1:]
	if src := TranslationSources(&img, Options{}); src.Main != "" || src.Quote == "" {
		t.Fatalf("主推文没有正文，被引推文照翻：%+v", src)
	}
	noQuoteTpl := Options{Templates: map[string]string{"x": "$链接$正文文字"}, Sites: map[string]SiteSetting{"x": {Forward: "never"}}}
	if src := TranslationSources(&r, noQuoteTpl); src.Quote != "" || src.Main == "" {
		t.Fatalf("模板没写 $被引推文：被引推文不翻：%+v", src)
	}
	onlyQuote := Options{Templates: map[string]string{"x": "$链接$被引推文"}, Sites: map[string]SiteSetting{"x": {Forward: "never"}}}
	if src := TranslationSources(&r, onlyQuote); src.Quote == "" || src.Main != "" {
		t.Fatalf("模板只写 $被引推文：只翻被引推文：%+v", src)
	}
	// 被引推文是简体中文等不用翻的文字：来源照给，Needs 在翻译那一层判（和主推文同一道门）
	if ok, _ := linktranslate.Needs("这是简体中文的被引推文", "简体中文"); ok {
		t.Fatal("简体不该翻")
	}
}

// 卡片：引用框里的被引推文换成译文、投票选项换成译文，Item 上带着；没有译文时是空。
func TestCardCarriesQuoteAndPollTranslation(t *testing.T) {
	r := quotePost("Which one do you like for the new print run?", "Here is the list of circles that will join")
	r.Poll = &linkreader.PollInfo{Choices: []linkreader.PollChoice{{Label: "Paper boat"}, {Label: "Paper kite"}}}
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "poll"})
	o := Options{Templates: map[string]string{"x": "$卡片"}, Sites: map[string]SiteSetting{"x": {Forward: "never"}},
		Translation: "你更喜欢新一批印刷的哪个？\n纸船\n纸鸢", QuoteTranslation: "这是参加的社团名单"}
	d := build(r, o)
	var card *Item
	for i := range d.Parts {
		for j := range d.Parts[i].Body {
			if d.Parts[i].Body[j].Kind == "card" {
				card = &d.Parts[i].Body[j]
			}
		}
	}
	if card == nil {
		t.Fatalf("没有卡片：%v", d.Describe())
	}
	if card.Translation != "你更喜欢新一批印刷的哪个？" || strings.Join(card.PollLabels, "|") != "纸船|纸鸢" || card.QuoteTranslation != "这是参加的社团名单" {
		t.Fatalf("卡片项：%+v", card)
	}
	d = build(r, Options{Templates: o.Templates, Sites: o.Sites})
	for i := range d.Parts {
		for _, it := range d.Parts[i].Body {
			if it.Kind == "card" && (it.PollLabels != nil || it.QuoteTranslation != "") {
				t.Fatalf("没有译文时卡片项不该带：%+v", it)
			}
		}
	}
}

func pollPost(text string, labels ...string) linkreader.Result {
	r := xPost(text)
	p := &linkreader.PollInfo{Total: av(int64(7)), EndsAt: av(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))}
	for i, l := range labels {
		p.Choices = append(p.Choices, linkreader.PollChoice{Label: l, Count: av(int64(i + 1))})
	}
	r.Poll = p
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "poll"})
	return r
}

// 投票选项也只写译文（用户 2026-11-14，推翻 11-12 的“原文和译文里都写选项”）：投票块写译文选项加票数，原文选项不出现；
// $原文 照旧永远是原文（含原文选项）。
func TestPollChoicesOnlyTranslation(t *testing.T) {
	r := pollPost("Which one do you like for the new print run?", "Paper boat", "Paper kite")
	never := map[string]SiteSetting{"x": {Forward: "never"}}
	got := descTr(&r, Options{Sites: never, Translation: "你更喜欢新一批印刷的哪个？\n纸船\n纸鸢"})
	if strings.Contains(got, "Paper boat") || strings.Contains(got, "Which one") {
		t.Fatalf("选项和正文都只放译文：%q", got)
	}
	if !strings.Contains(got, "「你更喜欢新一批印刷的哪个？\n投票（共 7 票，已结束）\n纸船：1 票\n纸鸢：2 票」\n（翻译自英语）") {
		t.Fatalf("投票块写译文选项加票数：%q", got)
	}
	if strings.Count(got, "纸船") != 1 {
		t.Fatalf("译文选项只出现一次（不在正文里再写一遍）：%q", got)
	}
	orig := descTr(&r, Options{Sites: never, Translation: "你更喜欢新一批印刷的哪个？\n纸船\n纸鸢", Templates: map[string]string{"x": "$原文"}})
	if !strings.Contains(orig, "Paper boat：1 票") || strings.Contains(orig, "纸船") {
		t.Fatalf("$原文 要写原文选项：%q", orig)
	}
	// 只有投票、没有正文：选项照样译成译文，写翻译自
	bare := pollPost("", "Paper boat", "Paper kite")
	bare.Text, bare.Blocks = av(""), bare.Blocks[1:]
	if src := TranslationSource(&bare, Options{}); src != "Paper boat\nPaper kite" {
		t.Fatalf("只有投票时送去翻译的是选项：%q", src)
	}
	got = descTr(&bare, Options{Sites: never, Translation: "纸船\n纸鸢"})
	if !strings.Contains(got, "纸船：1 票") || strings.Contains(got, "Paper boat") || !strings.Contains(got, "（翻译自") {
		t.Fatalf("只有投票也只放译文选项：%q", got)
	}
}

// 行数对不上就退回原文选项：模型把几行并成一行、少回一行、多回一行，选项都不能错位；TranslationProblems 报 poll_lines。
func TestPollTranslationLineMismatchFallsBack(t *testing.T) {
	r := pollPost("Which one do you like for the new print run?\nPlease vote", "Paper boat", "Paper kite")
	cleaned := cleanResult(&r, Options{})
	src := withPollChoices(ownText(cleaned), cleaned)
	if src != "Which one do you like for the new print run?\nPlease vote\nPaper boat\nPaper kite" {
		t.Fatalf("送去翻译的：%q", src)
	}
	body, labels, ok := splitPollTranslation(cleaned, "你更喜欢新一批印刷的哪个？\n请投票\n纸船\n纸鸢")
	if !ok || body != "你更喜欢新一批印刷的哪个？\n请投票" || strings.Join(labels, "|") != "纸船|纸鸢" {
		t.Fatalf("行数对得上：%q %v %v", body, labels, ok)
	}
	for name, bad := range map[string]string{
		"并成一行":    "你更喜欢新一批印刷的哪个？请投票 纸船 纸鸢",
		"少一行":     "你更喜欢新一批印刷的哪个？\n请投票\n纸船",
		"多一行":     "你更喜欢新一批印刷的哪个？\n\n请投票\n纸船\n纸鸢",
		"选项行是空行":  "你更喜欢新一批印刷的哪个？\n请投票\n纸船\n",
		"正文并了选项行": "你更喜欢新一批印刷的哪个？请投票\n纸船\n纸鸢",
	} {
		_, labels, ok := splitPollTranslation(cleaned, bad)
		if ok || labels != nil {
			t.Errorf("%s：该退回原文选项：%v %v", name, labels, ok)
		}
		o := Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}, Translation: bad}
		if probs := TranslationProblems(&r, o); len(probs) != 1 || probs[0] != "poll_lines" {
			t.Errorf("%s：问题 %v", name, probs)
		}
		got := descTr(&r, o)
		if !strings.Contains(got, "Paper boat：1 票") || strings.Contains(got, "纸鸢：") || strings.Contains(got, "纸船：") {
			t.Errorf("%s：投票块该写原文选项：%q", name, got)
		}
	}
	// 并成一行时，译文行数比选项少，整段译文不用（不凭猜测拆）
	got := descTr(&r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}, Translation: "你更喜欢新一批印刷的哪个？请投票 纸船 纸鸢"})
	if !strings.Contains(got, "Which one do you like for the new print run?") || strings.Contains(got, "翻译自") {
		t.Fatalf("译文用不上时正文也是原文：%q", got)
	}
}

func articlePost() linkreader.Result {
	r := goldenBase("x", "post", "80002")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper_cloud")
	r.Text = av("引导语")
	r.Article = av("正文一")
	img := goldenImage(3)
	r.Media = av([]linkreader.Media{img})
	r.Blocks = []linkreader.Block{
		{Type: "article_title", Text: "How paper boats float"},
		{Type: "text", Text: "Paper boats float because of the air that is trapped inside them."},
		{Type: "image", Media: &r.Media.Value[0]},
		{Type: "text", Text: "Folding matters too, and that is the part of the craft that you learn first."},
	}
	return r
}

func nodeTexts(d Draft) []string {
	var nodes []string
	for _, p := range d.Parts {
		for _, n := range p.Nodes {
			var s []string
			for _, it := range n.Body {
				if it.Kind == "text" {
					s = append(s, it.Text)
				} else {
					s = append(s, "["+it.Kind+"]")
				}
			}
			nodes = append(nodes, strings.Join(s, "|"))
		}
	}
	return nodes
}

// X 长文：文字块和小标题按段送去翻译（段与段之间 %% 单独一行），译文按段在原位填回，图留在原位；只放译文，原文不出现，
// 不再在末尾另起“——————译文”节点；头部一行（翻译自英语）。
func TestXArticleForwardTranslatedInPlace(t *testing.T) {
	r := articlePost()
	src := TranslationSources(&r, Options{})
	if src.Main != "" || src.Article != "How paper boats float\n%%\nPaper boats float because of the air that is trapped inside them.\n%%\nFolding matters too, and that is the part of the craft that you learn first." {
		t.Fatalf("长文送去翻译的：%+v", src)
	}
	tr := "纸船为什么能浮起来\n%%\n纸船能浮起来是因为里面困住了空气。\n%%\n折法也很重要。"
	d := Build(&r, "", Options{ArticleTranslation: tr}, 0)
	all := strings.Join(nodeTexts(d), "\n")
	for _, orig := range []string{"Paper boats float", "Folding matters", "How paper boats", "——————"} {
		if strings.Contains(all, orig) {
			t.Fatalf("长文只放译文，%q 不该出现：%q", orig, all)
		}
	}
	for _, want := range []string{"（翻译自英语）", "《纸船为什么能浮起来》", "纸船能浮起来是因为里面困住了空气。", "折法也很重要。", "[image]"} {
		if !strings.Contains(all, want) {
			t.Fatalf("缺 %q：%q", want, all)
		}
	}
	if !(strings.Index(all, "纸船能浮起来") < strings.Index(all, "[image]") && strings.Index(all, "[image]") < strings.Index(all, "折法也很重要")) {
		t.Fatalf("图要留在两段之间：%q", all)
	}
	// 没有译文：原样，没有翻译自
	d = Build(&r, "", Options{}, 0)
	all = strings.Join(nodeTexts(d), "\n")
	if !strings.Contains(all, "Paper boats float because of the air that is trapped inside them.") || strings.Contains(all, "翻译自") {
		t.Fatalf("没有译文照旧：%q", all)
	}
}

// 长文段数对不上（模型合并了段、丢了分隔行、某段是空的）：整篇用原文，不把译文对错位置；TranslationProblems 报 article_sections。
func TestXArticleTranslationSectionMismatchFallsBack(t *testing.T) {
	r := articlePost()
	for name, bad := range map[string]string{
		"丢了分隔行": "纸船为什么能浮起来\n纸船能浮起来是因为里面困住了空气。\n折法也很重要。",
		"少一段":   "纸船为什么能浮起来\n%%\n纸船能浮起来是因为里面困住了空气，折法也很重要。",
		"多一段":   "甲\n%%\n乙\n%%\n丙\n%%\n丁",
		"有一段是空": "纸船为什么能浮起来\n%%\n%%\n折法也很重要。",
	} {
		o := Options{ArticleTranslation: bad}
		if probs := TranslationProblems(&r, o); len(probs) != 1 || probs[0] != "article_sections" {
			t.Errorf("%s：问题 %v", name, probs)
		}
		all := strings.Join(nodeTexts(Build(&r, "", o, 0)), "\n")
		if !strings.Contains(all, "Paper boats float because of the air that is trapped inside them.") || strings.Contains(all, "折法也很重要") || strings.Contains(all, "翻译自") {
			t.Errorf("%s：整篇该用原文：%q", name, all)
		}
	}
	good := Options{ArticleTranslation: "纸船为什么能浮起来\n%%\n纸船能浮起来是因为里面困住了空气。\n%%\n折法也很重要。"}
	if probs := TranslationProblems(&r, good); len(probs) != 0 {
		t.Fatalf("对得上不该报：%v", probs)
	}
}

// 长文超过一次上限（linktranslate.MaxRunes）仍送去翻，由 Translator 分段（ADR-391）；来源照给、不再在 Needs 这一层挡掉。
func TestXArticleOverLimitSegmented(t *testing.T) {
	r := articlePost()
	r.Blocks[1].Text = strings.Repeat("Paper boats float because of the air that is trapped inside them. ", 140) // 约 4800 字
	src := TranslationSources(&r, Options{})
	if src.Article == "" {
		t.Fatal("来源照给，由 Translator 分段")
	}
	if ok, why := linktranslate.Needs(src.Article, "简体中文"); !ok {
		t.Fatalf("超过一次上限也要翻（分段）：%v %q", ok, why)
	}
	// 来源里的分隔行不是字母，不影响语种判断：短的长文照样是“要翻”
	short := articlePost()
	if ok, _ := linktranslate.Needs(TranslationSources(&short, Options{}).Article, "简体中文"); !ok {
		t.Fatal("英文长文该翻")
	}
}

// 被引的 X 长文：标题和正文块按 %% 分段送翻，译文在原位填回，原文不出现（ADR-391）。
func TestQuotedArticleTranslated(t *testing.T) {
	r := goldenBase("x", "post", "80010")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper_cloud")
	r.Text = av("看这篇长文")
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	q := articlePost()
	q.ID = "80011"
	q.Author.Name, q.Author.Account = av("纸灯"), av("paper_lamp")
	r.Quote = av(&q)
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "quote", Quote: &q})

	src := TranslationSources(&r, Options{})
	if src.Quote != "" || src.QuoteArticle == "" {
		t.Fatalf("被引长文要走 QuoteArticle：%+v", src)
	}
	want := "How paper boats float\n%%\nPaper boats float because of the air that is trapped inside them.\n%%\nFolding matters too, and that is the part of the craft that you learn first."
	if src.QuoteArticle != want {
		t.Fatalf("被引长文送去翻译的：%q", src.QuoteArticle)
	}
	tr := "纸船为什么能浮起来\n%%\n纸船能浮起来是因为里面困住了空气。\n%%\n折法也很重要。"
	all := strings.Join(nodeTexts(Build(&r, "", Options{QuoteArticleTranslation: tr}, 0)), "\n")
	for _, orig := range []string{"Paper boats float", "How paper boats", "Folding matters"} {
		if strings.Contains(all, orig) {
			t.Fatalf("被引长文只放译文，%q 不该出现：%q", orig, all)
		}
	}
	for _, want := range []string{"《纸船为什么能浮起来》", "纸船能浮起来是因为里面困住了空气。", "折法也很重要。", "（翻译自英语）"} {
		if !strings.Contains(all, want) {
			t.Fatalf("缺 %q：%q", want, all)
		}
	}
	// 段数对不上整篇用原文并记账
	bad := Options{QuoteArticleTranslation: "只有一段"}
	if probs := TranslationProblems(&r, bad); len(probs) != 1 || probs[0] != "quote_article_sections" {
		t.Fatalf("段数对不上该报 quote_article_sections：%v", probs)
	}
}

// 主长文正文里嵌的推文逐条送翻，并在原位换成译文（ADR-391）。
func TestArticleEmbeddedTweetTranslated(t *testing.T) {
	r := articlePost()
	e := goldenBase("x", "post", "80012")
	e.Author.Name, e.Author.Account = av("被引的人"), av("quoted_one")
	e.Text = av("This embedded tweet is in English.")
	e.Blocks = []linkreader.Block{{Type: "text", Text: e.Text.Value}}
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "quote", Quote: &e})

	src := TranslationSources(&r, Options{})
	if len(src.Embeds) != 1 || src.Embeds[0].Key != tweetKey(&e) || !strings.Contains(src.Embeds[0].Text, "embedded tweet") {
		t.Fatalf("正文里嵌的推文要单独送翻：%+v", src.Embeds)
	}
	tr := "纸船为什么能浮起来\n%%\n纸船能浮起来是因为里面困住了空气。\n%%\n折法也很重要。"
	all := strings.Join(nodeTexts(Build(&r, "", Options{Translations: Translations{
		Article: tr,
		Embeds:  map[string]Embedded{tweetKey(&e): {Text: "这是嵌入推文的译文。"}},
	}}, 0)), "\n")
	if !strings.Contains(all, "这是嵌入推文的译文。") || strings.Contains(all, "This embedded tweet") {
		t.Fatalf("嵌入推文要原位换成译文：%q", all)
	}
	if !strings.Contains(all, "引用 被引的人") || !strings.Contains(all, "（翻译自英语）") {
		t.Fatalf("嵌入推文该有引用头和翻译说明：%q", all)
	}
}
