package linkcard

// 推特卡片（规格-链接发送改版 C1、附录 C）：照 X 网页（react-tweet 的 CSS）画一张帖子的图。
// 版式按 550 逻辑像素宽排、按 720/550 放大成 720 像素宽的图，所以下面的数都是 react-tweet 的 CSS 像素原值。
// 这里不读网络和文件：头像、媒体由宿主下好、解好码再交进来；文字由宿主先过文字守卫。

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/emoji"
	"sumika-native/internal/linktrunc"
)

// Bitmap 是解好码的位图（DecodeCover 的结果），宿主不用另外引 mygo
type Bitmap = ui.Bitmap

// TweetMedia 是一张图或一个视频的缩略图
type TweetMedia struct {
	// Image 为空是没下到或解不开：画一块占位写“图片未加载”
	Image *ui.Bitmap
	// Video 为真时在缩略图上画播放钮（视频、动图）
	Video bool
	// Ratio 是高 / 宽（取数给的尺寸）；0 就按 Image 的尺寸
	Ratio float64
}

// TweetAuthor 是作者；Verification 是 none / blue / business / government，空或别的都不画认证标
type TweetAuthor struct {
	Name, Account, Verification string
	Avatar                      *ui.Bitmap
}

// Stat 是一个互动数；OK 为假是取数没给，不画这一项（不画成 0）
type Stat struct {
	N  int64
	OK bool
}

// Tweet 是一张推特卡片的全部内容。Quote 只画一层；QuoteGone 非空时画一个只写这句话的引用框（被引的帖子删了或不可见）。
type Tweet struct {
	Author                         TweetAuthor
	Text                           string
	Media                          []TweetMedia
	Quote                          *Tweet
	QuoteGone                      string
	Published                      time.Time
	Replies, Reposts, Likes, Views Stat
	// Poll 是投票，画在正文和图的下面、引用框的上面
	Poll *TweetPoll
	// Flow 是长文（X 文章、图夹在段落中间的帖子）按顺序的内容；只有不封顶时用它代替 Text 加 Media（tweet_flow.go）
	Flow []FlowBlock
	// TranslationNote 非空说明 Text 已经是译文（原文不画），这一句画在正文下面：小一号、次要色，照 X 网页版的“翻译自日语”，
	// 例如“翻译自日语”“翻译自外语”（宿主按检测到的源语言写）。有 Flow 时不画：长文按段落原样排，不换成译文。
	TranslationNote string
}

// 版式数值（CSS 像素，照 react-tweet；规格附录 C 的表）
const (
	tweetW        float32 = 550
	cardBorder    float32 = 1
	padX          float32 = 16 // .article padding: 0.75rem 1rem
	padY          float32 = 12
	avatarSize    float32 = 48
	authorGap     float32 = 8 // .author margin: 0 0.5rem
	headerSize    float32 = 15
	headerLine    float32 = 20
	headerPB      float32 = 12 // .header padding-bottom
	badgeSize     float32 = 18.75
	badgeGap      float32 = 2
	bodySize      float32 = 20 // --tweet-body-font-size 1.25rem
	bodyLine      float32 = 24 // --tweet-body-line-height 1.5rem
	mediaMT       float32 = 12
	mediaGap      float32 = 2
	frameRadius   float32 = 12
	quoteMY       float32 = 12 // --tweet-quoted-container-margin 0.75rem 0
	quotePad      float32 = 12
	quoteAvatar   float32 = 20
	quoteBodySize float32 = 15
	quoteBodyLine float32 = 20
	quoteBodyMT   float32 = 4 // --tweet-quoted-body-margin 0.25rem 0 0.75rem 0
	quoteBodyMB   float32 = 12
	infoMT        float32 = 2 // .info margin-top 0.125rem
	infoSize      float32 = 15
	infoLine      float32 = 20
	actionsMT     float32 = 4 // .actions margin-top / padding-top 0.25rem
	actionsPT     float32 = 4
	actionSize    float32 = 14
	actionIcon    float32 = 17.5 // 1.25em
	actionWrap    float32 = 28   // icon + 0.75em
	actionTextML  float32 = 4
	multiRatio    float32 = 0.5625
	noteSize      float32 = 13 // 译文下面的“翻译自……”：小一号、次要色
	noteLine      float32 = 18
	noteMT        float32 = 4
	minBodyLines          = 3
	minQuoteLines         = 2
)

// tweetScale 是逻辑像素换成图上像素的倍数
const tweetScale = float32(Width) / tweetW

type box struct{ X, Y, W, H float32 }

type cell struct {
	box
	m    TweetMedia
	more int // 超过四张时最后一格写 +N
}

type action struct {
	icon string
	at   box
	text string
	tx   float32
	w    float32 // 图标框加数字的整宽
}

