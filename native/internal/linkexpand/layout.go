package linkexpand

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 默认排版用到的几个字段和槽位：B 站视频的分 P 和时长说明，X 的短时间、正文文字、拼图、图片、视频。
// 排版选择来自主人在测试群里挑的结果（规格-链接解析模板“默认排版”一节）。

// explicitPart 说链接里有没有写 ?p=（展示链接和取数链接都保留 p 参数，所以看干净链接就够）。
func explicitPart(r *linkreader.Result) bool {
	u, err := url.Parse(r.CleanURL)
	if err != nil {
		return false
	}
	_, ok := u.Query()["p"]
	return ok
}

func multiPart(r *linkreader.Result) bool {
	return r.Parts.State == "available" && len(r.Parts.Value) > 1
}

// biliPartLine 是“P12/168：这一 P 的名字”；只有多 P 视频、且链接带了 ?p= 才有。
func biliPartLine(r *linkreader.Result) val {
	if !multiPart(r) || r.SelectedPart.State != "available" || !explicitPart(r) {
		return none
	}
	name := ""
	if t := optText(r.SelectedPart.Value.Title); t.ok {
		name = "：" + t.text
	}
	return some(fmt.Sprintf("P%d/%d%s", r.SelectedPart.Value.Number, len(r.Parts.Value), name))
}

// biliPartList 是不带 ?p= 的多 P 视频（不超过 10 个分 P）的分 P 列表，每行“P1 第一局（51:01）”。
func biliPartList(r *linkreader.Result) val {
	if !multiPart(r) || explicitPart(r) || len(r.Parts.Value) > 10 {
		return none
	}
	lines := make([]string, 0, len(r.Parts.Value))
	for _, p := range r.Parts.Value {
		line := fmt.Sprintf("P%d", p.Number)
		if t := optText(p.Title); t.ok {
			line += " " + t.text
		}
		if p.Duration.State == "available" {
			line += "（" + clock(p.Duration.Value) + "）"
		}
		lines = append(lines, line)
	}
	return some(strings.Join(lines, "\n"))
}

// biliLength 是信息行里的时长：单 P 写时长；带 ?p= 的多 P 写“本P 时长”；不带 p 的多 P 写“共 8P | 总长 …”。
func biliLength(r *linkreader.Result) val {
	if !multiPart(r) {
		return durationVal(r)
	}
	if explicitPart(r) {
		if d := durationVal(r); d.ok {
			return some("本P " + d.text)
		}
		return none
	}
	s := fmt.Sprintf("共 %dP", len(r.Parts.Value))
	total := 0
	for _, p := range r.Parts.Value {
		if p.Duration.State != "available" {
			return some(s)
		}
		total += p.Duration.Value
	}
	return some(s + " | 总长 " + clock(total))
}

// biliTotal 是多 P 视频（没指定第几 P）的总长；任何一 P 没有时长、或不是多 P 就没有。
func biliTotal(r *linkreader.Result) (string, bool) {
	if !multiPart(r) || explicitPart(r) {
		return "", false
	}
	total := 0
	for _, p := range r.Parts.Value {
		if p.Duration.State != "available" {
			return "", false
		}
		total += p.Duration.Value
	}
	return clock(total), true
}

// shortStamp 是“11-07 23:28”：今年的不写年份，往年的写“2025-11-07 23:28”（同 stamp，取不到时是没有）。
func shortStamp(f linkreader.Field[time.Time]) val {
	if f.State != "available" {
		return none
	}
	return some(stamp(f))
}

// isBareURL 说这段文字整个就是一条链接（没有别的字）。
func isBareURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) && !strings.ContainsAny(s, " \t\r\n")
}

// xBodyText 是 X 帖子自己正文里的文字部分（正文、投票、外链……按原顺序，不含图和视频，也不含被引推文：那个在 $被引推文）。
func (b *builder) xBodyText() val {
	return xOwnText(b.r, b.o)
}

func xOwnText(r *linkreader.Result, o Options) val {
	var lines []string
	for _, it := range contentsOf(r, o, false, 0, false) {
		if it.Kind == types.BodyText && strings.TrimSpace(it.Text) != "" {
			lines = append(lines, it.Text)
		}
	}
	if len(lines) == 0 {
		return none
	}
	return some(strings.Join(lines, "\n"))
}

// postSite 说这个站点的帖子用推文那一套排法（规格-链接发送改版 B10 第 5 条）：Bluesky、Misskey / Mastodon、Telegram。
func postSite(key string) bool { return key == "bluesky" || key == "misskey" || key == "telegram" }

