package linkexpand

import (
	"maps"
	"regexp"
	"strings"
	"unicode"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
	"sumika-native/internal/linktranslate"
)

// 翻译接入点（规格-链接翻译）：哪些站点有 $译文 字段、该翻哪一段、当前模板要不要译文。
// 站点要接翻译：在 linktemplate 的字段表里加 translation 字段，在 translatableSites 里登记（$译文 的值由 builder 统一赋）。
// 只有推文（x）是“原文加译文”一起写（「原文\n——————\n中文」）；以后接的非推文站点有译文时只出译文：
// $正文文字、$简介 在有译文时不再写（规格-链接发送改版 B5，translationOnly）。

// translatableSites 是已经接了 $译文 的模板站点键。
var translatableSites = map[string]bool{"x": true}

// TranslationSource 返回这条结果里该翻译的帖子正文（含投票选项）；站点没接翻译、当前模板（含自定义）里没有 $译文、没有可翻的正文、是 X 长文时返回空串。
// 空串意味着不要调模型：模板没用到译文就不花这一次调用。被引推文和长文见 TranslationSources。
func TranslationSource(r *linkreader.Result, o Options) string { return TranslationSources(r, o).Main }

// Sources 是一条结果里要送去翻译的几段文字，各自一次独立的调用、并发去翻、各自失败各自退回原文。空串表示这一段不翻。
type Sources struct {
	Main         string // 帖子自己的正文，后面接投票选项（一个选项一行）；X 长文这里是空，长文走 Article
	Quote        string // 被引推文的正文（2026-11-14 用户：被引推文也翻，同样只放译文）
	Article      string // X 长文：文字块和小标题按顺序，用 linktranslate.SectionMarker 单独一行隔开
	QuoteArticle string // 被引的 X 长文：格式同 Article
	Embeds       []EmbedSource
}

// EmbedSource 是主长文正文里嵌的一条推文：键是 tweetKey（平台加 ID），Text 是它自己的正文。
type EmbedSource struct {
	Key  string
	Text string
}

// Embedded 是一条嵌入推文（或被引长文）的译文：Article 为真说明 Text 是带 %% 分段的长文译文，否则是普通正文译文。
type Embedded struct {
	Text    string
	Article bool
}

// Translations 是宿主拿回来的各段译文；键是 tweetKey。
type Translations struct {
	Main, Quote, Article, QuoteArticle string
	Embeds                             map[string]Embedded
}

// TranslationSources 同上，一次给出主推文、被引推文、长文、被引长文和长文里嵌的推文。
// 送去翻译的和发出去的正文是同一份：都清理过、去过结尾的 hashtag。
// 被引推文要不要翻看模板里有没有 $被引推文（或画卡片的 $卡片），和主推文要不要翻互不影响。
func TranslationSources(r *linkreader.Result, o Options) Sources {
	key := linktemplate.SiteKey(r.Platform, r.Kind)
	if !translatableSites[key] {
		return Sources{}
	}
	srcs := templateSources(key, o)
	useBody, useQuote := false, false
	for _, low := range srcs {
		useBody = useBody || usesAny(low, "$译文", "$translation", "$卡片", "$card", "$正文文字", "$bodytext")
		useQuote = useQuote || usesAny(low, "$被引推文", "$quoted", "$卡片", "$card")
	}
	if !useBody && !useQuote {
		return Sources{}
	}
	r = cleanResult(r, o)
	var s Sources
	if useBody {
		if xArticle(r) {
			s.Article = articleSource(r)
			s.Embeds = articleEmbeds(r)
		} else {
			s.Main = mainSource(r)
		}
	}
	if useQuote {
		if q := quoteForTranslation(r); q != nil {
			if xArticle(q) {
				s.QuoteArticle = articleSource(q)
			} else {
				s.Quote = ownText(q)
			}
			s.Embeds = dropEmbed(s.Embeds, tweetKey(q))
		}
	}
	return s
}

// quoteForTranslation 是这条结果里“推文自己引用的那条”：长文正文里的 quote 块是嵌在正文里的推文，
// 不算帖子的被引推文（它们的翻译走 Embeds，ADR-391）；其它帖子照 quotedTweet 取。
func quoteForTranslation(r *linkreader.Result) *linkreader.Result {
	if xArticle(r) {
		if r.Quote.State == "available" {
			return r.Quote.Value
		}
		return nil
	}
	return quotedTweet(r)
}

