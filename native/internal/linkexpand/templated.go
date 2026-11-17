package linkexpand

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
	"sumika-native/internal/linktrunc"
)

// SiteSetting 是一个站点的发送设置（配置里 linkReader.expand.sites.<站点>）。
// Forward 为空按 auto；ForwardOver、DescLimit 为 nil 用站点的内置默认。
type SiteSetting struct {
	Forward     string
	ForwardOver *int
	DescLimit   *int
	// ShowDesc 为真时内置默认模板带上简介那一行（用户 2026-11-13，R-219）；用户自己写的模板不受它影响
	ShowDesc bool
}

type val struct {
	text string
	ok   bool
}

func some(s string) val { return val{s, true} }

var none = val{}

func optText(f linkreader.Field[string]) val {
	if f.State == "available" && f.Value != "" {
		return some(f.Value)
	}
	return none
}

// meaningfulText 是 optText 的严格版：全是标点、横线的内容（B 站简介常见的“-”）当没有。
func meaningfulText(f linkreader.Field[string]) val {
	v := optText(f)
	if v.ok && !strings.ContainsFunc(v.text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return none
	}
	return v
}
func optNumber(f linkreader.Field[int64]) val {
	if f.State == "available" {
		return some(strconv.FormatInt(f.Value, 10))
	}
	return none
}

// optCompact 是统计数字的缩写写法（1.08亿、297.5万），B 站视频和 X 用。
func optCompact(f linkreader.Field[int64]) val {
	if f.State == "available" {
		return some(compact(f.Value))
	}
	return none
}
func optStamp(f linkreader.Field[time.Time]) val {
	if f.State == "available" {
		return some(stamp(f))
	}
	return none
}

// builder 把一个 Result 按站点模板排成消息计划。
type builder struct {
	r     *linkreader.Result
	o     Options
	site  linktemplate.Site
	forwd bool
	vals  map[string]val
	desc  int
	// showDesc：内置默认模板带简介那一行（SiteSetting.ShowDesc）
	showDesc bool
	images   []Item
	loaded   bool
	// mosaic：直发模板里写了 $拼图（内置默认不写，B3 后直发最多 3 张、图在文字下面；自定义模板写了才发拼图）
	mosaic bool
	// quotedTweet：$被引推文 出的是一条真的被引推文（不是“被引推文不可用”的说明），转发里主推文节点要标 QuoteOf
	quotedTweet bool
	// artist：命中确认过的画师、且这条推文有自己的图（规格-画师原图）：用画师模板、不进 autoForward、拆图不拼回；空串是旧行为
	artist string
	// moreImages：图列不全时写在整条末尾的那句（爱发电动态最多列 10 张）
	moreImages string
	// articleNote：X 长文的正文已经换成译文时的“翻译自日语”（$翻译说明）；空串是没换
	articleNote string
}

// Build 只构建语义计划。媒体槽位由后续受限下载器准备，再由唯一 Outlet 发送。
// 每个站点的排版由模板决定（规格-链接解析模板）：字段、图片位置、分段和合并转发都在模板里；
// 合并转发一层、80 节点封顶、文字 2000 字拆节点这些边界由代码保证。
func Build(r *linkreader.Result, quote string, o Options, previous int) Draft {
	d := Draft{CleanURL: r.ShareURL()}
	if !Supports(r.Platform, r.Kind) {
		return d
	}
	if !hasContent(r) {
		d.NoContent = true
		return d
	}
	footer := ""
	if previous > 0 {
		footer = fmt.Sprintf("发过 %d 次", previous)
	}
	key := linktemplate.SiteKey(r.Platform, r.Kind)
	site, _, _ := linktemplate.SiteByKey(key)
	r = cleanResult(r, o)
	o = o.withSplitTranslation(r) // 译文末尾的投票选项行拆走：Translation 只剩正文，选项译文放 pollLabels
	articleNote := ""
	if key == "x" && strings.TrimSpace(o.ArticleTranslation) != "" && xArticle(r) {
		// X 长文的译文按段填回每个文字块和小标题，图和分隔线留在原位；原文不出现（2026-11-14）。段数对不上整篇用原文
		if tr, ok := articleTranslated(r, o.ArticleTranslation); ok {
			articleNote = articleFrom(r)
			r = tr
		}
	}
	b := &builder{r: r, o: o, site: site, showDesc: o.Sites[key].ShowDesc, articleNote: articleNote}
	if key == "x" && artistEligible(r, o.Artist) {
		b.artist = o.Artist
	}
	b.mosaic = key == "x" && b.template(key).Uses("mosaic")
	setting := o.Sites[key]
	over, desc := site.ForwardOver, site.DescLimit
	if setting.ForwardOver != nil {
		over = *setting.ForwardOver
	}
	if setting.DescLimit != nil {
		desc = *setting.DescLimit
	}
	b.desc = desc
	byImages := false                        // 只因为图多才进合并转发（shape.go 第 4 条）
	if site.HasForward() && b.artist == "" { // 画师的推文只直发，图太多由 shapeDirect 按通用规则改转发或截断
		switch setting.Forward {
		case linktemplate.ForwardAlways:
			b.forwd = true
		case linktemplate.ForwardNever:
		default:
			b.forwd, byImages = b.autoForward(key, over)
		}
	}
	tpl := b.template(key)
	d.Parts = b.assemble(tpl.Render(b), quote)
	limit := o.directImageMax()
	shapeDirect(&d, limit, setting.Forward != linktemplate.ForwardNever)
	if byImages {
		for i := range d.Parts {
			if d.Parts[i].Kind == types.PartForward {
				d.Parts[i].fitDirect, d.Parts[i].imageCap = true, limit
			}
		}
	}
	appendPanoramaOriginals(&d)
	if !o.ForwardVideo {
		splitForwardVideos(&d)
	}
	if note := incomplete(r); note != "" {
		footer = join(footer, note)
	}
	if b.moreImages != "" {
		footer = join(footer, b.moreImages)
	}
	if footer != "" {
		appendNote(&d, footer, quote)
	}
	capNodes(&d)
	applyForward(&d, r, o.Forward)
	return d
}

// AppendNote 把一句话（“还有 N 条没处理”这类）接在草稿的第一条后面，规则同 Build 里的“发过 N 次”：
// 第一段是合并转发就接在第一个节点的文字后面——转发段只发 Nodes，写进它的 Body 会被静默丢掉。
func (d *Draft) AppendNote(s string) {
	quote := ""
	if len(d.Parts) > 0 {
		quote = d.Parts[0].Quote
	}
	appendNote(d, s, quote)
}

// appendNote 追加到第一条：第一段是合并转发（内置转发模板不再另发前导句，规格-链接发送改版 B4）就接在第一个节点的文字后面，
// 不为它另发一条消息；第一段不是文字消息就在前面补一条带 quote 的。
func appendNote(d *Draft, s, quote string) {
	switch {
	case len(d.Parts) > 0 && d.Parts[0].Kind == types.PartForward && len(d.Parts[0].Nodes) > 0:
		appendToForward(&d.Parts[0], s)
	default:
		if len(d.Parts) == 0 || d.Parts[0].Kind != types.PartMessage {
			d.Parts = append([]Part{{Kind: types.PartMessage, Quote: quote}}, d.Parts...)
		}
		d.Parts[0].Body = append(d.Parts[0].Body, text(s))
	}
}