// postText 是这类帖子的正文文字：正文、引用的帖子（“引用 名字 @账号：”加它的文字）、外链卡标题、投票……按原顺序，不含图和视频。
func postText(r *linkreader.Result, o Options) val {
	var lines []string
	for _, it := range contents(r, o, false, 0) {
		if it.Kind == types.BodyText && strings.TrimSpace(it.Text) != "" {
			lines = append(lines, it.Text)
		}
	}
	if len(lines) == 0 {
		return none
	}
	return some(strings.Join(lines, "\n"))
}

// postMediaItems 是这类帖子的图和动图（id 为 images）或视频（id 为 video）；直发时视频各占一条消息，动图和图一样跟文字同一条（用户 2026-11-13）。
func (b *builder) postMediaItems(id string) []Item {
	var out []Item
	for _, it := range contents(b.r, b.o, true, 0) {
		switch {
		case it.Kind == types.BodyText:
		case it.Kind == types.BodyVideo:
			if id == "video" {
				it.Standalone = !b.forwd
				out = append(out, it)
			}
		case id == "images":
			out = append(out, it)
		}
	}
	if id == "images" && len(out) == 0 {
		if m, ok := profileAvatar(b.r); ok {
			it := mediaItem(m, false)
			it.Optional = true
			out = append(out, it)
		}
	}
	return out
}

// xOwnImages 是帖子自己的图（不含引用里的图、外链卡的图）。
func xOwnImages(r *linkreader.Result) []linkreader.Media {
	var out []linkreader.Media
	for _, m := range r.Media.Value {
		if (m.Type == "image" || m.Type == "photo") && sendable(m) {
			out = append(out, m)
		}
	}
	return out
}

// artistEligible 说这条 X 推文能按画师的发法发：调用方给了发法（空串就是不特殊处理）、是推文（不是 X 文章）、
// 有自己的至少一张图。纯文字、只有视频、只有外链卡的图、只有被引推文的图都不算。
func artistEligible(r *linkreader.Result, mode string) bool {
	if mode != ArtistCardAndOriginals && mode != ArtistOriginalsOnly {
		return false
	}
	if r.Kind != "post" || r.Article.State == "available" {
		return false
	}
	return len(xOwnImages(r)) > 0
}

// xUseMosaic 说这条帖子发拼图：直发模板写了 $拼图（内置默认不写）、直发（不进合并转发）、自己有三张及以上的图、没有视频、取数端给了拼图。
// 拼图取不到（vx 线路、旧缓存）就不发拼图，autoForward 会把这样的多图帖子交给合并转发。
func (b *builder) xUseMosaic() bool {
	return b.mosaic && !b.forwd && xMosaicUsable(b.r) && panoramaCandidate(b.r) == nil
}

// pixivFirstLine 是 pixiv 作品的首行（规格-链接发送改版 B7）：“[AI]🔞 | 标题 | 作者名”。[AI] 只在 AI 生成时有、
// 🔞 只在 R18（含 R18-G）时有，两个标记之间不加分隔；取不到的部分连同它的 “ | ” 一起省掉，都取不到就没有这一行。
// 在代码里合成而不用模板的可选段拼：标记之间没有分隔符、和标题之间的 “ | ” 只在前面有标记时才要。
func pixivFirstLine(r *linkreader.Result) val {
	marks := ""
	if ai, ok := flagBool(r, "ai_generated"); ok && ai {
		marks += "[AI]"
	}
	if level, ok := Restriction(r); ok && level > 0 {
		marks += "🔞"
	}
	var parts []string
	for _, s := range []string{marks, optText(r.Title).text, optText(r.Author.Name).text} {
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return none
	}
	return some(strings.Join(parts, " | "))
}

// xArticle 说这是不是 X 文章（长文）。
func xArticle(r *linkreader.Result) bool {
	return r.Article.State == "available" || r.Kind == "article"
}

// xAccount 是不带 @ 的账号（取数线路有的带 @、有的不带，统一在这里去掉）。
func xAccount(r *linkreader.Result) val {
	if a := optText(r.Author.Account); a.ok {
		if s := strings.TrimLeft(strings.TrimSpace(a.text), "@＠"); s != "" {
			return some(s)
		}
	}
	return none
}

// tweetText 是推文文字版的头和正文（规格-链接发送改版 B2）：链接一行、“名字[@ID]：”一行、「正文」（带译文时
// 「原文\n——————\n中文」）；正文为空时那一行省掉。内置模板对主推文的写法与它一致，这里给被引推文用。
func tweetText(r *linkreader.Result, o Options) string { return tweetTextWith(r, o, "", "") }