type tweetLayout struct {
	H                       float32
	avatar, name, handle    box
	badge                   box
	badgeKind               string
	nameText, handleText    string
	body                    box
	note                    box // 正文下面的“翻译自……”（没有译文是零值）
	noteText                string
	bodyLines, bodyAll      int
	bodyText                string // 实际画的正文：排不下时截在最近的标点后，补“……”和“完整内容见原文”（linktrunc）
	media                   box
	cells                   []cell
	flow                    []flowBox // 不封顶的长文：正文区按顺序排好的各块（非空时不再用 bodyText 和 cells）
	poll                    box       // 投票的整块（没有投票是零值）
	pollRows                []pollRow
	pollFoot                box
	pollFootText            string
	quote                   box
	qAvatar, qName, qHandle box
	qBadge                  box
	qBadgeKind              string
	qNameText, qHandleText  string
	qBody                   box
	qBodyLines, qBodyAll    int
	qBodyText               string
	qMedia                  box
	qCells                  []cell
	qGone                   string
	timeBox                 box
	timeText                string // 时间；空是没有时间
	viewsText               string // 浏览数（时间行后面的“· N 查看”）；空是取不到
	actionsLine             box
	actions                 []action
}

// measurer 量文字：多少行、多高、单行多宽；字体由 fontSet 给。
// 行高照表里的 CSS 值设，但 mygo 的一行至少有字体自己的上下伸展那么高（文渊圆体 20 像素字一行 28.96），
// 所以高度一律按实际排出来的量，画的时候用同样的字号、行高，量和画对得上。
// 表情换成 Twemoji 图的内嵌对象（emoji.go），量和画用同一份 Span。
type measurer struct {
	family string
	emoji  *emoji.Set // nil：表情交字体画
}

// punctTrim 是挤压相邻全角标点的 OpenType 特性 chws（上下文半宽：相邻的全角标点去掉半个字宽，CSS text-spacing-trim、
// JIS X 4051 约物詰め那一类；浏览器实现 text-spacing-trim 也是借字体的 chws / halt）。挤哪些对由字体定：
// 文渊圆体的 GPOS 里有，量过“？！”从 40 变 30（20 像素字），单个标点、汉字之间不变。字体没有这个特性时什么都不做。量和画都开。
// 后面四个是默认就开的特性：DirectWrite 一旦给了特性表，没写进去的默认特性（liga 等）就不开了，
// 只写 chws 会让 “finally” 的 fi 连字散掉、拉丁文宽度变；写全了和不给特性时逐像素宽度一样。
const punctTrim = "chws,liga,clig,calt,kern"

func (m measurer) shape(s string, size, line float32, weight int, width float32) *ui.TextLayout {
	sp, _ := emojiSpans(m.emoji, s, size, line)
	return ui.ShapeRichText(sp, ui.Font{Family: m.family, Size: size, Weight: weight, LineHeight: line / size, Features: punctTrim}, width)
}

// block 是最多 limit 行（0 不限）时的行数和高度；全文的行数另外返回
func (m measurer) block(s string, size, line float32, weight int, width float32, limit int) (n int, h float32, all int) {
	if strings.TrimSpace(s) == "" {
		return 0, 0, 0
	}
	ls := m.shape(s, size, line, weight, width).Lines()
	all = len(ls)
	n = all
	if limit > 0 && n > limit {
		n = limit
	}
	if n == 0 {
		return 0, 0, all
	}
	last := ls[n-1].Rect
	return n, last.Y + last.H, all
}

// blockFit 是最多 limit 行（0 不限）时要画的文字：全文排得下就是原文；排不下不再硬夹在 limit 行（末行被省略号吃掉半句），
// 而是截在最近的标点后，补“……”，最后一行写“完整内容见原文”（linktrunc.FitBody，用户 2026-11-13，R-220）。
// 返回的 n 是画出来的行数，all 是全文的行数。
func (m measurer) blockFit(s string, size, line float32, weight int, width float32, limit int) (shown string, n int, h float32, all int) {
	n, h, all = m.block(s, size, line, weight, width, 0)
	if limit <= 0 || all <= limit {
		return s, n, h, all
	}
	shown, _ = linktrunc.FitBodyLines(s, limit, func(d string) int {
		lines, _, _ := m.block(d, size, line, weight, width, 0)
		return lines
	})
	n, h, _ = m.block(shown, size, line, weight, width, 0)
	return shown, n, h, all
}

// lineHeight 是一行字实际的高
func (m measurer) lineHeight(size, line float32, weight int) float32 {
	_, h, _ := m.block("国Ag", size, line, weight, 0, 1)
	return max(h, line)
}
func (m measurer) width(s string, size float32, weight int) float32 {
	sp, _ := emojiSpans(m.emoji, s, size, size)
	w, _ := ui.ShapeRichText(sp, ui.Font{Family: m.family, Size: size, Weight: weight, Features: punctTrim}, 0).Size()
	return w
}