// markNoteEnd 在头部那组拆出的节点里标出提示该接的位置：开头连续的纯文字节点的最后一个（正文拆成几个节点时是正文的末尾，
// 在图前面）；节点是文字起头、后面跟着图的（图文混排），就标这个节点，提示接在它开头那段文字后面；开头就是图就标第一个节点
func markNoteEnd(nodes []Node) {
	at := 0
	for i, n := range nodes {
		if len(n.Body) == 0 || n.Body[0].Kind != types.BodyText {
			break
		}
		at = i
		if len(n.Body) != 1 {
			break
		}
	}
	if len(nodes) > 0 {
		nodes[at].noteEnd = true
	}
}

// noteText 是提示该接在节点里的哪一项后面：节点开头那一段连续的文字的最后一项（图文混排时在图前面）；
// 开头不是文字就取最后一项文字；没有文字返回 -1
func noteText(n *Node) int {
	at := -1
	for i, it := range n.Body {
		if it.Kind != types.BodyText {
			break
		}
		at = i
	}
	if at >= 0 {
		return at
	}
	for i := len(n.Body) - 1; i >= 0; i-- {
		if n.Body[i].Kind == types.BodyText {
			return i
		}
	}
	return -1
}

// appendToForward 把提示接在转发头部那组的末尾（没标过就是第一个节点）；接上后超过 2000 字就在它后面另起一个节点，
// 后面节点的 QuoteOf 跟着挪，标记移到新节点上（下一句提示接在它后面）
func appendToForward(p *Part, s string) {
	at := 0
	for i, n := range p.Nodes {
		if n.noteEnd {
			at = i
			break
		}
	}
	n := &p.Nodes[at]
	if t := noteText(n); t < 0 || nodeTextRunes(n)+1+utf8.RuneCountInString(s) <= 2000 {
		appendToNode(n, s)
		return
	}
	extra := Node{Name: n.Name, Fixed: n.Fixed, Body: []Item{text(s)}, noteEnd: true}
	n.noteEnd = false
	p.Nodes = slices.Insert(p.Nodes, at+1, extra)
	for i := range p.Nodes {
		if p.Nodes[i].QuoteOf > at+1 {
			p.Nodes[i].QuoteOf++
		}
	}
}

// appendToNode 把一句话接在节点开头那段文字后面（另起一行，图文混排时在图前面）；节点里没有文字（只有图）就在末尾加一段文字。
func appendToNode(n *Node, s string) {
	if t := noteText(n); t >= 0 {
		n.Body[t].Text = join(n.Body[t].Text, s)
		return
	}
	n.Body = append(n.Body, text(s))
}

// nodeTextRunes 数节点里文字的总字数（一个节点的文字最多 2000 字）
func nodeTextRunes(n *Node) int {
	total := 0
	for _, it := range n.Body {
		if it.Kind == types.BodyText {
			total += utf8.RuneCountInString(it.Text)
		}
	}
	return total
}

// template 取这个站点这一形态的模板：配置里的（可以写 @预设名），写错了或没写用内置默认。
func (b *builder) template(key string) *linktemplate.Template {
	if b.artist != "" {
		src := linktemplate.XArtistCardOriginals
		if b.artist == ArtistOriginalsOnly {
			src = linktemplate.XArtistOriginalsOnly
		}
		tpl, err := linktemplate.Parse(src, b.site.Set)
		if err != nil {
			panic("linkexpand: 画师模板写错了：" + err.Error())
		}
		return tpl
	}
	cfgKey := key
	if b.forwd {
		cfgKey += "-forward"
	}
	if src := b.o.Templates[cfgKey]; strings.TrimSpace(src) != "" {
		if tpl, err := b.site.ParseWith(src, b.showDesc); err == nil {
			return tpl
		}
	}
	src := b.site.SourceWith(b.forwd, b.showDesc)
	if key == "x" && b.forwd && xArticle(b.r) {
		src = linktemplate.XArticleForward
	}
	tpl, err := linktemplate.Parse(src, b.site.Set)
	if err != nil {
		panic("linkexpand: 内置模板写错了：" + err.Error())
	}
	return tpl
}

// autoForward 是 auto 下“这条帖子走直发还是合并转发”的代码规则。图数上限各站统一是 directImageMax
// （默认 3，规格-链接发送改版 B3），其余判断保持重构前的样子。byImages：除了图多，别的条件都够直发
// （Prepare 后图没那么多就收回成一条消息，shape.go 第 4 条）。
func (b *builder) autoForward(key string, over int) (forward, byImages bool) {
	r := b.r
	maxImages := b.o.directImageMax()
	switch key {
	case "bilibili-dynamic":
		short, images := b.dynamicShort(over)
		return !short || images > maxImages, short && images > maxImages
	case "bilibili-article":
		// 短的（正文不超过 over 字、图不超过上限、没有视频）直接发（用户 2026-11-12：一句话加一两张图不进转发）；over 为 0 不按字数（同 B 站动态；想一律转发用 forward: always）
		short, images := b.articleShort(over)
		return !short || images > maxImages, short && images > maxImages
	case "douyin-note", "pixiv-novel":
		return true, false
	case "pixiv-artwork":
		pages := int64(0)
		if r.Stats["pages"].State == "available" && r.Media.State == "available" {
			pages = r.Stats["pages"].Value // 页数比取到的多（只取到第一页）时按页数；图都没取到就按 0 张（P-10）
		}
		if pages == 0 {
			pages = int64(len(b.pixivImages()))
		}
		return pages > int64(maxImages), pages > int64(maxImages)
	case "telegram":
		// 文字不超过 over 字、图不超过 directImageMax 张、至多 1 个视频且不和图混着，才一条消息直发；其余进合并转发
		images, videos := 0, 0
		for _, m := range r.Media.Value {
			switch m.Type {
			case "image", "photo":
				images++
			case "video", "gif":
				videos++
			}
		}
		rest := (over <= 0 || WeightedLength(r.Text.Value) <= over) && !r.Media.Truncated && videos <= 1 && !(images > 0 && videos > 0)
		return !rest || images > maxImages, rest && images > maxImages
	case "x":
		images, videos := 0, 0
		hasQuote := r.Quote.State == "available" || r.MoreQuotes.Value
		for _, m := range r.Media.Value {
			if m.Type == "image" || m.Type == "photo" {
				images++
			}
			if m.Type == "video" || m.Type == "gif" {
				videos++
			}
		}
		for _, blk := range r.Blocks {
			if blk.Type == "quote" {
				hasQuote = true
			}
		}
		if r.LinkCard.State == "available" && r.LinkCard.Value.Image.State == "available" {
			images++
		}
		if cand := panoramaCandidate(r); cand != nil {
			images -= len(cand) - 1 // 可能是拆开的横幅，拼回后只是一张图
		} else if b.mosaic && xMosaicUsable(r) && len(xOwnImages(r)) >= 3 {
			images -= len(xOwnImages(r)) // 自定义模板写了 $拼图：三张及以上的图发拼图，一条消息就够
		}
		rest := r.Expansion.State == "available" && r.Expansion.Value == "short" && r.Text.State == "available" && !r.Text.Truncated && r.Media.State == "available" && !r.Media.Truncated &&
			(over <= 0 || WeightedLength(r.Text.Value) <= over) && videos <= 1 && !hasQuote && r.Article.Reason == "not_applicable" && !(images > 0 && videos > 0)
		return !rest || images > maxImages, rest && images > maxImages
	}
	return false, false
}

// articleShort 说一篇 B 站专栏够不够短：正文没被截断、文字不超过 over 字（over 为 0 不按字数）、没有视频；images 是正文里的图数。
func (b *builder) articleShort(over int) (short bool, images int) {
	r := b.r
	if r.Text.Truncated {
		return false, 0
	}
	runes := 0
	for _, it := range contents(r, b.o, true, 0) {
		switch {
		case it.Kind == types.BodyText:
			runes += utf8.RuneCountInString(it.Text)
		case visualItem(it):
			images++
		default:
			return false, images
		}
	}
	return over <= 0 || runes <= over, images
}