// tweetKey 是一条推文在译文表里的键：平台加 ID；没有 ID（编的样本、旧缓存）返回空串，这种不接嵌入译文。
func tweetKey(r *linkreader.Result) string {
	if r == nil || r.ID == "" {
		return ""
	}
	return r.Platform + "\x00" + r.ID
}

// articleEmbeds 是长文正文里嵌的推文，按出现顺序；帖子自己引用的那条（r.Quote）不算“嵌在正文里”。
func articleEmbeds(r *linkreader.Result) []EmbedSource {
	var own *linkreader.Result
	if r.Quote.State == "available" {
		own = r.Quote.Value
	}
	seen := map[string]bool{}
	var out []EmbedSource
	for _, b := range r.Blocks {
		if b.Type != "quote" || b.Quote == nil || b.Quote == own {
			continue
		}
		key := tweetKey(b.Quote)
		if key == "" || seen[key] {
			continue
		}
		if text := ownText(b.Quote); strings.TrimSpace(text) != "" {
			seen[key] = true
			out = append(out, EmbedSource{Key: key, Text: text})
		}
	}
	return out
}

// dropEmbed 去掉和 key 相同的那条嵌入推文（它已经作为被引推文单独送了）。
func dropEmbed(list []EmbedSource, key string) []EmbedSource {
	if key == "" {
		return list
	}
	out := list[:0]
	for _, e := range list {
		if e.Key != key {
			out = append(out, e)
		}
	}
	return out
}

// withTranslations 在 Build 开头把宿主给的 Translations 接到旧的几个字段上（旧字段非空时优先），
// 并建好嵌入推文的译文表：被引推文自己的译文也按同一把键放进去，长文正文里那一条会原地换掉（ADR-391）。
func (o Options) withTranslations(r *linkreader.Result) Options {
	t := o.Translations
	if o.Translation == "" {
		o.Translation = t.Main
	}
	if o.QuoteTranslation == "" {
		o.QuoteTranslation = t.Quote
	}
	if o.ArticleTranslation == "" {
		o.ArticleTranslation = t.Article
	}
	if o.QuoteArticleTranslation == "" {
		o.QuoteArticleTranslation = t.QuoteArticle
	}
	m := make(map[string]Embedded, len(t.Embeds)+1)
	maps.Copy(m, t.Embeds)
	if q := quoteForTranslation(r); q != nil {
		if key := tweetKey(q); key != "" {
			if strings.TrimSpace(o.QuoteArticleTranslation) != "" && xArticle(q) {
				m[key] = Embedded{Text: o.QuoteArticleTranslation, Article: true}
			} else if strings.TrimSpace(o.QuoteTranslation) != "" {
				m[key] = Embedded{Text: o.QuoteTranslation}
			}
		}
	}
	if len(m) > 0 {
		o.embeds = m
	}
	return o
}

// embedFor 取 q 这条推文的嵌入译文（已经补回丢的链接）；没有或和原文一样返回假。
func (o Options) embedFor(q *linkreader.Result) (Embedded, bool) {
	key := tweetKey(q)
	if key == "" {
		return Embedded{}, false
	}
	e, ok := o.embeds[key]
	if !ok || strings.TrimSpace(e.Text) == "" {
		return Embedded{}, false
	}
	if e.Article {
		return e, true
	}
	own := ownText(q)
	text := keepOriginalLinks(strings.TrimSpace(e.Text), own)
	if text == strings.TrimSpace(own) {
		return Embedded{}, false
	}
	return Embedded{Text: text}, true
}

// translatedEmbed 是长文正文里嵌的推文换成译文后的副本和“翻译自……”：长文按 %% 分段填回，
// 普通推文换掉自己的正文；没有译文返回原样和空串。
func (o Options) translatedEmbed(q *linkreader.Result) (*linkreader.Result, string) {
	e, ok := o.embedFor(q)
	if !ok {
		return q, ""
	}
	if e.Article {
		if q2, good := articleTranslated(q, e.Text); good {
			return q2, articleFrom(q)
		}
		return q, ""
	}
	return withOwnText(q, e.Text), translatedFrom(q)
}