func badgeKind(v string) string {
	switch v {
	case "blue", "business", "government":
		return v
	}
	return ""
}

func mediaRatio(m TweetMedia) float32 {
	r := m.Ratio
	if r <= 0 && m.Image != nil {
		if w, h := m.Image.Size(); w > 0 {
			r = float64(h) / float64(w)
		}
	}
	if r <= 0 {
		r = float64(multiRatio)
	}
	return float32(r)
}

// grid 照 X 的 1～4 张网格排：一张整幅；两张左右；三张左边一整列、右边上下两格；四张及以上 2×2，第四格写 +N
func grid(media []TweetMedia, x, y, w float32, singleMax float32, scale float32) (float32, []cell) {
	n := len(media)
	if n == 0 {
		return 0, nil
	}
	var h float32
	if n == 1 {
		h = w * min(max(mediaRatio(media[0]), 0.5), singleMax)
	} else {
		h = w * multiRatio
	}
	h = float32(math.Round(float64(h * scale)))
	half := (w - mediaGap) / 2
	halfH := (h - mediaGap) / 2
	at := func(i int, b box) cell { return cell{box: b, m: media[i]} }
	var cells []cell
	switch n {
	case 1:
		cells = []cell{at(0, box{x, y, w, h})}
	case 2:
		cells = []cell{at(0, box{x, y, half, h}), at(1, box{x + half + mediaGap, y, half, h})}
	case 3:
		cells = []cell{at(0, box{x, y, half, h}), at(1, box{x + half + mediaGap, y, half, halfH}), at(2, box{x + half + mediaGap, y + halfH + mediaGap, half, halfH})}
	default:
		cells = []cell{at(0, box{x, y, half, halfH}), at(1, box{x + half + mediaGap, y, half, halfH}), at(2, box{x, y + halfH + mediaGap, half, halfH}), at(3, box{x + half + mediaGap, y + halfH + mediaGap, half, halfH})}
		cells[3].more = n - 4
	}
	return h, cells
}

// fit 是为了塞进 960 像素高而收的量：正文最多几行、引用正文最多几行、媒体高度乘多少
type fit struct {
	body, quote int
	media       float32
	long        bool // 不封顶：按长截图排（段落间距、配图夹在段落里），不收行数
}