// dynamicImages 是 B 站动态里的图，按原顺序（动图、视频不在里面）。
func (b *builder) dynamicImages() []Item {
	var out []Item
	for _, it := range contents(b.r, b.o, true, 0) {
		if it.Kind == types.BodyImage {
			out = append(out, it)
		}
	}
	return out
}

// dynamicExtras 是 B 站动态里图和正文文字以外的内容：视频（直发时各自一条消息）、动图（跟文字同一条）、引用、投票等。
// 直发模板带上它，站点设成 never 时这些内容才不会丢。已在 $正文文字 里的文字不重复。
func (b *builder) dynamicExtras() []Item {
	var out []Item
	own := map[string]bool{} // 正文文字块，由 $正文文字 管
	for _, blk := range b.r.Blocks {
		if blk.Type == "text" {
			own[blk.Text] = true
		}
	}
	for _, it := range contents(b.r, b.o, true, 0) {
		switch it.Kind {
		case types.BodyImage:
			continue
		case types.BodyText:
			if b.r.Text.State == "available" && strings.Contains(b.r.Text.Value, it.Text) || own[it.Text] {
				continue
			}
		case types.BodyVideo: // 动图跟文字同一条（用户 2026-11-13）
			it.Standalone = !b.forwd
		}
		out = append(out, it)
	}
	return out
}

// dynamicShort 说一条 B 站动态除了图数以外够不够短：正文没被截断且不超过 over 字（over 为 0 不按字数）、
// 没有视频、动图、引用、投票等别的内容；images 是图数，不超过 directImageMax 张（各站统一，R-201）才一条消息直发。
func (b *builder) dynamicShort(over int) (short bool, images int) {
	r := b.r
	if r.Text.State == "available" && (r.Text.Truncated || (over > 0 && utf8.RuneCountInString(r.Text.Value) > over)) {
		return false, 0
	}
	if r.Quote.State == "available" || r.MoreQuotes.Value || r.Poll != nil {
		return false, 0
	}
	for _, it := range contents(r, b.o, true, 0) {
		switch it.Kind {
		case types.BodyText:
		case types.BodyImage:
			images++
		default:
			return false, images
		}
	}
	return true, images
}

// Text 和 Slot 实现 linktemplate.Env。
func (b *builder) Text(id string) (string, bool) {
	if b.vals == nil {
		b.vals = b.fields()
	}
	v, ok := b.vals[id]
	if (id == "bodytext" || id == "text") && translationOnly(b.site.Key) && strings.TrimSpace(b.o.Translation) != "" {
		return "", true // 非推文站点有译文时只出译文（B5）：值为空，可选段照样省掉，裸写的占位符输出空串、不写“未返回”
	}
	if ok && v.ok && b.desc > 0 && (id == "text" || id == "desc") {
		return linktrunc.Body(v.text, b.desc), true // 简介截断字数对所有“简介”字段通用（截在最近的标点，补“……”和“完整内容见原文”）
	}
	return v.text, ok && v.ok
}

func (b *builder) Slot(id string) bool { return len(b.slotItems(id)) > 0 }