// withOwnText 返回把帖子自己正文换成 t 的副本（别的块原位不动，后面的正文块清空免得重复）；图片、引用留着。
func withOwnText(r *linkreader.Result, t string) *linkreader.Result {
	c := *r
	c.Text = linkreader.Field[string]{State: "available", Value: t}
	c.Blocks = append([]linkreader.Block(nil), r.Blocks...)
	placed := false
	for i := range c.Blocks {
		if c.Blocks[i].Type != "text" {
			continue
		}
		if !placed {
			c.Blocks[i].Text = t
			placed = true
			continue
		}
		c.Blocks[i].Text = ""
	}
	return &c
}

func usesAny(low string, tokens ...string) bool {
	for _, t := range tokens {
		if strings.Contains(low, t) {
			return true
		}
	}
	return false
}

// mainSource 是送去翻译的主推文：帖子自己的正文，后面接投票选项。
func mainSource(r *linkreader.Result) string { return withPollChoices(ownText(r), r) }

// withPollChoices 把投票的选项接在要翻译的正文后面，一个选项一行（用户 2026-11-12：原文和译文里都写选项；
// 2026-11-14 改成选项也只写译文：译文回来后 splitPollTranslation 按行拆出选项，投票块用译文选项加票数）。
// 只送选项名：票数、截止时间是我们拼的中文，留在原文那块，不交给模型改写。
func withPollChoices(text string, r *linkreader.Result) string {
	labels := pollLabelsToTranslate(r)
	if len(labels) == 0 {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return strings.Join(labels, "\n")
	}
	return text + "\n" + strings.Join(labels, "\n")
}

// pollLabelsToTranslate 是要送去翻译的选项名：只取不是空白的，按原顺序。
func pollLabelsToTranslate(r *linkreader.Result) []string {
	if r.Poll == nil {
		return nil
	}
	var labels []string
	for _, c := range r.Poll.Choices {
		if l := strings.TrimSpace(c.Label); l != "" {
			labels = append(labels, l)
		}
	}
	return labels
}

// splitPollTranslation 把主推文的译文拆成正文和投票选项：送出去的是“正文行加选项行”，模型被要求逐行译、行数不变，
// 所以译文的总行数要和送出去的一样，末尾那几行就是按顺序的选项。r 是清理过的结果（和送去翻译时同一份）。
// 返回的 labels 和 r.Poll.Choices 一一对应（空白选项留空串）。行数对不上（模型合并、拆开或丢了行）时 ok 为假：选项用原文，
// 正文只在译文行数比选项多时去掉末尾那几行（模型多半还是把选项放在末尾），否则整段译文不用（body 为空）。
// 没有投票或没有可译的选项时原样返回 body，ok 为真。
func splitPollTranslation(r *linkreader.Result, translated string) (body string, labels []string, ok bool) {
	want := pollLabelsToTranslate(r)
	t := strings.TrimSpace(translated)
	if len(want) == 0 {
		return t, nil, true
	}
	lines := strings.Split(t, "\n")
	n := len(want)
	if len(lines) == strings.Count(strings.TrimSpace(mainSource(r)), "\n")+1 && len(lines) >= n {
		tail := lines[len(lines)-n:]
		good := true
		for _, l := range tail {
			if strings.TrimSpace(l) == "" {
				good = false
			}
		}
		if good {
			labels = make([]string, len(r.Poll.Choices))
			k := 0
			for i, c := range r.Poll.Choices {
				if strings.TrimSpace(c.Label) != "" {
					labels[i] = strings.TrimSpace(tail[k])
					k++
				}
			}
			return strings.TrimSpace(strings.Join(lines[:len(lines)-n], "\n")), labels, true
		}
	}
	if len(lines) > n {
		return strings.TrimSpace(strings.Join(lines[:len(lines)-n], "\n")), nil, false
	}
	return "", nil, false
}