func layoutTweet(t Tweet, m measurer, f fit) tweetLayout {
	var l tweetLayout
	left := cardBorder + padX
	cw := tweetW - 2*left
	y := cardBorder + padY
	l.avatar = box{left, y, avatarSize, avatarSize}
	ax := left + avatarSize + authorGap
	maxName := cw - avatarSize - 2*authorGap // .author max-width: calc(100% - 84px)，84 里含右边的 X 标，这里不画那个标
	l.badgeKind = badgeKind(t.Author.Verification)
	nameMax := maxName
	if l.badgeKind != "" {
		nameMax -= badgeSize + badgeGap
	}
	l.nameText = strings.TrimSpace(t.Author.Name)
	l.handleText = ""
	if a := strings.TrimSpace(t.Author.Account); a != "" {
		l.handleText = "@" + a
	}
	hl := m.lineHeight(headerSize, headerLine, 700)
	nameW := min(m.width(l.nameText, headerSize, 700), nameMax)
	top := y + (avatarSize-2*hl)/2
	l.name = box{ax, top, nameW, hl}
	if l.badgeKind != "" {
		l.badge = box{ax + nameW + badgeGap, top + (hl-badgeSize)/2, badgeSize, badgeSize}
	}
	l.handle = box{ax, top + hl, min(m.width(l.handleText, headerSize, 400), maxName), hl}
	y += avatarSize + headerPB

	if f.long {
		blocks := t.flowBlocks()
		flow, ey := layoutFlow(blocks, m, left, cw, y)
		l.flow = flow
		var texts []string
		for _, fb := range flow {
			if fb.kind == FlowPara || fb.kind == FlowHeading {
				texts = append(texts, fb.text)
				l.bodyLines += fb.lines
			}
		}
		l.bodyText = strings.Join(texts, "\n\n")
		l.bodyAll = l.bodyLines
		l.body = box{left, y, cw, ey - y}
		y = ey
		y = l.layoutNote(t, m, left, cw, y)
		if len(flow) > 0 {
			y += paraGap // 内容和下面的时间、互动之间留半行
		}
		if len(t.Flow) == 0 && len(t.Media) > 0 { // 没有按段落排的配图：和封顶时一样接在文字下面
			y += mediaMT
			h, cells := grid(t.Media, left+cardBorder, y+cardBorder, cw-2*cardBorder, 1, f.media)
			l.media = box{left, y, cw, h + 2*cardBorder}
			l.cells = cells
			y += l.media.H
		}
	} else {
		shown, n, bh, all := m.blockFit(t.Text, bodySize, bodyLine, 400, cw, f.body)
		l.bodyText, l.bodyLines, l.bodyAll = shown, n, all
		l.body = box{left, y, cw, bh}
		y += l.body.H
		y = l.layoutNote(t, m, left, cw, y)

		if len(t.Media) > 0 {
			y += mediaMT
			h, cells := grid(t.Media, left+cardBorder, y+cardBorder, cw-2*cardBorder, 1, f.media)
			l.media = box{left, y, cw, h + 2*cardBorder}
			l.cells = cells
			y += l.media.H
		}
	}
	y = l.layoutPoll(t.Poll, m, left, cw, y)
	if t.Quote != nil || t.QuoteGone != "" {
		y += quoteMY
		qy := y
		qx := left + cardBorder + quotePad
		qw := cw - 2*cardBorder - 2*quotePad
		y += cardBorder + quotePad
		if t.Quote == nil {
			l.qGone = t.QuoteGone
			gh := m.lineHeight(quoteBodySize, quoteBodyLine, 400)
			l.qBody = box{qx, y, qw, gh}
			l.qBodyLines = 1
			y += gh + quotePad
		} else {
			q := t.Quote
			l.qAvatar = box{qx, y, quoteAvatar, quoteAvatar}
			nx := qx + quoteAvatar + authorGap
			l.qBadgeKind = badgeKind(q.Author.Verification)
			l.qNameText = strings.TrimSpace(q.Author.Name)
			if a := strings.TrimSpace(q.Author.Account); a != "" {
				l.qHandleText = "@" + a
			}
			room := qw - quoteAvatar - authorGap
			if l.qBadgeKind != "" {
				room -= badgeSize + badgeGap
			}
			qn := min(m.width(l.qNameText, headerSize, 700), room*0.7)
			// 引用头一行 20 高（--tweet-header-line-height），头像 20；字的实际行高更高时，头像跟着这一行竖直居中
			l.qName = box{nx, y, qn, hl}
			l.qAvatar.Y = y + (hl-quoteAvatar)/2
			hx := nx + qn + 2 // .username margin-left 0.125rem
			if l.qBadgeKind != "" {
				l.qBadge = box{nx + qn + badgeGap, y + (hl-badgeSize)/2, badgeSize, badgeSize}
				hx += badgeSize + badgeGap
			}
			l.qHandle = box{hx, y, max(0, qx+qw-hx), hl}
			y += hl
			qshown, qn2, qbh, qall := m.blockFit(q.Text, quoteBodySize, quoteBodyLine, 400, qw, f.quote)
			l.qBodyText, l.qBodyLines, l.qBodyAll = qshown, qn2, qall
			if qn2 > 0 {
				y += quoteBodyMT
				l.qBody = box{qx, y, qw, qbh}
				y += l.qBody.H + quoteBodyMB
			} else {
				y += quoteBodyMB
			}
			if len(q.Media) > 0 {
				iw := cw - 2*cardBorder
				h, cells := grid(q.Media, left+cardBorder, y, iw, multiRatio, f.media)
				l.qMedia = box{left + cardBorder, y, iw, h}
				l.qCells = cells
				y += h
			}
		}
		y += cardBorder
		l.quote = box{left, qy, cw, y - qy}
		y += quoteMY
	}
	y += infoMT
	l.timeText = beijingStamp(t.Published)
	if t.Views.OK {
		l.viewsText = CompactCount(t.Views.N)
	}
	if l.timeText != "" || l.viewsText != "" {
		ih := m.lineHeight(infoSize, infoLine, 400)
		l.timeBox = box{left, y, cw, ih}
		y += ih
	}
	y += actionsMT
	l.actionsLine = box{left, y, cw, cardBorder}
	y += cardBorder + actionsPT
	// 互动排照 X 网页的操作栏两端对齐、等距：第一项的图标框左移 4（.xxxIconWrapper margin-left -0.25rem），
	// 最后一项的数字右端贴正文右边，中间的空隙一样大；只有一项时靠左
	var widths []float32
	for _, a := range []struct {
		icon string
		s    Stat
	}{{"reply", t.Replies}, {"repost", t.Reposts}, {"like", t.Likes}, {"views", t.Views}} {
		if !a.s.OK {
			continue
		}
		txt := CompactCount(a.s.N)
		l.actions = append(l.actions, action{icon: a.icon, text: txt})
		widths = append(widths, actionWrap+actionTextML+m.width(txt, actionSize, 700))
	}
	start, end := left-4, left+cw
	var gap, sum float32
	for _, w := range widths {
		sum += w
	}
	if n := len(widths); n > 1 {
		gap = (end - start - sum) / float32(n-1)
	}
	x := start
	for i := range l.actions {
		l.actions[i].at = box{x, y, actionWrap, actionWrap}
		l.actions[i].tx = x + actionWrap + actionTextML
		l.actions[i].w = widths[i]
		x += widths[i] + gap
	}
	y += actionWrap + padY + cardBorder
	l.H = y
	return l
}