func (b *builder) fields() map[string]val {
	r := b.r
	v := map[string]val{"url": some(r.ShareURL())} // 推文按取到的作者账号写链接（ShareURL）
	set := func(id string, x val) { v[id] = x }
	stat := func(id, key string) { set(id, optCompact(r.Stats[key])) }
	switch b.site.Key {
	case "bilibili-video":
		set("site", some("B站视频"))
		set("when", shortStamp(r.Published))
		set("title", optText(r.Title))
		set("part", biliPartLine(r))
		set("partlist", biliPartList(r))
		set("author", optText(r.Author.Name))
		set("published", optStamp(r.Published))
		set("duration", durationVal(r))
		set("length", biliLength(r))
		set("desc", meaningfulText(r.Text))
		set("upower", upowerVal(r))
		set("hot", commentsVal(r))
		for id, key := range map[string]string{"views": "view", "favorites": "favorite", "shares": "share", "comments": "reply", "danmaku": "danmaku", "likes": "like", "coins": "coin"} {
			set(id, optCompact(r.Stats[key]))
		}
	case "bilibili-mall":
		set("site", some("会员购"))
		set("title", optText(r.Title))
		set("details", some(strings.Join(r.Mall.Details(), "\n")))
		set("published", optStamp(r.Published))
	case "bilibili-live":
		set("site", some("B站直播间"))
		set("title", optText(r.Title))
		set("author", optText(r.Author.Name))
		set("area", optText(r.Category))
		if s := liveStatus(r); s != "" {
			set("status", some(s))
		}
		set("published", optStamp(r.Published))
		set("desc", optText(r.Text))
		if r.Tags.State == "available" && len(r.Tags.Value) > 0 {
			set("tags", some(strings.Join(r.Tags.Value, "、")))
		}
		if n := r.Stats["online"]; n.State == "available" && n.Value > 0 {
			set("online", optCompact(n))
		}
		// 直播间的“关注”就是主播粉丝数，取数端只给 followers；$关注 留着当 $粉丝 的别名，用户以前写进模板的照样有数
		stat("attention", "followers")
		stat("followers", "followers")
	case "bilibili-space":
		set("site", some("B站个人主页"))
		set("title", optText(r.Title))
		set("desc", optText(r.Text))
		set("official", optText(r.Category))
		stat("followers", "followers")
		stat("following", "following")
		stat("videos", "videos")
		stat("likes", "likes")
	case "bilibili-dynamic":
		set("site", some("B站动态"))
		set("title", optText(r.Title))
		set("text", optText(r.Text)) // 有图时的“分享图片”已在 cleanResult 去掉
		set("author", optText(r.Author.Name))
		set("published", optStamp(r.Published))
		stat("likes", "like")
		stat("comments", "comment")
		stat("shares", "forward")
	case "bilibili-toy", "bilibili-workshop", "bilibili-article", "bilibili-bangumi", "bilibili-course", "bilibili-audio", "bilibili-collection":
		b.biliMoreFields(v)
	case "x":
		label := "X 推文"
		if xArticle(r) {
			label = "X 文章"
		}
		set("site", some(label))
		// $账号 不带 @（有的线路给的带 @，在这里去掉）；$作者 仍是“名字 @账号”
		acc := xAccount(r)
		if name := optText(r.Author.Name); name.ok && acc.ok {
			set("author", some(name.text+" @"+acc.text))
		} else if name.ok {
			set("author", name)
		} else if acc.ok {
			set("author", some("@"+acc.text))
		}
		set("name", optText(r.Author.Name))
		set("account", acc)
		set("published", optStamp(r.Published))
		set("text", optText(r.Text))
		for id, key := range map[string]string{"likes": "likes", "retweets": "reposts", "replies": "replies", "quotes": "quotes", "bookmarks": "bookmarks", "views": "views"} {
			set(id, optCompact(r.Stats[key]))
		}
		if s := xStats(r, textSep); s != "" {
			set("stats", some(s))
		}
		set("when", shortStamp(r.Published))
		orig := xOwnText(b.r, b.o.untranslated()) // $原文 永远是原文：投票选项也写原文
		set("origtext", orig)
		if body, note := b.translatedBody(); body.ok {
			set("bodytext", body) // 有译文：$正文文字 就是译文，原文不出现（想要原文写 $原文）
			set("tnote", some(note))
		} else if len(b.o.pollLabelsFor(b.r.Poll)) > 0 {
			set("bodytext", b.xBodyText()) // 只有投票选项有译文（帖子没有自己的正文）：投票块写译文选项
			set("tnote", some(translatedFrom(b.r)))
		} else {
			set("bodytext", orig)
			if b.articleNote != "" {
				set("tnote", some(b.articleNote))
			}
		}
	case "pixiv-series":
		pixivSeriesFields(r, set)
	case "pixiv-artwork", "pixiv-novel":
		pixivNavFields(r, set)
		if b.site.Key == "pixiv-novel" {
			set("site", some("pixiv 小说"))
		} else {
			set("site", some("pixiv 作品"))
		}
		set("title", optText(r.Title))
		set("author", optText(r.Author.Name))
		if x, ok := flagBool(r, "ai_generated"); ok {
			set("ai", some(map[bool]string{true: "是", false: "否"}[x]))
		}
		if level, ok := Restriction(r); ok {
			set("rating", some([]string{"全年龄", "R18", "R18-G"}[level]))
		}
		pages := int64(0)
		if r.Stats["pages"].State == "available" {
			pages = r.Stats["pages"].Value
		}
		if pages == 0 && b.site.Key == "pixiv-artwork" {
			pages = int64(len(b.pixivImages()))
		}
		set("pages", some(strconv.FormatInt(pages, 10)))
		set("firstline", pixivFirstLine(r))
		if b.site.Key == "pixiv-novel" {
			tags := []string{}
			for _, tag := range r.Tags.Value[:min(10, len(r.Tags.Value))] {
				tags = append(tags, linktrunc.Inline(tag, 40))
			}
			set("tags", some(strings.Join(tags, "、")))
			if r.Novel != nil {
				set("words", optNumber(r.Novel.CharacterCount))
				set("desc", optText(r.Novel.Description))
			}
		}
	case "github":
		set("site", some("GitHub"))
		set("title", optText(r.Title))
		set("desc", optText(r.Text))
		if r.GitHub != nil {
			set("language", optText(r.GitHub.Language))
			set("license", optText(r.GitHub.License))
			set("updated", optStamp(r.GitHub.Updated))
		}
		stat("stars", "stars")
		stat("forks", "forks")
		stat("issues", "open_issues")
	case "youtube":
		set("site", some("YouTube"))
		set("title", optText(r.Title))
		set("channel", optText(r.Author.Name))
		set("published", optStamp(r.Published))
		set("duration", durationVal(r))
		set("when", shortStamp(r.Published))
		set("views", optCompact(r.Stats["views"]))
		set("likes", optCompact(r.Stats["likes"]))
		stat("comments", "comments")
	case "douyin-video", "douyin-note":
		if b.site.Key == "douyin-note" {
			set("site", some("抖音图集"))
		} else {
			set("site", some("抖音"))
			set("duration", durationVal(r))
			set("when", shortStamp(r.Published))
		}
		set("text", optText(r.Text))
		set("author", optText(r.Author.Name))
		set("published", optStamp(r.Published))
		if b.site.Key == "douyin-video" {
			set("likes", optCompact(r.Stats["likes"]))
		} else {
			stat("likes", "likes")
		}
		stat("comments", "comments")
		stat("favorites", "favorites")
		stat("shares", "shares")
	case "netease":
		set("site", some("网易云音乐"))
		set("title", optText(r.Title))
		set("artist", optText(r.Author.Name))
		if r.Music != nil {
			set("album", optText(r.Music.Album))
		}
		set("duration", durationVal(r))
	case "xiaoheihe":
		set("site", some("小黑盒"))
		set("appid", some(r.ID))
		set("title", optText(r.Title)) // 取不到游戏名就不写这一行（以前写“游戏 编号”，编号对群友没有意义）
		set("developer", optText(r.Author.Name))
		set("released", releaseVal(r))
		set("desc", optText(r.Text))
	case "booth":
		set("site", some("BOOTH"))
		set("title", optText(r.Title))
		set("shop", optText(r.Author.Name))
		set("category", optText(r.Category))
		set("tags", tagsVal(r, 10))
		stat("wishes", "wish_lists")
		var status []string
		if x, ok := flagBool(r, "sold_out"); ok && x {
			status = append(status, "已售罄")
		}
		if x, ok := flagBool(r, "end_of_sale"); ok && x {
			status = append(status, "已结束销售")
		}
		if len(status) > 0 {
			set("status", some(strings.Join(status, "、")))
		}
		if r.Booth != nil {
			set("price", optText(r.Booth.Price))
			set("variations", boothVariations(r.Booth.Variations))
		}
		set("published", optStamp(r.Published))
		set("desc", optText(r.Text))
	case "steam":
		b.steamFields(v)
	case "afdian":
		set("site", some(map[string]string{"creator": "爱发电创作者", "album": "爱发电相册", "album_post": "爱发电相册动态", "post": "爱发电动态"}[r.Kind]))
		set("title", optText(r.Title))
		set("author", optText(r.Author.Name))
		set("published", optStamp(r.Published))
		stat("likes", "likes")
		stat("comments", "comments")
		stat("posts", "posts")
		if r.Kind == "creator" && v["title"].ok && v["author"].ok && v["title"].text == v["author"].text {
			set("author", none)
		}
		if x, ok := flagBool(r, "sponsor_required"); ok && x {
			s := "需要赞助才能看全文"
			if p := r.Flags["sponsor_min_price"]; p.State == "available" {
				if price, ok := p.Value.(string); ok && price != "" {
					s += "，最低 ¥" + price
				}
			}
			set("status", some(s))
		}
		set("desc", optText(r.Text))
	case "bluesky", "misskey":
		b.socialFields(v)
	case "telegram":
		v["site"] = some("Telegram 帖子")
		v["author"] = person(r)
		v["name"] = optText(r.Author.Name)
		v["account"] = xAccount(r)
		v["bodytext"] = postText(r, b.o)
		v["published"] = optStamp(r.Published)
		v["text"] = optText(r.Text)
		v["views"] = optCompact(r.Stats["views"])
		if line := statsCompact(r, []string{"views"}, []string{"浏览"}, textSep); line != "" {
			v["stats"] = some(line)
		}
	case "spotify":
		b.spotifyFields(v)
	case "tiktok":
		b.tiktokFields(v)
	default:
		set("site", some("链接"))
		set("title", optText(r.Title))
		set("author", optText(r.Author.Name))
		set("text", optText(r.Text))
		set("published", optStamp(r.Published))
	}
	// 模板里写了 $正文文字 时它已经是译文，$译文 不再重复（旧的「$正文文字 —— $译文」写法自动变成只放译文）
	if t := strings.TrimSpace(b.o.Translation); t != "" && translatableSites[b.site.Key] && !(b.site.Key == "x" && b.template("x").Uses("bodytext")) {
		if t = dropRepeatedLinks(t, ownText(r)); strings.TrimSpace(t) != "" {
			set("translation", some(t))
		}
	}
	// $冒号：作者行后面的“：”只在有正文、也有名字或账号时出现（用户 2026-11-13：纯图推文的作者行后面别挂一个冒号）
	if body := v["bodytext"]; body.ok && strings.TrimSpace(body.text) != "" && (v["name"].ok || v["account"].ok) {
		set("colon", some("："))
	}
	return v
}

func durationVal(r *linkreader.Result) val {
	if s := duration(r); s != "未返回" {
		return some(s)
	}
	return none
}