// withSplitTranslation 在 Build 开头用：把 Translation 拆成正文和投票选项译文，之后 o.Translation 只是正文。
func (o Options) withSplitTranslation(r *linkreader.Result) Options {
	if strings.TrimSpace(o.Translation) == "" {
		return o
	}
	body, labels, _ := splitPollTranslation(r, o.Translation)
	o.Translation, o.pollLabels, o.pollKey = body, labels, r.Poll
	return o
}

// pollLabelsFor 是这个投票的选项译文：只对主推文自己的投票生效。
func (o Options) pollLabelsFor(p *linkreader.PollInfo) []string {
	if p == nil || o.pollKey != p {
		return nil
	}
	return o.pollLabels
}

// untranslated 是不带投票选项译文的同一份设置：$原文 和“原文那一块”用它。
func (o Options) untranslated() Options {
	o.pollLabels, o.pollKey = nil, nil
	return o
}

// TranslationProblems 说这次拿到的译文哪几段用不上，原因类别不含用户内容；宿主拿去记日志，帖子照常发（用不上的那一段写原文）：
// poll_lines 投票选项的行数对不上（选项用原文）；article_sections 长文的段数对不上（整篇长文用原文）。
func TranslationProblems(r *linkreader.Result, o Options) []string {
	var out []string
	r = cleanResult(r, o)
	o = o.withTranslations(r)
	if strings.TrimSpace(o.Translation) != "" {
		if _, _, ok := splitPollTranslation(r, o.Translation); !ok {
			out = append(out, "poll_lines")
		}
	}
	if strings.TrimSpace(o.ArticleTranslation) != "" && xArticle(r) {
		if _, ok := articleTranslated(r, o.ArticleTranslation); !ok {
			out = append(out, "article_sections")
		}
	}
	if strings.TrimSpace(o.QuoteArticleTranslation) != "" {
		if q := quotedTweet(r); q != nil && xArticle(q) {
			if _, ok := articleTranslated(q, o.QuoteArticleTranslation); !ok {
				out = append(out, "quote_article_sections")
			}
		}
	}
	return out
}

// articleSegments 是长文里要翻的块的下标：文字块和小标题，按出现顺序。
func articleSegments(r *linkreader.Result) []int {
	var idx []int
	for i, b := range r.Blocks {
		if (b.Type == "text" || b.Type == "article_title") && strings.TrimSpace(b.Text) != "" {
			idx = append(idx, i)
		}
	}
	return idx
}

// articleSource 是送去翻译的长文：每个文字块、小标题一段，段与段之间 SectionMarker 单独一行。没有可翻的块是空串。
func articleSource(r *linkreader.Result) string {
	var parts []string
	for _, i := range articleSegments(r) {
		parts = append(parts, strings.TrimSpace(r.Blocks[i].Text))
	}
	return strings.Join(parts, "\n"+linktranslate.SectionMarker+"\n")
}

// articleTranslated 把长文译文按段填回每个文字块、小标题，图和分隔线留在原位，返回新结果（不改原结果）。
// 段数和原来对不上（模型合并、丢了分隔行）或有一段是空的，ok 为假，调用方整篇用原文：不凭猜测把译文对到某个块上。
// 原文里的链接译文里丢了的，补回那一段末尾（keepOriginalLinks）。
func articleTranslated(r *linkreader.Result, translated string) (*linkreader.Result, bool) {
	idx := articleSegments(r)
	if len(idx) == 0 {
		return r, false
	}
	var parts []string
	cur := []string{}
	for _, l := range strings.Split(strings.ReplaceAll(translated, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) == linktranslate.SectionMarker {
			parts = append(parts, strings.TrimSpace(strings.Join(cur, "\n")))
			cur = cur[:0]
			continue
		}
		cur = append(cur, l)
	}
	parts = append(parts, strings.TrimSpace(strings.Join(cur, "\n")))
	if len(parts) != len(idx) {
		return r, false
	}
	for _, p := range parts {
		if p == "" {
			return r, false
		}
	}
	c := *r
	c.Blocks = append([]linkreader.Block(nil), r.Blocks...)
	for k, i := range idx {
		c.Blocks[i].Text = keepOriginalLinks(parts[k], r.Blocks[i].Text)
	}
	return &c, true
}