// fitTweet 先按全文排；超过 capPx 像素高就依次收：正文行数（最少 3 行）、引用正文（最少 2 行）、媒体高度（最少一半）。
// capPx 为 0 是不封顶：按长截图排（fit.long），整张画出来；长到超过硬上限 TweetHardMaxHeight 才改按硬上限收（正文截在标点后）。
func fitTweet(t Tweet, m measurer, capPx int) (tweetLayout, error) {
	if capPx == 0 {
		if l := layoutTweet(t, m, fit{media: 1, long: true}); l.H <= float32(TweetHardMaxHeight)/tweetScale {
			return l, nil
		}
		capPx = TweetHardMaxHeight
	}
	maxH := float32(capPx) / tweetScale
	f := fit{media: 1}
	l := layoutTweet(t, m, f)
	if l.H <= maxH {
		return l, nil
	}
	for l.H > maxH && l.bodyLines > minBodyLines {
		per := l.body.H / float32(l.bodyLines)
		over := int(math.Ceil(float64((l.H - maxH) / per)))
		f.body = max(minBodyLines, l.bodyLines-max(1, over))
		l = layoutTweet(t, m, f)
	}
	for l.H > maxH && l.qBodyLines > minQuoteLines && l.qGone == "" {
		f.quote = l.qBodyLines - 1
		l = layoutTweet(t, m, f)
	}
	for l.H > maxH && f.media > 0.5 {
		f.media -= 0.05
		l = layoutTweet(t, m, f)
	}
	if l.H > maxH {
		return l, fmt.Errorf("推特卡片收到最小也排不进%d像素", capPx)
	}
	return l, nil
}

// infoText 是时间行的全部文字
func (l tweetLayout) infoText() string {
	switch {
	case l.viewsText == "":
		return l.timeText
	case l.timeText == "":
		return l.viewsText + " 查看"
	}
	return l.timeText + " · " + l.viewsText + " 查看"
}

// CompactCount 照 X 中文界面写互动数：一万以下原样，万、亿各留一位小数
func CompactCount(n int64) string {
	switch {
	case n >= 100_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e8)) + "亿"
	case n >= 10_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1e4)) + "万"
	}
	return fmt.Sprint(n)
}
func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// beijingStamp 照 X 中文界面写时间：下午6:30 · 2026年11月1日（北京时间）；没有时间返回空串
func beijingStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	b := t.In(beijing)
	half := "上午"
	if b.Hour() >= 12 {
		half = "下午"
	}
	h := b.Hour() % 12
	if h == 0 {
		h = 12
	}
	return fmt.Sprintf("%s%d:%02d · %d年%d月%d日", half, h, b.Minute(), b.Year(), int(b.Month()), b.Day())
}