// slotItems 展开一个槽位（封面、卡片、视频、图片、正文）成消息里的项；没有内容返回空。
func (b *builder) slotItems(id string) []Item {
	r := b.r
	if b.site.Key == "x" && (id == "mosaic" || id == "images" || id == "video") {
		return b.xMediaItems(id)
	}
	if b.site.Key == "x" && id == "quoted" {
		return b.xQuoteItems()
	}
	if postSite(b.site.Key) && (id == "images" || id == "video") {
		return b.postMediaItems(id)
	}
	switch id {
	case "cover":
		if b.site.Key == "pixiv-novel" {
			// 小说封面单独存放，不在 Media 里（那里是正文插图）
			if r.Novel != nil && r.Novel.Cover.State == "available" && sendable(r.Novel.Cover.Value) {
				return []Item{mediaItem(r.Novel.Cover.Value, false)}
			}
			return nil
		}
		if m, ok := firstImage(r); ok {
			return []Item{mediaItem(m, false)}
		}
		if m, ok := profileAvatar(r); ok {
			it := mediaItem(m, false)
			it.Optional = true
			return []Item{it}
		}
	case "card":
		return b.cardItems()
	case "video":
		for _, m := range r.Media.Value {
			if m.Type == "video" && sendable(m) {
				return []Item{mediaItem(m, true)}
			}
		}
	case "images":
		if b.site.Key == "bilibili-dynamic" {
			return b.dynamicImages()
		}
		if b.site.Key == "booth" {
			return b.boothImages()
		}
		if b.site.Key == "afdian" {
			return b.afdianImages()
		}
		return b.imageItems()
	case "extra":
		return b.dynamicExtras()
	case "body":
		return b.bodyItems()
	}
	return nil
}

// cardItems 是 B 站视频和会员购的卡片（字段由代码取，不由模板排列）。
func (b *builder) cardItems() []Item {
	r := b.r
	var data *linkcard.Data
	switch b.site.Key {
	case "bilibili-video":
		title := field(r.Title)
		if r.SelectedPart.State == "available" && len(r.Parts.Value) > 1 {
			title += fmt.Sprintf("\nP%d/%d：%s", r.SelectedPart.Value.Number, len(r.Parts.Value), field(r.SelectedPart.Value.Title))
		}
		s1 := statsCompact(r, []string{"view", "favorite", "share", "reply"}, []string{"播放", "收藏", "转发", "评论"}, cardSep)
		s2 := statsCompact(r, []string{"danmaku", "like", "coin"}, []string{"弹幕", "点赞", "投币"}, cardSep)
		var date []string
		if p := optStamp(r.Published); p.ok {
			date = append(date, "上传："+p.text)
		}
		if t, ok := biliTotal(r); ok {
			date = append(date, "总长："+t) // 多 P 的整本：和文字版的“总长”一致，不拿 P1 的时长冒充
		} else if d := durationVal(r); d.ok {
			date = append(date, "时长："+d.text)
		}
		var lines []string
		for _, l := range []string{s1, s2} {
			if l != "" {
				lines = append(lines, l)
			}
		}
		who := "UP主：" + field(r.Author.Name)
		if u := upowerVal(r); u.ok {
			who += "｜" + u.text
		}
		data = &linkcard.Data{Title: title, Author: who, DateDuration: strings.Join(date, "｜"), Stats: lines}
	case "bilibili-mall":
		details := r.Mall.Details()
		data = &linkcard.Data{Title: field(r.Title), Author: details[2], DateDuration: details[0] + "\n" + details[1], Stats: details[3:]}
	default:
		data = b.genericCard()
		if data == nil {
			return nil
		}
	}
	item := Item{Kind: "card", Card: data}
	item.Translation, item.TranslationNote = b.cardTranslation()
	if b.site.Key == "x" {
		item.PollLabels = b.o.pollLabelsFor(b.r.Poll)
		if q := quotedTweet(b.r); q != nil {
			item.QuoteTranslation = b.quoteTranslated(q)
		}
	}
	if cover, ok := firstImage(r); ok {
		item.Media = cover
	} else if avatar, ok := profileAvatar(r); ok {
		item.Media = avatar
	}
	return []Item{item}
}

// liveStatus 是直播间状态的中文：0 未开播，1 直播中，2 轮播中；取不到返回空串。
func liveStatus(r *linkreader.Result) string {
	f, ok := r.Flags["live_status"]
	if !ok || f.State != "available" {
		return ""
	}
	switch numeric(f) {
	case 0:
		return "未开播"
	case 1:
		return "直播中"
	case 2:
		return "轮播中"
	}
	return ""
}

// genericCard 是除 B 站视频、会员购以外各站点的卡片：标题（没有标题用正文开头）、作者、日期一行、一两行统计，
// 字段由代码按站点取，模板只决定卡片放在哪。取不到标题也取不到正文时不出卡片（模板里的 $卡片 就没有内容，由退路处理）。
func (b *builder) genericCard() *linkcard.Data {
	r := b.r
	title := ""
	if r.Title.State == "available" && strings.TrimSpace(r.Title.Value) != "" {
		title = r.Title.Value
	} else if r.Text.State == "available" {
		title = linktrunc.Inline(strings.SplitN(strings.TrimSpace(r.Text.Value), "\n", 2)[0], 80)
	}
	// 正文开头只是一条链接（Mastodon 帖子常见）时，标题不写一长串网址：用外链卡的标题，没有就用作者。
	if isBareURL(title) {
		title = ""
		if r.LinkCard.State == "available" {
			title = linktrunc.Inline(strings.TrimSpace(r.LinkCard.Value.Title.Value), 80)
		}
		if strings.TrimSpace(title) == "" {
			title = person(r).text
		}
	}
	if strings.TrimSpace(title) == "" {
		return nil
	}
	data := &linkcard.Data{Title: title}
	who := func(label string) {
		if n := optText(r.Author.Name); n.ok {
			data.Author = label + author(r)
		}
	}
	var date []string
	if p := optStamp(r.Published); p.ok {
		date = append(date, p.text)
	}
	noWho := false // 主页类卡片的作者就是标题，不再写一行平台名（用户 2026-11-13：卡片上的“B站个人主页”“GitHub”这类平台名去掉）
	var lines []string
	add := func(keys, labels []string) {
		if s := stats(r, keys, labels, cardSep); s != "" {
			lines = append(lines, s)
		}
	}
	switch b.site.Key {
	case "bilibili-live":
		who("主播：")
		date = nil
		if s := liveStatus(r); s != "" {
			date = append(date, "状态："+s)
		}
		if c := optText(r.Category); c.ok {
			date = append(date, "分区："+c.text)
		}
		// 直播间的“关注”就是主播粉丝数，取数端已并进 followers（attention 记成没取到），卡片只写粉丝
		keys, labels := []string{"followers"}, []string{"粉丝"}
		if n := r.Stats["online"]; n.State == "available" && n.Value > 0 {
			keys, labels = []string{"online", "followers"}, []string{"人气", "粉丝"} // 轮播、没开播时人气是 0，不写
		}
		add(keys, labels)
	case "bilibili-space":
		noWho = true
		date = nil
		if c := optText(r.Category); c.ok {
			date = append(date, "认证："+c.text)
		}
		add([]string{"followers", "following", "videos", "likes"}, []string{"粉丝", "关注", "投稿", "获赞"})
	case "bilibili-dynamic":
		who("UP主：")
		add([]string{"like", "comment", "forward"}, []string{"点赞", "评论", "转发"})
	case "x":
		who("")
		if s := xStats(r, cardSep); s != "" {
			lines = append(lines, s)
		}
	case "github":
		noWho = true
		date = nil // 日期是更新时间，和文字版一致，带“更新：”
		if r.GitHub != nil {
			if u := optStamp(r.GitHub.Updated); u.ok {
				date = append(date, "更新："+u.text)
			}
		}
		add([]string{"stars", "forks", "open_issues"}, []string{"Star", "Fork", "Issue"})
	case "youtube":
		who("频道：")
		if d := durationVal(r); d.ok {
			date = append(date, "时长："+d.text)
		}
		add([]string{"views", "likes", "comments"}, []string{"观看", "点赞", "评论"})
	case "douyin-video", "douyin-note":
		who("作者：")
		if d := durationVal(r); d.ok && b.site.Key == "douyin-video" {
			date = append(date, "时长："+d.text)
		}
		add([]string{"likes", "comments", "favorites", "shares"}, []string{"点赞", "评论", "收藏", "转发"})
	case "pixiv-artwork", "pixiv-novel", "pixiv-series":
		who("作者：")
		add([]string{"pages", "total"}, []string{"页数", "总数"})
	case "steam":
		date = nil // 同文字版：发行日期只有日期，不是上传时刻
		if d := releaseVal(r); d.ok {
			date = append(date, "发行："+d.text)
		}
	case "spotify", "xiaoheihe":
		// 发行日期只有日期：照文字版写，不补“08:00”（以前卡片和文字差一天、带假钟点）
		date = nil
		if d := releaseVal(r); d.ok {
			date = append(date, d.text)
		}
	case "tiktok":
		if r.Kind == "user" {
			noWho = true
			data.Author = profileHandle(r)
		}
	case "bluesky", "misskey":
		if profileKind(r) {
			noWho = true
			data.Author = profileHandle(r)
			date = nil
			if d := dateVal(r.Published); d.ok {
				date = append(date, "注册："+d.text)
			}
		}
	case "netease":
		who("歌手：")
		if r.Music != nil {
			if a := optText(r.Music.Album); a.ok {
				date = append(date, "专辑："+a.text)
			}
		}
		if d := durationVal(r); d.ok {
			date = append(date, "时长："+d.text)
		}
	default:
		if biliMoreSite(b.site.Key) {
			cp := cardParts{date: date, lines: lines}
			b.biliMoreCard(&cp)
			data.Author, date, lines = cp.author, cp.date, cp.lines
			if cp.title != "" {
				data.Title += "\n" + cp.title
			}
		}
	}
	if data.Author == "" && !noWho {
		who("")
	}
	data.DateDuration = strings.Join(date, "｜")
	data.Stats = lines
	return data
}