// articleFrom 是长文的“翻译自日语”：按送去翻译的整篇原文判语种，认不出写“翻译自外语”。
func articleFrom(r *linkreader.Result) string {
	if lang := linktranslate.SourceLanguage(articleSource(r)); lang != "" {
		return "翻译自" + lang
	}
	return "翻译自外语"
}

// quotedTweet 是这条推文引用的那条推文：Blocks 里第一个 quote 块，没有就用 Quote 字段；没有引用或不可用返回 nil。xQuoteItems 和翻译用同一个取法。
func quotedTweet(r *linkreader.Result) *linkreader.Result {
	for _, blk := range r.Blocks {
		if blk.Type == "quote" && blk.Quote != nil {
			return blk.Quote
		}
	}
	if r.Quote.State == "available" {
		return r.Quote.Value
	}
	return nil
}

// quoteTranslated 是被引推文 q 的译文（已补回丢的链接）：没有译文或和原文一样返回空串。
// q 是被引的 X 长文时用 QuoteArticleTranslation 按段填回，卡片引用框写标题和正文（ADR-391）。
func (b *builder) quoteTranslated(q *linkreader.Result) string {
	if xArticle(q) && strings.TrimSpace(b.o.QuoteArticleTranslation) != "" {
		if q2, ok := articleTranslated(q, b.o.QuoteArticleTranslation); ok {
			if v := xOwnText(q2, b.o); v.ok {
				return v.text
			}
		}
		return ""
	}
	t := strings.TrimSpace(b.o.QuoteTranslation)
	own := ownText(q)
	if t == "" || strings.TrimSpace(own) == "" {
		return ""
	}
	t = keepOriginalLinks(t, own)
	if t == strings.TrimSpace(own) {
		return ""
	}
	return t
}

// stripTrailingHashtags 去掉文字结尾连续的 #标签 块（规格-链接发送改版 B6）：每个标签以 # 开头、由空白隔开，可以夹换行；
// 去完再去尾部的空格、空行。正文中间的标签不动；整段只有标签时不去（不然正文就空了）。去两遍和去一遍一样。
func stripTrailingHashtags(s string) string {
	end := len(s)
	cut := -1
	for {
		rest := strings.TrimRightFunc(s[:end], unicode.IsSpace)
		start := strings.LastIndexFunc(rest, unicode.IsSpace) + 1
		tok := rest[start:]
		if !isHashtag(tok) {
			break
		}
		cut, end = start, start
	}
	if cut < 0 {
		return s
	}
	out := strings.TrimRightFunc(s[:cut], unicode.IsSpace)
	if strings.TrimSpace(out) == "" {
		return s
	}
	return out
}

// isHashtag 说一段不含空白的字是不是一个 hashtag：# 或全角 ＃ 开头，后面至少有一个不是数字的字。
// 纯数字的 #1、#２０２６ 不算（X 自己也不把它们当 hashtag，“排行 #1”的 #1 是正文）。
func isHashtag(tok string) bool {
	for _, mark := range []string{"#", "＃"} {
		if rest, ok := strings.CutPrefix(tok, mark); ok {
			rest = strings.TrimLeft(rest, "#＃")
			return strings.IndexFunc(rest, func(r rune) bool { return !unicode.IsDigit(r) }) >= 0
		}
	}
	return false
}

// stripTweetHashtags 返回去掉了结尾 hashtag 的推文副本（不改原结果）：帖子自己最后一段文字和 Text 都去，被引推文同样处理。
// 展示、autoForward 的字数判断和送去翻译的原文都用这份。
func stripTweetHashtags(r *linkreader.Result) *linkreader.Result {
	c := *r
	if c.Text.State == "available" {
		c.Text.Value = stripTrailingHashtags(c.Text.Value)
	}
	c.Blocks = append([]linkreader.Block(nil), r.Blocks...)
	last := -1
	for i, b := range c.Blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			last = i
		}
		if b.Type == "quote" && b.Quote != nil {
			c.Blocks[i].Quote = stripTweetHashtags(b.Quote)
		}
	}
	if last >= 0 {
		c.Blocks[last].Text = stripTrailingHashtags(c.Blocks[last].Text)
	}
	if c.Quote.State == "available" && c.Quote.Value != nil {
		c.Quote.Value = stripTweetHashtags(c.Quote.Value)
	}
	return &c
}