// tweetTextWith 同 tweetText；translated 非空时帖子自己的正文换成译文（别的文字原位不动），并在「正文」下面写一行“（note）”。
// 被引推文有译文时用（2026-11-14）：只放译文，不留原文。
func tweetTextWith(r *linkreader.Result, o Options, translated, note string) string {
	lines := []string{r.ShareURL()}
	who := ""
	if n := optText(r.Author.Name); n.ok {
		who = n.text
	}
	if a := xAccount(r); a.ok {
		who += "[@" + a.text + "]"
	}
	body := xOwnText(r, o)
	if translated != "" {
		if tb, ok := translatedOwn(r, o, translated); ok {
			body = tb
		} else {
			note = ""
		}
	}
	if who != "" {
		if body.ok {
			who += "："
		}
		lines = append(lines, who) // 没有正文时不挂冒号（用户 2026-11-13）
	}
	if body.ok {
		lines = append(lines, "「"+body.text+"」")
		if note != "" {
			lines = append(lines, "（"+note+"）")
		}
	}
	if when := shortStamp(r.Published); when.ok {
		lines = append(lines, when.text) // 发帖时间在「正文」下面，同主推文（用户 2026-11-12）
	}
	return strings.Join(lines, "\n")
}

// xQuoteItems 是 $被引推文：被引推文的文字（格式同主推文）和它的图、视频；被引推文不可用时是一句说明，
// 更深的引用只写“还有更深引用，看原文”，紧跟被引推文的文字（同一个节点，在它的图和视频前面）。
func (b *builder) xQuoteItems() []Item {
	r := b.r
	q := quotedTweet(r)
	gone := ""
	for _, blk := range r.Blocks {
		if blk.Type == "quote_unavailable" && gone == "" {
			gone = tombstoneText(blk.Reason)
		}
	}
	var out []Item
	switch {
	case q != nil:
		b.quotedTweet = true
		note, qt := "", ""
		shown := q
		if xArticle(q) && strings.TrimSpace(b.o.QuoteArticleTranslation) != "" {
			// 被引的是 X 长文：标题和正文块照主长文那样按 %% 分段填回，原文不出现（ADR-391）
			if q2, ok := articleTranslated(q, b.o.QuoteArticleTranslation); ok {
				shown, note = q2, articleFrom(q)
			}
		}
		if shown == q {
			if qt = b.quoteTranslated(q); qt != "" {
				note = translatedFrom(q)
			}
		}
		out = append(out, text(tweetTextWith(shown, b.o, qt, note)))
		if q.MoreQuotes.Value || q.Quote.State == "available" {
			// 紧跟被引推文的文字，在它的图和视频前面：放在媒体后面会夹在被引推文和主推文之间，读起来像是在说主推文（新 2-3 X-21）
			out = append(out, text("还有更深引用，看原文"))
		}
		for _, it := range contentsOf(shown, b.o, true, 1, false) {
			if it.Kind == types.BodyText {
				continue
			}
			if it.Kind == types.BodyVideo { // QQ 的视频消息不能带文字；动图在 QQ 里就是图，跟文字同一条
				it.Standalone = !b.forwd
			}
			out = append(out, it)
		}
	case gone != "":
		out = append(out, text(gone))
	case r.Quote.State == "failed":
		// 取数失败不再让整条进合并转发（classifyX 删除后统一按 autoForward）；直发时也要说明引用的那条没取到。
		out = append(out, text("引用的推文没取到，看原文"))
	case r.MoreQuotes.Value:
		out = append(out, text("还有更深引用，看原文"))
	}
	return out
}

func xMosaicUsable(r *linkreader.Result) bool {
	if r.Mosaic.State != "available" || r.Mosaic.Value.URL.State != "available" || len(xOwnImages(r)) < 3 {
		return false
	}
	for _, m := range r.Media.Value {
		if m.Type == "video" || m.Type == "gif" {
			return false
		}
	}
	return true
}

// xMediaItems 是 X 默认排版里的 $拼图、$图片、$视频：拼图单独一个槽位放在文字上面；图片是其余的图，放在文字下面；
// 视频和动图在直发时各自独立成一条消息。用了拼图，帖子自己的图就不再重复发。
func (b *builder) xMediaItems(id string) []Item {
	r := b.r
	if id == "mosaic" {
		if b.xUseMosaic() {
			return []Item{mediaItem(r.Mosaic.Value, false)}
		}
		return nil
	}
	skip := map[string]bool{}
	if b.xUseMosaic() {
		for _, m := range xOwnImages(r) {
			skip[m.URL.Value] = true
		}
	}
	var out []Item
	for _, it := range contentsOf(r, b.o, true, 0, false) {
		switch {
		case it.Kind == types.BodyText:
		case it.Kind == types.BodyVideo:
			if id == "video" {
				it.Standalone = !b.forwd
				out = append(out, it)
			}
		case id == "images" && !skip[it.Media.URL.Value]:
			out = append(out, it)
		}
	}
	if id == "images" && b.artist == "" { // 画师的推文：拆开的几张就发几张原图，不拼回
		b.markPanorama(out)
	}
	return out
}