// pixivImages 是 pixiv 作品的图（动图换成 ugoira 项），按页顺序；转发时再截前 10 张。
func (b *builder) pixivImages() []Item {
	if b.loaded {
		return b.images
	}
	b.loaded = true
	r := b.r
	for _, item := range contents(r, b.o, true, 0) {
		if item.Kind != types.BodyText {
			b.images = append(b.images, item)
		}
	}
	if typ, ok := r.Flags["illust_type"]; ok && numeric(typ) == 2 && r.Ugoira != nil {
		fallback, _ := firstImage(r)
		if fallback.Thumbnail.State != "available" {
			fallback.Thumbnail = fallback.URL
		}
		b.images = []Item{{Kind: "ugoira", Ugoira: r.Ugoira, Media: fallback}}
	}
	return b.images
}

func (b *builder) imageItems() []Item {
	images := b.pixivImages()
	if !b.forwd {
		return images
	}
	pages := int64(0)
	if b.r.Stats["pages"].State == "available" {
		pages = b.r.Stats["pages"].Value
	}
	if pages == 0 {
		pages = int64(len(images))
	}
	shown := append([]Item(nil), images[:min(10, len(images))]...)
	if pages > 10 {
		shown = append(shown, text(fmt.Sprintf("只发前10张，另有%d张，请看原文。", pages-10)))
	}
	return shown
}

// bodyItems 是帖子的正文块（文字、图、视频、引用）按原顺序；pixiv 小说是正文节选加最多 3 张图。
// 直发的 X 短帖里，视频必须独立成一条消息（QQ 的视频消息不能带文字）；动图在 QQ 里是图，跟文字同一条。
func (b *builder) bodyItems() []Item {
	r := b.r
	content := contents(r, b.o, true, 0)
	switch b.site.Key {
	case "pixiv-novel":
		novel := []Item{text("正文节选：\n" + novelExcerpt(r))}
		count := 0
		for _, item := range content {
			if item.Kind == types.BodyImage && count < 3 {
				novel = append(novel, item)
				count++
			}
		}
		return novel
	case "x", "telegram":
		if !b.forwd {
			for i := range content {
				if content[i].Kind == types.BodyVideo {
					content[i].Standalone = true
				}
			}
		}
	}
	return content
}

// assemble 把渲染出的片段排成消息：$分段 另起一条消息；$合并转发 之后进合并转发，$节点 换下一个节点。
// 引用（回复原消息）只挂在第一条消息上。
func (b *builder) assemble(pieces []linktemplate.Piece, quote string) []Part {
	var parts []Part
	var cur []Item
	var groups [][]Item
	inForward := false
	quotedGroup := -1 // $被引推文 落在转发的第几组（组 0 是主推文头部）
	flushMessage := func() {
		if len(cur) > 0 {
			parts = append(parts, Part{Kind: types.PartMessage, Body: cur})
		}
		cur = nil
	}
	for _, p := range pieces {
		switch p.Kind {
		case linktemplate.PieceText:
			if inForward {
				groups[len(groups)-1] = append(groups[len(groups)-1], text(p.Text))
			} else {
				cur = append(cur, text(p.Text))
			}
		case linktemplate.PieceSlot:
			items := b.slotItems(p.ID)
			if inForward {
				if p.ID == "quoted" && len(items) > 0 && b.quotedTweet && quotedGroup < 0 && len(groups[len(groups)-1]) == 0 {
					quotedGroup = len(groups) - 1
				}
				groups[len(groups)-1] = append(groups[len(groups)-1], items...)
			} else {
				cur = append(cur, items...)
			}
		case linktemplate.PieceBreak:
			if inForward {
				groups = append(groups, nil)
			} else {
				flushMessage()
			}
		case linktemplate.PieceForward:
			if !inForward {
				flushMessage()
				inForward = true
				groups = [][]Item{nil}
			}
		case linktemplate.PieceNode:
			if inForward {
				groups = append(groups, nil)
			}
		}
	}
	flushMessage()
	if inForward {
		var nodes []Node
		quotedAt, quotedEnd := -1, -1
		for i, g := range groups {
			if len(g) > 0 {
				if i == quotedGroup {
					quotedAt = len(nodes)
				}
				gn := toNodes(g)
				if len(nodes) == 0 {
					markNoteEnd(gn)
				}
				nodes = append(nodes, gn...)
				if i == quotedGroup {
					quotedEnd = len(nodes)
				}
			}
		}
		// 引用推文（规格-链接发送改版 B2、A7）：被引推文那组节点挪到最前面（用户 2026-11-12：点开转发先看到被引的那条），
		// 主推文头部节点标上它（QuoteOf = 1）；发送端映射成 reply 段，协议端不支持就去掉重发。“发过 N 次”这类提示仍跟着主推文头部（noteEnd）
		if quotedAt > 0 {
			head := quotedEnd - quotedAt // 挪完以后主推文头部的下标
			moved := append(append([]Node{}, nodes[quotedAt:quotedEnd]...), nodes[:quotedAt]...)
			nodes = append(moved, nodes[quotedEnd:]...)
			nodes[head].QuoteOf = 1
		}
		if len(nodes) > 0 {
			parts = append(parts, Part{Kind: types.PartForward, Nodes: nodes})
		}
	}
	if len(parts) > 0 {
		parts[0].Quote = quote
	}
	return parts
}