// ownText 是帖子自己的正文：Blocks 里的 text 块按顺序，没有 text 块用 Text。不含引用、投票、作者行这些合成的内容。
func ownText(r *linkreader.Result) string {
	var parts []string
	for _, b := range r.Blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) == 0 && r.Text.State == "available" && strings.TrimSpace(r.Text.Value) != "" {
		parts = append(parts, r.Text.Value)
	}
	return strings.Join(parts, "\n")
}

// translationOnly 说这个站点有译文时是不是只出译文、不带原文：登记了翻译的非推文站点都是。
func translationOnly(key string) bool { return translatableSites[key] && key != "x" }

// cardTranslation 是推特卡片上要画的译文和它下面的小标记：卡片里有译文时只显示译文（用户 2026-11-13），
// 标记写“翻译自”加原文的语种（日语、韩语、英语……），认不出写“翻译自外语”。
// 只有 X 的卡片；纯文字消息的“原文 —— 译文”写法不动。投票选项是跟正文一起送去翻译的（withPollChoices），
// Build 开头已经把末尾的选项行拆走了（withSplitTranslation），这里的 Translation 只有正文；选项译文走 Item.PollLabels。没有译文返回空串。
func (b *builder) cardTranslation() (text, note string) {
	if b.site.Key != "x" {
		return "", ""
	}
	t := strings.TrimSpace(b.o.Translation)
	if t == "" {
		return "", ""
	}
	return t, translatedFrom(b.r)
}

// translatedFrom 是“翻译自日语”：原文语种认不出写“翻译自外语”，繁体写“翻译自繁体中文”。卡片和文字版共用。
// 语种按送去翻译的那份原文判（正文加投票选项）：只有投票没有正文的帖子也认得出。
func translatedFrom(r *linkreader.Result) string {
	if lang := linktranslate.SourceLanguage(mainSource(r)); lang != "" {
		return "翻译自" + lang
	}
	return "翻译自外语"
}

// translatedBody 是文字版（含合并转发里的推文节点）的 $正文文字 在有译文时的值（用户 2026-11-13：不是简体中文就只放译文，不留原文）：
// 帖子自己的正文那几块换成译文，原来夹在中间的别的文字（投票的票数、外链标题……）留在原位；投票选项的译文由投票块自己写（pollLabels）。
// 没有译文返回 none，调用方用原文。note 是“翻译自日语”。想要原文的自定义模板写 $原文。
func (b *builder) translatedBody() (body val, note string) {
	if b.site.Key != "x" || xArticle(b.r) { // 长文的正文是按段送翻译、按段填回的（articleTranslated），不走这里
		return none, ""
	}
	body, ok := translatedOwn(b.r, b.o, strings.TrimSpace(b.o.Translation))
	if !ok {
		return none, ""
	}
	return body, translatedFrom(b.r)
}

// translatedOwn 是帖子 r 的“正文文字”（xOwnText）把自己的正文换成译文 t 之后的值：译文放在第一块正文的位置，
// 别的文字（投票块、外链标题……）原样留在原位。没有自己的正文（纯图、只有投票）、没有译文、译文和原文一样时 ok 为假。
func translatedOwn(r *linkreader.Result, o Options, t string) (val, bool) {
	own := ownText(r)
	if t == "" || strings.TrimSpace(own) == "" {
		return none, false
	}
	t = keepOriginalLinks(t, own)
	if t == strings.TrimSpace(own) {
		return none, false // 译文和原文一样：没有可说的“翻译自”
	}
	ownBlocks := map[string]bool{}
	for _, blk := range r.Blocks {
		if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
			ownBlocks[blk.Text] = true
		}
	}
	if r.Text.State == "available" {
		ownBlocks[r.Text.Value] = true
	}
	var lines []string
	placed := false
	for _, it := range contentsOf(r, o, false, 0, false) {
		if it.Kind != types.BodyText || strings.TrimSpace(it.Text) == "" {
			continue
		}
		if ownBlocks[it.Text] {
			if !placed {
				lines = append(lines, t)
				placed = true
			}
			continue
		}
		lines = append(lines, it.Text)
	}
	if !placed {
		lines = append([]string{t}, lines...)
	}
	return some(strings.Join(lines, "\n")), true
}