// Semantic 是卡片上画出来的全部文字，宿主拿去过文字守卫、做图片的说明
func (t Tweet) Semantic() string {
	parts := []string{strings.TrimSpace(t.Author.Name + " @" + t.Author.Account), t.Text}
	if t.TranslationNote != "" && len(t.Flow) == 0 {
		parts = append(parts, t.TranslationNote) // 卡片上画出来的字都过文字守卫
	}
	if t.Poll != nil { // 投票上画出来的字也要过文字守卫
		for _, c := range t.Poll.Choices {
			parts = append(parts, c.Label)
		}
		parts = append(parts, t.Poll.Footer)
	}
	for _, b := range t.Flow { // 长文里画出来的字也要过文字守卫
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if t.Quote != nil {
		parts = append(parts, strings.TrimSpace(t.Quote.Author.Name+" @"+t.Quote.Author.Account), t.Quote.Text)
	} else if t.QuoteGone != "" {
		parts = append(parts, t.QuoteGone)
	}
	return strings.Join(parts, "\n")
}

// drawBadge 画认证标：八瓣的圆章加白色对勾，自己算的矢量
func drawBadge(p *ui.Painter, r ui.Rect, c ui.Color) {
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	R := r.W / 2
	var path ui.Path
	const steps = 96
	for i := 0; i <= steps; i++ {
		a := float64(i) / steps * 2 * math.Pi
		rr := R * float32(0.9+0.1*math.Cos(8*a))
		x, y := cx+rr*float32(math.Cos(a)), cy+rr*float32(math.Sin(a))
		if i == 0 {
			path.MoveTo(x, y)
		} else {
			path.LineTo(x, y)
		}
	}
	path.Close()
	p.FillPath(&path, c)
	var check ui.Path
	check.MoveTo(cx-R*0.38, cy+R*0.02).LineTo(cx-R*0.1, cy+R*0.3).LineTo(cx+R*0.4, cy-R*0.28)
	check.Cap(ui.RoundCap).Join(ui.RoundJoin)
	p.StrokePath(&check, R*0.2, ui.RGB(255, 255, 255))
}

func drawPlay(p *ui.Painter, r ui.Rect, c ui.Color) {
	d := min(r.W, r.H, 52)
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	p.Fill(ui.Rect{X: cx - d/2, Y: cy - d/2, W: d, H: d}, c, d/2)
	var tri ui.Path
	tri.MoveTo(cx-d*0.14, cy-d*0.22).LineTo(cx+d*0.24, cy).LineTo(cx-d*0.14, cy+d*0.22).Close()
	p.FillPath(&tri, ui.RGB(255, 255, 255))
}

func renderTweet(t Tweet, fs *fontSet, theme string, capPx int) (Output, error) {
	m := fs.measurer()
	l, err := fitTweet(t, m, capPx)
	if err != nil {
		return Output{}, err
	}
	col := colorsFor(theme)
	badgeColor := func(kind string) ui.Color {
		switch kind {
		case "business":
			return col.badgeGold
		case "government":
			return col.badgeGray
		}
		return col.badgeBlue
	}
	var fields []FieldLayout
	field := func(name, text string, b box) {
		if b.W <= 0 && b.H <= 0 {
			return
		}
		px := func(v float32) int { return int(math.Round(float64(v * tweetScale))) }
		fields = append(fields, FieldLayout{Name: name, Text: text, X: px(b.X), Y: px(b.Y), W: px(b.X+b.W) - px(b.X), H: px(b.Y+b.H) - px(b.Y)})
	}
	h := l.H
	img := ui.Render(func(c *ui.Context) {
		fields = fields[:0]
		ui.Box(c).Fill().Background(col.bg)
		ui.Box(c).Absolute().Left(0).Top(0).Size(tweetW, h).Radius(frameRadius).Border(cardBorder, col.border)
		text := func(s string, b box, size, line float32, weight int, lines int, color ui.Color) {
			if s == "" || lines <= 0 {
				return
			}
			sp, objs := emojiSpans(m.emoji, s, size, line)
			e := ui.RichText(c, sp...).Absolute().Left(b.X).Top(b.Y).Size(b.W+1, b.H).Font(fs.family).FontSize(size).FontWeight(weight).FontFeatures(punctTrim).FixedLineHeight(line).MaxLines(lines).TextColor(color)
			if lines == 1 {
				e.NoWrap()
			}
			if len(objs) > 0 {
				e.DrawObjects(drawEmoji(objs, size))
			}
		}
		image := func(bmp *ui.Bitmap, b box, radius float32) {
			ui.Box(c).Absolute().Left(b.X).Top(b.Y).Size(b.W, b.H).Radius(radius).Clip().Background(col.placeholder).Children(func() {
				if bmp != nil {
					ui.Image(c, bmp).Fill().Fit(ui.Cover)
				}
			})
		}
		badge := func(kind string, b box) {
			if kind == "" {
				return
			}
			bc := badgeColor(kind)
			ui.Box(c).Absolute().Left(b.X).Top(b.Y).Size(b.W, b.H).Draw(func(p *ui.Painter, r ui.Rect) { drawBadge(p, r, bc) })
		}
		cells := func(cs []cell) {
			for _, ce := range cs {
				image(ce.m.Image, ce.box, 0)
				if ce.m.Image == nil {
					ui.Text(c, "图片未加载").Absolute().Left(ce.X).Top(ce.Y+ce.H/2-10).Size(ce.W, 20).Font(fs.family).FontSize(14).TextAlign(ui.Center).TextColor(col.secondary)
				}
				if ce.m.Video {
					play := col.blue
					ui.Box(c).Absolute().Left(ce.X).Top(ce.Y).Size(ce.W, ce.H).Draw(func(p *ui.Painter, r ui.Rect) { drawPlay(p, r, play) })
				}
				if ce.more > 0 {
					ui.Box(c).Absolute().Left(ce.X).Top(ce.Y).Size(ce.W, ce.H).Background(ui.RGBA(0, 0, 0, 0.45))
					ui.Text(c, fmt.Sprintf("+%d", ce.more)).Absolute().Left(ce.X).Top(ce.Y+ce.H/2-16).Size(ce.W, 32).Font(fs.family).FontSize(26).FontWeight(700).TextAlign(ui.Center).TextColor(ui.RGB(255, 255, 255))
				}
			}
		}

		image(t.Author.Avatar, l.avatar, avatarSize/2)
		field("avatar", "", l.avatar)
		text(l.nameText, l.name, headerSize, headerLine, 700, 1, col.text)
		field("name", l.nameText, l.name)
		badge(l.badgeKind, l.badge)
		if l.badgeKind != "" {
			field("badge", l.badgeKind, l.badge)
		}
		text(l.handleText, l.handle, headerSize, headerLine, 400, 1, col.secondary)
		field("handle", l.handleText, l.handle)
		if len(l.flow) > 0 {
			for i, fb := range l.flow {
				switch fb.kind {
				case FlowPara:
					text(fb.text, fb.box, bodySize, bodyLine, 400, fb.lines, col.text)
				case FlowHeading:
					text(fb.text, fb.box, headingSize, headingLine, 700, fb.lines, col.text)
				case FlowRule:
					ui.Box(c).Absolute().Left(fb.box.X).Top(fb.box.Y).Size(fb.box.W, fb.box.H).Background(col.border)
				case FlowImage:
					mb := fb.box
					inner := cell{box: box{mb.X + cardBorder, mb.Y + cardBorder, mb.W - 2*cardBorder, mb.H - 2*cardBorder}, m: fb.m}
					ui.Box(c).Absolute().Left(mb.X).Top(mb.Y).Size(mb.W, mb.H).Radius(frameRadius).Clip().Background(col.border).Children(func() {
						inner.X -= mb.X
						inner.Y -= mb.Y
						cells([]cell{inner})
					})
					ui.Box(c).Absolute().Left(mb.X).Top(mb.Y).Size(mb.W, mb.H).Radius(frameRadius).Border(cardBorder, col.border)
					field(fmt.Sprintf("flow-%d", i), string(fb.kind), mb)
				}
			}
			if l.bodyLines > 0 {
				field("body", l.bodyText, l.body)
			}
		} else {
			text(l.bodyText, l.body, bodySize, bodyLine, 400, l.bodyLines, col.text)
			if l.bodyLines > 0 {
				field("body", l.bodyText, l.body)
			}
		}
		if l.noteText != "" {
			text(l.noteText, l.note, noteSize, noteLine, 400, 1, col.secondary)
			field("translation-note", l.noteText, l.note)
		}
		if len(l.cells) > 0 {
			mb := l.media
			ui.Box(c).Absolute().Left(mb.X).Top(mb.Y).Size(mb.W, mb.H).Radius(frameRadius).Clip().Background(col.border).Children(func() {
				for _, ce := range l.cells {
					ce.X -= mb.X
					ce.Y -= mb.Y
					cells([]cell{ce})
				}
			})
			ui.Box(c).Absolute().Left(mb.X).Top(mb.Y).Size(mb.W, mb.H).Radius(frameRadius).Border(cardBorder, col.border)
			field("media", fmt.Sprint(len(t.Media)), mb)
			for i, ce := range l.cells {
				field(fmt.Sprintf("media-%d", i), "", ce.box)
			}
		}
		if l.poll.H > 0 {
			barLead, barRest := col.blue.Alpha(0.35), col.border
			for _, pr := range l.pollRows {
				pr := pr
				ui.Box(c).Absolute().Left(pr.box.X).Top(pr.box.Y).Size(pr.box.W, pr.box.H).Radius(pollRadius).Clip().Background(col.placeholder).Children(func() {
					if pr.fill > 0 {
						bar := barRest
						if pr.lead {
							bar = barLead
						}
						ui.Box(c).Absolute().Left(0).Top(0).Size(pr.fill, pr.box.H).Background(bar)
					}
				})
				lh := m.lineHeight(pollSize, 20, 400)
				weight := 400
				if pr.lead {
					weight = 700
				}
				ty := pr.box.Y + (pr.box.H-lh)/2
				text(pr.label, box{pr.box.X + pollPadX, ty, pr.box.W - 2*pollPadX - 56, lh}, pollSize, 20, weight, 1, col.text)
				if pr.pct != "" {
					ui.Text(c, pr.pct).Absolute().Left(pr.box.X+pr.box.W-pollPadX-56).Top(ty).Size(56, lh).Font(fs.family).FontSize(pollSize).FontWeight(weight).FixedLineHeight(20).MaxLines(1).NoWrap().TextAlign(ui.End).TextColor(col.text)
				}
			}
			field("poll", fmt.Sprint(len(l.pollRows)), l.poll)
			if l.pollFootText != "" {
				text(l.pollFootText, l.pollFoot, pollSize, 20, 400, 1, col.secondary)
			}
		}
		if l.quote.H > 0 {
			qb := l.quote
			ui.Box(c).Absolute().Left(qb.X).Top(qb.Y).Size(qb.W, qb.H).Radius(frameRadius).Clip().Children(func() {
				if len(l.qCells) > 0 {
					for _, ce := range l.qCells {
						ce.X -= qb.X
						ce.Y -= qb.Y
						cells([]cell{ce})
					}
				}
			})
			ui.Box(c).Absolute().Left(qb.X).Top(qb.Y).Size(qb.W, qb.H).Radius(frameRadius).Border(cardBorder, col.border)
			field("quote", "", qb)
			if l.qGone != "" {
				text(l.qGone, l.qBody, quoteBodySize, quoteBodyLine, 400, 1, col.secondary)
				field("quote-gone", l.qGone, l.qBody)
			} else {
				image(t.Quote.Author.Avatar, l.qAvatar, quoteAvatar/2)
				field("quote-avatar", "", l.qAvatar)
				text(l.qNameText, l.qName, headerSize, headerLine, 700, 1, col.text)
				field("quote-name", l.qNameText, l.qName)
				badge(l.qBadgeKind, l.qBadge)
				if l.qBadgeKind != "" {
					field("quote-badge", l.qBadgeKind, l.qBadge)
				}
				text(l.qHandleText, l.qHandle, headerSize, headerLine, 400, 1, col.secondary)
				text(l.qBodyText, l.qBody, quoteBodySize, quoteBodyLine, 400, l.qBodyLines, col.text)
				if l.qBodyLines > 0 {
					field("quote-body", l.qBodyText, l.qBody)
				}
				if len(l.qCells) > 0 {
					field("quote-media", fmt.Sprint(len(t.Quote.Media)), l.qMedia)
				}
			}
		}
		if l.timeBox.H > 0 {
			// 照 X 网页单条推文页：下午5:45 · 2026年11月12日 · 182 查看，浏览数加粗、用正文色，其余次要色
			var sp []ui.Span
			if l.timeText != "" {
				sep := ""
				if l.viewsText != "" {
					sep = " · "
				}
				sp = append(sp, ui.Span{Text: l.timeText + sep})
			}
			if l.viewsText != "" {
				sp = append(sp, ui.Span{Text: l.viewsText, Weight: 700, Color: col.text}, ui.Span{Text: " 查看"})
			}
			b := l.timeBox
			ui.RichText(c, sp...).Absolute().Left(b.X).Top(b.Y).Size(b.W+1, b.H).Font(fs.family).FontSize(infoSize).FontWeight(400).FixedLineHeight(infoLine).MaxLines(1).NoWrap().TextColor(col.secondary)
			field("time", l.infoText(), l.timeBox)
		}
		ui.Box(c).Absolute().Left(l.actionsLine.X).Top(l.actionsLine.Y).Size(l.actionsLine.W, l.actionsLine.H).Background(col.border)
		icons := actionIcons()
		for _, a := range l.actions {
			ib := box{a.at.X + (actionWrap-actionIcon)/2, a.at.Y + (actionWrap-actionIcon)/2, actionIcon, actionIcon}
			ui.Icon(c, icons[a.icon]).Absolute().Left(ib.X).Top(ib.Y).Size(ib.W, ib.H).TextColor(col.secondary)
			ah := m.lineHeight(actionSize, 16, 700)
			tb := box{a.tx, a.at.Y + (actionWrap-ah)/2, a.w - (a.tx - a.at.X), ah}
			text(a.text, tb, actionSize, 16, 700, 1, col.secondary)
			field("action-"+a.icon, a.text, box{a.at.X, a.at.Y, a.w, actionWrap})
		}
	}, int(tweetW), int(math.Ceil(float64(h))), tweetScale)
	out := Output{Layout: fields}
	ph := min(img.Bounds().Dy(), int(math.Round(float64(h*tweetScale))))
	if ph > max(capPx, TweetHardMaxHeight) {
		return Output{}, errors.New("卡片高度超限")
	}
	if err := encode(&out, img, ph); err != nil {
		return Output{}, err
	}
	return out, nil
}

// layoutNote 在正文下面排“翻译自……”一行；没有译文、或者按段落排的长文（Flow）不画。返回新的 y。
func (l *tweetLayout) layoutNote(t Tweet, m measurer, left, cw, y float32) float32 {
	note := strings.TrimSpace(t.TranslationNote)
	if note == "" || len(t.Flow) > 0 || strings.TrimSpace(t.Text) == "" {
		return y
	}
	y += noteMT
	h := m.lineHeight(noteSize, noteLine, 400)
	l.note, l.noteText = box{left, y, cw, h}, note
	return y + h
}