// Describe 把计划写成文字，每条消息（或合并转发）一段：图片类写成 [图片] 之类的方块，卡片写出标题。
// 给 sumika-ctl link preview 和设置页看效果用，不碰媒体。
func (d Draft) Describe() []string {
	var out []string
	items := func(items []Item) string {
		var lines []string
		for _, it := range items {
			switch {
			case it.Kind == types.BodyText:
				lines = append(lines, it.Text)
			case it.Kind == "card":
				title := ""
				if it.Card != nil {
					title = it.Card.Title
				}
				lines = append(lines, "[卡片："+strings.ReplaceAll(title, "\n", " ")+"]")
			case it.Kind == types.BodyVideo:
				lines = append(lines, "[视频，单独一条消息]")
			case it.Kind == types.BodyGIF || it.Kind == "ugoira":
				lines = append(lines, "[动图]")
			default:
				lines = append(lines, "[图片]")
			}
		}
		return strings.Join(lines, "\n")
	}
	for _, p := range d.Parts {
		switch p.Kind {
		case types.PartForward:
			var b strings.Builder
			fmt.Fprintf(&b, "合并转发（%d 个节点）", len(p.Nodes))
			if c := p.Card; c != nil {
				fmt.Fprintf(&b, "\n  卡片：标题 %q｜预览 %q｜摘要 %q｜外显 %q", c.Source, c.News, c.Summary, c.Prompt)
			}
			for i, n := range p.Nodes {
				fmt.Fprintf(&b, "\n  节点 %d：%s", i+1, strings.ReplaceAll(items(n.Body), "\n", "\n    "))
			}
			out = append(out, b.String())
		default:
			head := ""
			if p.Quote != "" {
				head = "（回复原消息）\n"
			}
			out = append(out, head+items(p.Body))
		}
	}
	return out
}

// relaxedContent 是后来接进来的站点：它们的结果常常没有标题（TikTok 视频、小黑盒游戏页），靠作者或媒体也算有内容。
var relaxedContent = map[string]bool{"xiaoheihe": true, "booth": true, "steam": true, "afdian": true, "bluesky": true, "misskey": true, "spotify": true, "tiktok": true}

// hasContent 说这个结果有没有东西可发：标题、正文、作者名、媒体任一项取到就行。
// 以前只看标题和正文，TikTok 没写文案的视频、只有封面的歌会被当成失败。
func hasContent(r *linkreader.Result) bool {
	if !relaxedContent[linktemplate.SiteKey(r.Platform, r.Kind)] {
		// 老站点保持原判断：标题和正文都没取到就当失败
		return r.Title.State == "available" || r.Text.State == "available"
	}
	return r.Title.State == "available" || r.Text.State == "available" || r.Author.Name.State == "available" || (r.Media.State == "available" && len(r.Media.Value) > 0)
}

// person 是“昵称 @账号”；昵称缺了只写 @账号，账号缺了只写昵称，都没有就是没取到（不写“未返回”）。
func person(r *linkreader.Result) val {
	name, acc := optText(r.Author.Name), optText(r.Author.Account)
	switch {
	case name.ok && acc.ok && sameHandle(name.text, acc.text):
		return name
	case name.ok && acc.ok:
		return some(name.text + " @" + acc.text)
	case name.ok:
		return name
	case acc.ok:
		return some("@" + acc.text)
	}
	return none
}

// dateVal 是只有日期的时间（发行日期之类），不写“08:00”这种凭空多出来的钟点。
func dateVal(f linkreader.Field[time.Time]) val {
	if f.State != "available" {
		return none
	}
	return some(f.Value.In(displayZone).Format("2006-01-02")) // 一律按北京时间取日期：小黑盒的 Published 是北京时间零点，按 UTC 会差一天
}

// releaseVal 是发行日期：有时间戳就按北京时间写日期（和别处的日期同一写法），没有（即将推出、只有文字的日期）才用原文。
func releaseVal(r *linkreader.Result) val {
	if v := dateVal(r.Published); v.ok {
		return v
	}
	return optText(r.PublishedText)
}

func tagsVal(r *linkreader.Result, n int) val {
	if r.Tags.State != "available" || len(r.Tags.Value) == 0 {
		return none
	}
	tags := make([]string, 0, n)
	for _, tag := range r.Tags.Value[:min(n, len(r.Tags.Value))] {
		tags = append(tags, linktrunc.Inline(tag, 40))
	}
	return some(strings.Join(tags, "、"))
}

func (b *builder) steamFields(v map[string]val) {
	r := b.r
	site := "Steam 商店"
	switch r.Kind {
	case "app":
		site = "Steam 游戏"
		if r.Steam != nil && r.Steam.Type.State == "available" && r.Steam.Type.Value != "game" {
			site = "Steam 商店"
		}
	case "package":
		site = "Steam 礼包"
	case "sharedfile":
		site = "Steam 创意工坊"
	case "profile":
		site = "Steam 个人主页"
	}
	v["site"] = some(site)
	v["title"] = optText(r.Title)
	v["desc"] = optText(r.Text)
	// 作者只有游戏、礼包是开发商；个人主页是玩家本人，创意工坊是上传者（#65）
	if r.Kind == "app" || r.Kind == "package" {
		v["developer"] = optText(r.Author.Name)
	}
	v["released"] = releaseVal(r)
	v["tags"] = tagsVal(r, 8)
	v["recommendations"] = optCompact(r.Stats["recommendations"])
	s := r.Steam
	if s == nil {
		return
	}
	if p := s.Publishers; p.State == "available" && len(p.Value) > 0 {
		v["publisher"] = some(strings.Join(p.Value, " / "))
	}
	if s.IsFree.State == "available" && s.IsFree.Value {
		v["price"] = some("免费")
	} else if s.Price.State == "available" {
		v["price"] = optText(s.Price.Value.Formatted)
		if d := s.Price.Value.DiscountPercent; d.State == "available" && d.Value > 0 {
			v["discount"] = some("-" + strconv.FormatInt(d.Value, 10) + "%")
		}
	}
	if s.RequiredAge.State == "available" && s.RequiredAge.Value > 0 {
		v["age"] = some(strconv.Itoa(s.RequiredAge.Value) + "+")
	}
}