// templateSources 是这个站点当前会用到的模板源码（小写）：自定义模板（含 @预设）解析得了就用它，否则用内置默认；
// 设了永远合并转发 / 永不合并转发的站点另一种形态用不到，不看它。X 长文走合并转发时内置用 XArticleForward，它只用 $正文，
// 本来就覆盖在“默认模板里有 $正文文字”之内（默认直发模板含它）。
func templateSources(key string, o Options) []string {
	site, _, ok := linktemplate.SiteByKey(key)
	if !ok {
		return nil
	}
	mode := o.Sites[key].Forward
	var out []string
	for _, forward := range []bool{false, true} {
		if forward && (!site.HasForward() || mode == linktemplate.ForwardNever) || !forward && mode == linktemplate.ForwardAlways && site.HasForward() {
			continue
		}
		cfgKey := key
		if forward {
			cfgKey += "-forward"
		}
		src := ""
		if cfg := o.Templates[cfgKey]; strings.TrimSpace(cfg) != "" {
			if resolved, err := site.Resolve(cfg); err == nil {
				if _, err := site.Parse(cfg); err == nil {
					src = resolved
				}
			}
		}
		if src == "" {
			src = site.Source(forward)
		}
		out = append(out, strings.ToLower(src))
	}
	return out
}

// keepOriginalLinks 补回译文里丢了的链接：只放译文时原文看不到了，原文里的链接译文里没有就一行一个接在末尾。
// 模型按提示词原样保留链接，这里只是兜底；补完再补一遍不变。
func keepOriginalLinks(translation, original string) string {
	out := translation
	for _, u := range asciiURLRe.FindAllString(original, -1) {
		u = strings.TrimRight(u, linkPunct)
		if u != "" && !strings.Contains(out, u) {
			out += "\n" + u
		}
	}
	return out
}

var linkTokenRe = regexp.MustCompile(`https?://\S+`)

// asciiURLRe 认链接：http(s):// 后面连着的可打印 ASCII；遇到空白或中日文就停（“见https://x.invalid/a，后面的字”不把后面的字吞进链接）。
var asciiURLRe = regexp.MustCompile(`https?://[!-~]+`)

// linkPunct 是紧贴在链接后面、不属于链接的标点（中英文句读、括号引号）。
const linkPunct = "。，、；：！？.,;:!?)）]】」』》〉”’\"'"

// dropRepeatedLinks 去掉译文里重复的链接（用户 2026-11-13，#36）：模型把链接原样保留，原文已经有了，「原文——————译文」里就出现两遍。
// 只处理原文里也有的链接：整行只有链接的那行去掉；链接在行尾的把链接去掉，该行还有别的字才保留（只剩标点就去掉整行），
// 句子中间的不动；末尾去掉的空行一并收掉。去两遍和去一遍一样。
func dropRepeatedLinks(translation, original string) string {
	have := map[string]bool{}
	for _, u := range linkTokenRe.FindAllString(original, -1) {
		have[strings.TrimRight(u, linkPunct)] = true
	}
	if len(have) == 0 {
		return translation
	}
	lines := strings.Split(translation, "\n")
	var out []string
	changed := false
	for _, line := range lines {
		cur := strings.TrimRightFunc(line, unicode.IsSpace)
		for {
			loc := linkTokenRe.FindAllStringIndex(cur, -1)
			if len(loc) == 0 {
				break
			}
			last := loc[len(loc)-1]
			if last[1] != len(cur) || !have[strings.TrimRight(cur[last[0]:], linkPunct)] {
				break
			}
			cur = strings.TrimRightFunc(cur[:last[0]], unicode.IsSpace)
			changed = true
		}
		if changed && strings.TrimSpace(cur) == "" && strings.TrimSpace(line) != "" {
			continue // 整行只剩链接：这一行不要了
		}
		out = append(out, cur)
	}
	if !changed {
		return translation
	}
	return strings.TrimRightFunc(strings.Join(out, "\n"), unicode.IsSpace)
}