// socialFields 是 Bluesky、Misskey、Mastodon 的帖子和主页：作者、时间、正文槽位、一行统计。
func (b *builder) socialFields(v map[string]val) {
	r := b.r
	label := map[string]string{"bluesky/post": "Bluesky 帖子", "bluesky/profile": "Bluesky 主页", "misskey/note": "Misskey 帖子", "misskey/user": "Misskey 用户", "mastodon/status": "Mastodon 帖子", "mastodon/account": "Mastodon 账号"}[r.Platform+"/"+r.Kind]
	v["site"] = some(label)
	v["author"] = person(r)
	v["name"] = optText(r.Author.Name)
	v["account"] = xAccount(r) // 不带 @（和推文一样），模板写 [@$账号]
	v["bodytext"] = postText(r, b.o)
	v["published"] = optStamp(r.Published)
	v["text"] = optText(r.Text)
	for id, key := range map[string]string{"likes": "likes", "reposts": "reposts", "replies": "replies", "quotes": "quotes", "followers": "followers", "follows": "follows", "posts": "posts"} {
		v[id] = optCompact(r.Stats[key])
	}
	// 个人主页的时间是注册时间（只有日期有意义），不当“发布时间”写
	if profileKind(r) {
		v["published"] = none
		v["joined"] = dateVal(r.Published)
	}
	var line string
	switch r.Platform + "/" + r.Kind {
	case "bluesky/post":
		line = stats(r, []string{"likes", "reposts", "replies", "quotes"}, []string{"点赞", "转发", "回复", "引用"}, textSep)
	case "bluesky/profile":
		line = stats(r, []string{"followers", "follows", "posts"}, []string{"粉丝", "关注", "帖子"}, textSep)
	case "misskey/note":
		line = stats(r, []string{"renotes", "replies", "reactions"}, []string{"转发", "回复", "反应"}, textSep)
	case "misskey/user":
		line = stats(r, []string{"followers", "following", "notes"}, []string{"粉丝", "关注", "帖子"}, textSep)
	case "mastodon/status":
		line = stats(r, []string{"favourites", "reblogs", "replies"}, []string{"点赞", "转发", "回复"}, textSep)
	case "mastodon/account":
		line = stats(r, []string{"followers", "following", "statuses"}, []string{"粉丝", "关注", "帖子"}, textSep)
	}
	if line != "" {
		v["stats"] = some(line)
	}
	// 平台给了敏感标记才写“是”；没返回就不写。不拦截、不改图，只让看的人知道。
	for _, key := range []string{"possibly_sensitive", "sensitive"} {
		if x, ok := flagBool(r, key); ok && x {
			v["sensitive"] = some("是")
		}
	}
	if f := r.Flags["content_warning"]; f.State == "available" {
		if cw, ok := f.Value.(string); ok && strings.TrimSpace(cw) != "" {
			v["warning"] = some(cw)
		}
	}
}

func (b *builder) spotifyFields(v map[string]val) {
	r := b.r
	v["site"] = some(map[string]string{"track": "Spotify 单曲", "album": "Spotify 专辑", "playlist": "Spotify 歌单", "artist": "Spotify 艺人", "episode": "Spotify 单集"}[r.Kind])
	v["title"] = optText(r.Title)
	if r.Kind == "episode" {
		v["show"] = optText(r.Author.Name)
	} else if r.Kind != "artist" && r.Kind != "playlist" {
		v["artist"] = optText(r.Author.Name)
	}
	v["released"] = dateVal(r.Published)
	v["duration"] = durationVal(r)
	if x, ok := flagBool(r, "explicit"); ok && x {
		v["explicit"] = some("是")
	}
}

func (b *builder) tiktokFields(v map[string]val) {
	r := b.r
	switch {
	case r.Kind == "user":
		v["site"] = some("TikTok 用户")
	case b.hasMedia("video"):
		v["site"] = some("TikTok 视频")
	default:
		v["site"] = some("TikTok 图文")
	}
	v["title"] = optText(r.Title)
	v["text"] = optText(r.Text)
	v["author"] = person(r)
	if r.Kind == "user" {
		// 用户主页的标题就是昵称（官方号 @tiktok 的昵称本来就叫“TikTok”），作者行再写一遍昵称是重复；账号和昵称不同才补一行 @账号
		v["author"] = none
		if h := profileHandle(r); h != "" {
			v["author"] = some(h)
		}
	}
	v["published"] = optStamp(r.Published)
	v["duration"] = durationVal(r)
	for id, key := range map[string]string{"views": "views", "likes": "likes", "comments": "comments", "shares": "shares", "collects": "collects", "followers": "followers", "following": "following", "hearts": "heart", "videos": "videos"} {
		v[id] = optCompact(r.Stats[key])
	}
	v["views"], v["likes"] = optCompact(r.Stats["views"]), optCompact(r.Stats["likes"])
	v["when"] = shortStamp(r.Published)
	if x, ok := flagBool(r, "verified"); ok && x {
		v["verified"] = some("是")
	}
}

func (b *builder) hasMedia(kind string) bool {
	for _, m := range b.r.Media.Value {
		if m.Type == kind {
			return true
		}
	}
	return false
}

// profileHandle 是主页卡片、文字版作者行写的内容：标题已经是昵称，所以只在账号和昵称不同时补一行 @账号，相同或没有就空。
func profileHandle(r *linkreader.Result) string {
	if acc := optText(r.Author.Account); acc.ok && !sameHandle(acc.text, r.Author.Name.Value) {
		return "@" + acc.text
	}
	return ""
}

// profileKind 说这是不是个人主页（Bluesky、Misskey、Mastodon）：它们的 Published 是注册时间。
func profileKind(r *linkreader.Result) bool {
	switch r.Platform + "/" + r.Kind {
	case "bluesky/profile", "misskey/user", "mastodon/account":
		return true
	}
	return false
}

// boothVariations 是 BOOTH 的规格行：“规格：”下面每个规格一行“名字 ¥ 1,500”，没库存的标“（售罄）”；
// 只有一个规格、或一个都没有时不写（价格那一行已经说清楚了）。最多列 10 个，多的写“另有 N 个规格”。
func boothVariations(vs []linkreader.BoothVariation) val {
	if len(vs) < 2 {
		return none
	}
	lines := []string{"规格："}
	for i, v := range vs {
		if i == 10 {
			lines = append(lines, fmt.Sprintf("另有 %d 个规格", len(vs)-10))
			break
		}
		name := strings.TrimSpace(v.Name.Value)
		if v.Name.State != "available" || name == "" {
			name = "未命名规格"
		}
		line := name
		if v.Price.State == "available" {
			line += " ¥ " + yen(v.Price.Value)
		}
		if v.SoldOut {
			line += "（售罄）"
		}
		lines = append(lines, line)
	}
	return some(strings.Join(lines, "\n"))
}

// yen 把日元整数写成带千分位的“1,500”（BOOTH 页面上的写法）。
func yen(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return neg + s
}

// boothImages 是 BOOTH 商品图的前 boothImageMax 张（用户 2026-11-12：最多发 3 张），按原顺序。
func (b *builder) boothImages() []Item {
	var out []Item
	for _, m := range b.r.Media.Value {
		if len(out) == boothImageMax {
			break
		}
		if (m.Type == "image" || m.Type == "photo") && sendable(m) {
			out = append(out, mediaItem(m, false))
		}
	}
	return out
}

const boothImageMax = 3

// afdianImageMax 是爱发电动态最多列出几张图：图数多的动态（一页漫画几十张）超过直发上限走合并转发，同 pixiv 最多 10 张，多的写一句“请看原文”。
const afdianImageMax = 10

// afdianImages 是爱发电动态的全部静态图和动图（按原顺序、不含视频）：以前模板只写 $封面，14 张图的动态只发第一张。
// 直发几张、多了怎么办（合并转发，或站点设成从不转发时只发前几张并标“（N/总数）”）交给发送形状（shape.go）。
func (b *builder) afdianImages() []Item {
	var out []Item
	total := 0
	for _, m := range b.r.Media.Value {
		if (m.Type != "image" && m.Type != "photo" && m.Type != "gif") || !sendable(m) {
			continue
		}
		total++
		if len(out) < afdianImageMax {
			out = append(out, mediaItem(m, false))
		}
	}
	if total > afdianImageMax {
		// 图在模板最前面，这句不能跟着图排在标题前面：记下来，Build 在整条消息的末尾（转发是第一个节点的末尾）补上
		b.moreImages = fmt.Sprintf("只发前%d张，另有%d张，请看原文。", afdianImageMax, total-afdianImageMax)
	}
	return out
}
