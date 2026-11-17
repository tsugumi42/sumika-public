// Package linkexpand 把共享的 Result 编排为固定消息，不取数、不发送、不调用模型。
package linkexpand

import (
	"fmt"
	"math"
	"sort"
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

const NodeName = "link-reader（外部内容）"

var displayZone = time.FixedZone("+08:00", 8*60*60)

type Options struct {
	// Templates 是配置里各站点的模板（键 <站点> 或 <站点>-forward，值可写 @预设名）；缺省用内置默认
	Templates map[string]string
	// Forward 是合并转发的节点名和卡片文字；零值是 QQ 默认
	Forward ForwardOptions
	// Sites 是各站点的转发和简介设置，键是站点键
	Sites        map[string]SiteSetting
	VideoEnabled bool
	// ForwardVideo：容器是合并转发时把视频作为节点放进转发（linkReader.expand.send.forwardVideo，规格-链接发送改版 A6）。注意零值是“视频拆出转发、单独一条”（B3），不是旧行为：新写 Options{} 的调用方要显式设它
	ForwardVideo bool
	ForwardMedia types.MediaCaps
	// PanoramaOriginals：X 拆图拼回成功后，把原图放进合并转发附在后面（linkReader.expand.panoramaOriginals，默认关）
	PanoramaOriginals bool
	// Translation 是帖子正文的译文（规格-链接翻译）；空串表示没有，模板里的 $译文 整段不出现。
	// 送去翻译的是“正文加投票选项（一个选项一行）”，模型的回复原样放这里；Build 开头按行数把末尾的选项行拆出来（splitPollTranslation）。
	Translation string
	// QuoteTranslation 是被引推文正文的译文（2026-11-14 用户：被引推文也翻，同样只放译文）；空串表示没有，被引推文照旧写原文。
	QuoteTranslation string
	// ArticleTranslation 是 X 长文的译文（2026-11-14）：长文的文字块和小标题按顺序用 linktranslate.SectionMarker 单独一行隔开，整段送去翻译，
	// 回复原样放这里；段数和原来对不上就整篇不用（articleTranslated）。空串表示没有。
	ArticleTranslation string
	// QuoteArticleTranslation 是被引的 X 长文（推文引用了 Article）的译文，格式同 ArticleTranslation（ADR-391）。
	QuoteArticleTranslation string
	// Translations 由宿主从翻译结果填进来（ADR-391）：主文、引用、长文、被引长文、嵌入推文各段。
	// 旧字段 Translation / QuoteTranslation / ArticleTranslation 非空时优先（测试和预览直接填它们）。
	Translations Translations
	// pollLabels 是投票选项的译文，和 pollKey 那个投票的 Choices 一一对应（空串表示这个选项没有译文）；Build 开头从 Translation 里拆出来。
	// 只对 pollKey 指向的主推文投票生效，被引推文自己的投票不动。
	pollLabels []string
	pollKey    *linkreader.PollInfo
	// embeds 是嵌入推文和被引推文的译文表（tweetKey → 译文），Build 开头从 Translations 建好；不导出、不进 JSON。
	embeds map[string]Embedded
	// KeepTrailingHashtags：不去帖子结尾的 hashtag 块（各平台）（linkReader.expand.text.stripTrailingHashtags 关掉时为真）。
	// 零值是默认的“去掉”，所以没接配置的调用方（预览、测试）和线上默认一致。
	KeepTrailingHashtags bool
	// Artist 是这条链接命中了确认过的画师时的发法（规格-画师原图）：ArtistCardAndOriginals、ArtistOriginalsOnly；
	// 空串是旧行为（零值即不特殊处理），和 Translation 一样每条链接不同、由调用方填，BuildOptions 不填它。
	Artist string
	// DirectImageMax 是直接发的图数上限（linkReader.expand.directImageMax，1～9），X、pixiv 作品、Telegram、B 站动态超过就走合并转发；
	// 0 按默认 DefaultDirectImageMax。
	DirectImageMax int
}

// 画师的发法（Options.Artist 的取值）
const (
	ArtistCardAndOriginals = "cardAndOriginals" // 卡片、链接、原图
	ArtistOriginalsOnly    = "originalsOnly"    // 链接、原图
)

// DefaultDirectImageMax 是直接发的图数上限的默认值（规格-链接发送改版 B3：图和文字挤在同一条消息里，最多 3 张）。
const DefaultDirectImageMax = 3

func (o Options) directImageMax() int {
	if o.DirectImageMax > 0 {
		return o.DirectImageMax
	}
	return DefaultDirectImageMax
}

type Item struct {
	Kind, Text string
	Media      linkreader.Media
	Card       *linkcard.Data
	Ugoira     *linkreader.UgoiraInfo
	Standalone bool
	// Optional：这张图是锦上添花（个人主页的头像），下不下来都不写说明、也不算“媒体全没取到”（零值即旧行为）。
	Optional bool `json:",omitempty"`
	// PanoramaN 大于 0：这是 X 拆图候选组的第一张，组里共 N 张（它和后面 N-1 个图片项）；Prepare 下载后再用像素判断能不能拼回（panorama.go）。
	// PanoramaKeep：拼成功后还要把原图附在合并转发里，所以不能释放。PanoramaOrig：这是附在后面的原图，拼成功才发。
	// Translation、TranslationNote 只在 X 的推特卡片项上有（规格-链接发送改版 B12 第 3 条）：卡片只显示译文，
	// 不画原文，正文下面加一行小字“翻译自日语”（源语言认不出写“翻译自外语”）。没有译文是空串，零值即旧行为。
	Translation     string `json:",omitempty"`
	TranslationNote string `json:",omitempty"`
	// PollLabels：卡片上投票选项的译文（和投票的 Choices 一一对应，空串是没有）；QuoteTranslation：卡片引用框里被引推文的译文，引用框不画原文
	PollLabels       []string `json:",omitempty"`
	QuoteTranslation string   `json:",omitempty"`
	PanoramaN        int      `json:",omitempty"`
	PanoramaKeep     bool     `json:",omitempty"`
	PanoramaOrig     bool     `json:",omitempty"`
}
type Node struct {
	Name string
	// Fixed 为真：Name 是设置里写死的显示名，原样用；默认（假）是发送时换成她自己的 QQ 昵称，取不到才用 Name。
	// 做成“写死”而不是“用昵称”，是为了零值就是默认行为，对拍基线不用重录。
	Fixed bool `json:",omitempty"`
	Body  []Item
	// QuoteOf 是这个节点引用的同一转发里另一节点的下标加 1（引用推文，规格-链接发送改版 A7）；0 表示没有，零值即旧行为
	QuoteOf int `json:",omitempty"`
	// noteEnd 标出转发里头部那组文字拆完后的最后一个节点：“发过 N 次”“还有 N 条没处理”这类提示接在它后面。
	// 只在 Build 到 AppendNote 之间的内存里用，不进 JSON；头部正文超过节点字节上限拆成几个节点时，提示不会落进正文中间
	noteEnd bool
}
type Part struct {
	Kind, Quote string
	Body        []Item
	Nodes       []Node
	// Card 是合并转发卡片外面的文字，只在 Kind 为 forward 时有
	Card *types.ForwardCard `json:",omitempty"`
	// 下面几项只在 Build 到 Prepare 之间的内存里用，不进 JSON（shape.go）：
	// imageCap 大于 0 时，直接发的这一条在 Prepare 后图超过它（X 拆图没拼成）就按 capForward 改成合并转发（节点名、卡片文字
	// 用 capNode、capCard），否则只留前 imageCap 张并标“（发出/总数）”；fitDirect 的合并转发在 Prepare 后图不超过 imageCap、没有视频就收回成一条消息。
	imageCap int
	// imageTotal 大于 0 说明这条消息因站点设成从不合并转发、图多于上限而只留了前几张：原来一共几张，Prepare 标“（发出/总数）”
	imageTotal int
	capForward bool
	fitDirect  bool
	capNode    Node
	capCard    *types.ForwardCard
	// nodeBytes 是 Prepare 阶段重新排节点时的文字上限（字节，和转发门槛同一个配置值，ADR-391）；0 按默认 16500
	nodeBytes int
}
type Draft struct {
	Parts    []Part
	CleanURL string
	// NoContent 为真表示取到了页面但标题、正文都没有可发的内容：不发任何文字，调用方当失败处理（群里换失败表情）
	NoContent bool `json:",omitempty"`
	// nodeBytes 是合并转发一个节点的文字上限（UTF-8 字节，ADR-391），AppendNote 拆节点时用；0 按默认 16500
	nodeBytes int
}

func flagBool(r *linkreader.Result, key string) (bool, bool) {
	f := r.Flags[key]
	v, ok := f.Value.(bool)
	return v, ok && f.State == "available"
}
func Restriction(r *linkreader.Result) (int, bool) {
	f := r.Flags["x_restrict"]
	if f.State != "available" {
		return 0, false
	}
	n := -1
	switch v := f.Value.(type) {
	case int:
		n = v
	case int64:
		if v >= 0 && v <= 2 {
			n = int(v)
		}
	case float64:
		if v == math.Trunc(v) && v >= 0 && v <= 2 {
			n = int(v)
		}
	}
	return n, n >= 0 && n <= 2
}

func text(s string) Item { return Item{Kind: types.BodyText, Text: s} }
func field(f linkreader.Field[string]) string {
	if f.State == "available" && f.Value != "" {
		return f.Value
	}
	return "未返回"
}
func number(f linkreader.Field[int64]) string {
	if f.State == "available" {
		return strconv.FormatInt(f.Value, 10)
	}
	return "未返回"
}

// stamp 是发出去的时间写法，各平台统一成推文那种（用户 2026-11-12）：今年的写“11-07 23:28”，往年的带年份，不写时区。
func stamp(f linkreader.Field[time.Time]) string {
	if f.State != "available" {
		return "未返回"
	}
	t := f.Value.In(displayZone)
	if t.Year() == nowFunc().In(displayZone).Year() {
		return t.Format("01-02 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

// nowFunc 是判断“今年”用的当前时间；基线测试把它钉在固定日子，过了元旦基线也不变。
var nowFunc = time.Now

func duration(r *linkreader.Result) string {
	if r.SelectedPart.State == "available" && r.SelectedPart.Value.Duration.State == "available" {
		return clock(r.SelectedPart.Value.Duration.Value)
	}
	for _, m := range r.Media.Value {
		if m.Type == "video" || m.Type == "audio" {
			if m.Duration.State == "available" {
				return clock(int(m.Duration.Value))
			}
		}
	}
	return "未返回"
}

// clock 把秒数写成 mm:ss，满一小时写成 h:mm:ss（不按天进位：多 P 合集的总长可以是 46:22:22）。
func clock(n int) string {
	if n >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", n/3600, n%3600/60, n%60)
	}
	return fmt.Sprintf("%02d:%02d", n/60, n%60)
}

// compact 是大数字的缩写：不到一万原样；一万以上写成“297.5万”（一位小数）；一亿以上写成“1.08亿”（两位小数）；
// 小数末尾的 0 去掉（“10万”不写“10.0万”）。四舍五入后凑满一亿的（9999.99 万）直接进到亿。
func compact(n int64) string {
	trim := func(f float64, digits int, unit string) string {
		s := strconv.FormatFloat(f, 'f', digits, 64)
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		return s + unit
	}
	neg := ""
	if n < 0 {
		neg, n = "-", -n
	}
	switch {
	case n >= 100_000_000:
		return neg + trim(float64(n)/1e8, 2, "亿")
	case n >= 10_000:
		w := math.Round(float64(n)/1e3) / 10 // 万，保留一位小数
		if w >= 10_000 {
			return neg + trim(float64(n)/1e8, 2, "亿")
		}
		return neg + trim(w, 1, "万")
	}
	return neg + strconv.FormatInt(n, 10)
}

// textSep 是文字版里我们自己加的分隔（半角竖线两边各一个空格，用户 2026-11-12 确认）；cardSep 是卡片图上的分隔，不变。
// 标题等原始内容自带的竖线不经过这里，原样保留。
const (
	textSep = " | "
	cardSep = "｜"
)

// stats 把取得到的统计项用 sep 连成一行；取不到的不写（不再写“未返回”），一项也没有就返回空串。
// 数字一律缩写（用户 2026-11-12：同一套发送格式里各站一样，不到一万原样，以上写万、亿），同 statsCompact。
func stats(r *linkreader.Result, keys, labels []string, sep string) string {
	return statsCompact(r, keys, labels, sep)
}
func author(r *linkreader.Result) string {
	s := field(r.Author.Name)
	if r.Author.Account.State == "available" && r.Author.Account.Value != "" && !sameHandle(s, r.Author.Account.Value) {
		s += " @" + r.Author.Account.Value
	}
	return s
}

// sameHandle 说名字本身就是这个账号（“@paper_shop”和“paper_shop”）：这时只写名字，不写成“@paper_shop @paper_shop”。
func sameHandle(name, account string) bool {
	trim := func(x string) string { return strings.ToLower(strings.TrimLeft(strings.TrimSpace(x), "@＠")) }
	return trim(name) != "" && trim(name) == trim(account)
}
func firstImage(r *linkreader.Result) (linkreader.Media, bool) {
	for _, m := range r.Media.Value {
		if m.Type == "image" || m.Type == "photo" {
			return m, true
		}
	}
	for _, m := range r.Media.Value {
		if m.Thumbnail.State == "available" {
			m.URL = m.Thumbnail
			m.Type = "image"
			return m, true
		}
	}
	return linkreader.Media{}, false
}

// profileAvatar 是个人主页没有图片媒体时拿头像当那张图（Bluesky、Misskey、Mastodon、TikTok 的取数只把头像放在 Author.Avatar，
// 不放进 Media；B 站、爱发电的主页自己把头像放进了 Media，走不到这里）。别的页面类型不用头像冒充封面。
func profileAvatar(r *linkreader.Result) (linkreader.Media, bool) {
	switch r.Platform + "/" + r.Kind {
	case "bluesky/profile", "misskey/user", "mastodon/account", "tiktok/user":
	default:
		return linkreader.Media{}, false
	}
	if u, ok := avail(r.Author.Avatar); ok && u != "" {
		return linkreader.Media{Type: "image", URL: linkreader.Field[string]{State: "available", Value: u}}, true
	}
	return linkreader.Media{}, false
}

// sendable 说这个媒体有没有能下载的地址：取数端标了 unsupported（只有封面、没有媒体文件）的不排进消息。
func sendable(m linkreader.Media) bool { return m.URL.State != "unsupported" }
func mediaItem(m linkreader.Media, standalone bool) Item {
	k := m.Type
	if k == "photo" {
		k = types.BodyImage
	}
	return Item{Kind: k, Media: m, Standalone: standalone}
}
func xStats(r *linkreader.Result, sep string) string {
	return statsCompact(r, []string{"likes", "reposts", "replies", "quotes", "bookmarks", "views"}, []string{"点赞", "转发", "回复", "引用", "书签", "浏览"}, sep)
}

// statsCompact 同 stats，数字用 compact 缩写（B 站视频、X 用）。
func statsCompact(r *linkreader.Result, keys, labels []string, sep string) string {
	var a []string
	for i, k := range keys {
		if r.Stats[k].State == "available" {
			a = append(a, labels[i]+" "+compact(r.Stats[k].Value))
		}
	}
	return strings.Join(a, sep)
}

// incomplete 是接在第一条后面的取数说明：只剩 pixiv 页图没取到、只取到第一页这类（mediaNote）。
// 以前正文或媒体被截断时还写一句“正文未完整取得”，那是取数层的状态，对群友没有用，而且常和“（已截取）”“请到原文继续阅读”重复（用户 2026-11-12）。
func incomplete(r *linkreader.Result) string {
	return mediaNote(r)
}

// Supports 只列发送端规格已定义的资源类型（查 linktemplate.Catalog）；新增取数能力不自动获得发送格式。
func Supports(platform, kind string) bool { return linktemplate.CanExpand(platform, kind) }

func numeric(f linkreader.Field[any]) int {
	switch v := f.Value.(type) {
	case int:
		return v
	case float64:
		if v == math.Trunc(v) {
			return int(v)
		}
	}
	return -1
}

// novelExcerpt 是 pixiv 小说的正文节选：最多 1500 字；取数端本来就只给了开头（Truncated）或这里又截了，都写“……”和“完整内容见原文”。
func novelExcerpt(r *linkreader.Result) string {
	head, cut := linktrunc.Cut(r.Text.Value, 1500)
	if !cut && !r.Text.Truncated {
		return head
	}
	return linktrunc.Mark(head)
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n" + b
}

// NodeImageMax 是合并转发一个节点里最多放几张图（图文混排时）。
// 依据：项目里没有 QQ 对单节点图数的明文说法，取直发一条消息图数上限 directImageMax 的配置上限 9（config 范围 1～9）；
// 多出来的图另起一个节点，仍按文字起头的节点接着放（用户 2026-11-14：图跟在文字后面，不要一图一节点）。
const NodeImageMax = 9

// inlineImage 说这一项能留在文字节点里跟文字混排：静态图和动图（QQ 里动图也是图）。
// 视频、卡片、文件、动图帧、单独成条的各自成节点。
func inlineImage(it Item) bool {
	return !it.Standalone && (it.Kind == types.BodyImage || it.Kind == types.BodyGIF)
}

// toNodes 把一组内容排成节点（用户 2026-11-14：帖子信息、正文、图片放同一个节点，只有视频等单独成节点），规则见 packNodes。
func toNodes(items []Item, maxBytes int) []Node {
	var nodes []Node
	for _, body := range packNodes(items, func(it Item) bool { return it.Kind == types.BodyText }, func(it Item) string { return it.Text }, text, inlineImage, maxBytes) {
		nodes = append(nodes, Node{Name: NodeName, Body: body})
	}
	return nodes
}

// packNodes 是排节点的唯一一份规则，Item（Build 阶段）和 types.BodySegment（Prepare 阶段的 overCap）共用：
//   - 文字按原顺序接起来；一个节点的文字最多 maxBytes 个 UTF-8 字节（ADR-391，默认 16500，多字节字符不切坏），
//     切点落在链接中间时整条链接挪到下一个节点（splitText）；
//   - 文字之后的图（inline 为真的项）留在同一个节点里，图文按原顺序混排，一个节点最多 NodeImageMax 张图，超了另起一个节点接着放；
//   - 一组开头就是图（前面没有文字）时仍一图一节点（pixiv 的“链接一个节点、后面一图一节点”）；
//   - 视频、卡片等其它项各自成节点。
func packNodes[T any](items []T, isText func(T) bool, textOf func(T) string, mkText func(string) T, inline func(T) bool, maxBytes int) [][]T {
	if maxBytes <= 0 {
		maxBytes = linktemplate.DefaultForwardOverBytes
	}
	var nodes [][]T
	var cur []T
	bytes, imgs := 0, 0
	textLed := false // 当前这段是文字起头的：后面的图可以接进节点
	pending := ""
	closeCur := func() {
		if len(cur) > 0 {
			nodes = append(nodes, cur)
		}
		cur, bytes, imgs = nil, 0, 0
	}
	flushText := func() {
		if pending == "" {
			return
		}
		chunks := splitText(pending, maxBytes)
		pending = ""
		for i, s := range chunks {
			n := len(s)
			if len(cur) > 0 && bytes+n > maxBytes {
				closeCur()
			}
			cur = append(cur, mkText(s))
			bytes += n
			textLed = true
			if i < len(chunks)-1 {
				closeCur() // 同一段文字切出来的前几块各占一个节点，最后一块留着接后面的图
			}
		}
	}
	for _, item := range items {
		switch {
		case isText(item):
			pending = join(pending, textOf(item))
		case inline(item):
			flushText()
			if imgs >= NodeImageMax {
				closeCur()
			}
			if len(cur) == 0 && !textLed {
				nodes = append(nodes, []T{item})
				continue
			}
			cur = append(cur, item)
			imgs++
		default:
			flushText()
			closeCur()
			nodes = append(nodes, []T{item})
			textLed = false
		}
	}
	flushText()
	closeCur()
	return nodes
}
func capNodes(d *Draft) {
	for i := range d.Parts {
		p := &d.Parts[i]
		if len(p.Nodes) > 80 {
			n := len(p.Nodes) - 79
			p.Nodes = KeepNodes(p.Nodes, func(j int) bool { return j < 79 })
			p.Nodes = append(p.Nodes, Node{Name: NodeName, Body: []Item{text(fmt.Sprintf("后面还有 %d 段，看原文\n%s", n, d.CleanURL))}})
		}
	}
}

// KeepNodes 留下 keep(下标) 为真的节点，并把留下节点的 QuoteOf 改成新下标；被引的节点没留下就清成 0。
// 节点的增删都走它（拆视频、80 节点封顶、准备时丢掉空节点），免得引用指错节点。
func KeepNodes(nodes []Node, keep func(int) bool) []Node {
	at := make([]int, len(nodes)) // 旧下标 → 新下标加 1；0 表示没留下
	var out []Node
	for i, n := range nodes {
		if keep(i) {
			out = append(out, n)
			at[i] = len(out)
		}
	}
	for i := range out {
		if q := out[i].QuoteOf; q > 0 {
			if q <= len(at) {
				out[i].QuoteOf = at[q-1]
			} else {
				out[i].QuoteOf = 0
			}
		}
	}
	return out
}

// splitForwardVideos 是视频不进合并转发时（expand.send.forwardVideo 关着，或这个会话退回过，规格-链接发送改版 A6、B3）的排法：
// 转发节点里的视频拿出来，各自一条消息紧跟在那个转发后面（不标 Standalone：那会被 Prepare 挪到所有段最后）；拿空的节点去掉。
// 动图不动（A6 只试视频）。
func splitForwardVideos(d *Draft) {
	var parts []Part
	for _, p := range d.Parts {
		if p.Kind != types.PartForward {
			parts = append(parts, p)
			continue
		}
		var videos []Part
		nodes := make([]Node, len(p.Nodes))
		for i, n := range p.Nodes {
			nodes[i] = n
			nodes[i].Body = nil
			for _, it := range n.Body {
				if it.Kind == types.BodyVideo {
					videos = append(videos, Part{Kind: types.PartMessage, Body: []Item{it}})
				} else {
					nodes[i].Body = append(nodes[i].Body, it)
				}
			}
		}
		p.Nodes = KeepNodes(nodes, func(i int) bool { return len(nodes[i].Body) > 0 })
		if len(p.Nodes) > 0 {
			parts = append(parts, p)
		}
		parts = append(parts, videos...)
	}
	d.Parts = parts
}

// pollText 把投票写成几行字：第一行总票数和是否结束，后面一行一个选项。缺的项不写，不写“未返回”。
func pollText(p *linkreader.PollInfo, now time.Time) string { return pollTextLabels(p, nil, now) }

// pollTextLabels 同 pollText；labels 和选项一一对应、不为空串的那些换成译文（2026-11-14 用户：选项也只写译文），数量对不上就全用原文。
func pollTextLabels(p *linkreader.PollInfo, labels []string, now time.Time) string {
	if len(labels) != len(p.Choices) {
		labels = nil
	}
	head := []string{"投票"}
	var info []string
	if p.Total.State == "available" {
		info = append(info, fmt.Sprintf("共 %d 票", p.Total.Value))
	}
	if p.EndsAt.State == "available" {
		if p.EndsAt.Value.After(now) {
			info = append(info, "截止 "+stamp(p.EndsAt))
		} else {
			info = append(info, "已结束")
		}
	}
	if len(info) > 0 {
		head = append(head, "（"+strings.Join(info, "，")+"）")
	}
	lines := []string{strings.Join(head, "")}
	for i, c := range p.Choices {
		line := c.Label
		if labels != nil && labels[i] != "" {
			line = labels[i]
		}
		var nums []string
		if c.Percent.State == "available" {
			nums = append(nums, strconv.FormatFloat(c.Percent.Value, 'f', -1, 64)+"%")
		}
		if c.Count.State == "available" {
			nums = append(nums, strconv.FormatInt(c.Count.Value, 10)+" 票")
		}
		if len(nums) > 0 {
			line += "：" + strings.Join(nums, "，")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// tombstoneText 是被引用的帖子不可用时写的一句话。
func tombstoneText(reason string) string {
	why := map[string]string{"deleted": "已被删除", "suspended": "所属账号已被停用", "private": "是受保护的，看不到", "blocked": "被屏蔽了，看不到", "unavailable": "现在看不到"}[reason]
	if why == "" {
		why = "现在看不到（" + reason + "）"
	}
	return "被引用的推文" + why
}

func contents(r *linkreader.Result, o Options, media bool, depth int) []Item {
	return contentsOf(r, o, media, depth, true)
}

// contentsOf 同 contents；quotes 为假时只要帖子自己的内容：不含被引用的帖子、它不可用的说明和“还有更深引用”（X 的 $被引推文 另外放）。
func contentsOf(r *linkreader.Result, o Options, media bool, depth int, quotes bool) []Item {
	return contentsSeen(r, o, media, depth, quotes, map[string]bool{})
}

// contentsSeen 同 contentsOf；seen 记下已经写过的外链卡地址，帖子和它引用的帖子带同一张卡（BS-04：外层、被引用的各一张 Delvetown）时只写一次，留先出现的那张。
func contentsSeen(r *linkreader.Result, o Options, media bool, depth int, quotes bool, seen map[string]bool) []Item {
	var out []Item
	appendQuote := func(q *linkreader.Result) {
		if q == nil || !quotes {
			return
		}
		if depth > 0 {
			out = append(out, text("还有更深引用，看原文"))
			return
		}
		// 引用头一行“引用 名字 @账号：”（以前是“引用 @账号（外部内容）”再接一行“名字 @账号”，重复）
		head := "引用："
		if who := person(q); who.ok {
			head = "引用 " + who.text + "："
		}
		out = append(out, text(head))
		shown, note := o.translatedEmbed(q) // 长文正文里嵌的推文原位换成译文（ADR-391）
		out = append(out, contentsSeen(shown, o, media, depth+1, true, seen)...)
		if note != "" {
			out = append(out, text("（"+note+"）"))
		}
	}
	hasText, hasQuote := false, false
	for _, b := range r.Blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, text(b.Text))
				hasText = true
			}
		case "image", "photo", "video", "gif", "article_cover":
			if media && b.Media != nil && sendable(*b.Media) {
				out = append(out, mediaItem(*b.Media, false))
			} else if media && b.Media == nil && b.State != "" {
				// 接口说有这张图却没给地址：写一句，不让它悄悄消失
				out = append(out, text("这里有一张图没取到"))
			}
		case "article_title":
			if b.Text != "" {
				out = append(out, text("《"+b.Text+"》"))
			}
		case "divider":
			out = append(out, text("———"))
		case "poll":
			if r.Poll != nil {
				out = append(out, text(pollTextLabels(r.Poll, o.pollLabelsFor(r.Poll), nowFunc())))
			}
		case "quote_unavailable":
			if quotes {
				out = append(out, text(tombstoneText(b.Reason)))
			}
		case "quote":
			hasQuote = true
			appendQuote(b.Quote)
		case "link", "link_card":
			if b.Type == "link_card" {
				// 同一个地址的外链卡只写一次；取不到地址（旧缓存）就按卡片文字比
				key := b.Text
				if r.LinkCard.State == "available" && r.LinkCard.Value.URL.Value != "" {
					key = r.LinkCard.Value.URL.Value
				}
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			out = append(out, text(b.Text))
			if media && b.Media != nil && sendable(*b.Media) {
				out = append(out, mediaItem(*b.Media, false))
			}
		default:
			out = append(out, text("这一段未能读取"))
		}
	}
	if !hasText && r.Text.State == "available" && r.Text.Value != "" {
		out = append([]Item{text(r.Text.Value)}, out...)
	}
	if len(r.Blocks) == 0 && media {
		for _, m := range r.Media.Value {
			if m.Type != "audio" && sendable(m) {
				out = append(out, mediaItem(m, false))
			}
		}
	}
	if !hasQuote && r.Quote.State == "available" {
		appendQuote(r.Quote.Value)
	}
	if r.MoreQuotes.Value && quotes {
		out = append(out, text("还有更深引用，看原文"))
	}
	return out
}

// linkCost 统计一次拆节点的链接保护工作量（测试用来盯住“每个候选切点不再从头扫链接”，不靠墙钟）。
type linkCost struct {
	scans      int // 调了几次 URLSpans（每个节点只该一次）
	scanBytes  int // URLSpans 一共扫了多少字节
	candidates int // 试过多少个候选切点
}

// nodeHead 取 rest 作为一个节点的开头，最多 maxBytes 个 UTF-8 字节（多字节字符不切坏）。超长时优先在空行切，
// 其次换行，再其次句末标点；都找不到才在上限附近切。任何切点都不落在链接、@用户名、#话题 中间（整条挪到下一个节点）；
// 单个链接自己就超过上限时整条放进一个节点。
func nodeHead(rest string, maxBytes int) string { return nodeHeadCost(rest, maxBytes, nil) }

// nodeHeadCost 是 nodeHead 的可计数版本；cost 非空时累计链接区间扫描和候选切点检查的次数。
func nodeHeadCost(rest string, maxBytes int, cost *linkCost) string {
	if maxBytes <= 0 || len(rest) <= maxBytes {
		return rest
	}
	at := maxBytes
	for at > 0 && !utf8.RuneStart(rest[at]) {
		at--
	}
	if at == 0 {
		_, at = utf8.DecodeRuneInString(rest)
	}
	floor := at / 2 // 不为了好看切得太短
	// 链接区间只算一次（URLSpans 整段扫一遍），之后每个候选切点二分查一次位置。
	// 以前每个候选都调 SafeSplit 从头扫剩余文字，2 万字的单段要十几秒。
	spans := linkreader.URLSpans(rest)
	if cost != nil {
		cost.scans++
		cost.scanBytes += len(rest)
	}
	insideLink := func(i int) bool {
		k := sort.Search(len(spans), func(j int) bool { return spans[j][0] >= i }) - 1
		return k >= 0 && i > spans[k][0] && i < spans[k][1]
	}
	pick := func(ok func(string) int) int {
		for i := at; i > floor; i-- {
			if cost != nil {
				cost.candidates++
			}
			if !utf8.RuneStart(rest[i]) {
				continue
			}
			if insideLink(i) || insideToken(rest, i) {
				continue
			}
			if ok(rest[:i]) > 0 {
				return i
			}
		}
		return 0
	}
	cutAt := pick(func(h string) int {
		if strings.HasSuffix(h, "\n\n") {
			return 1
		}
		return 0
	})
	if cutAt == 0 {
		cutAt = pick(func(h string) int {
			if strings.HasSuffix(h, "\n") {
				return 1
			}
			return 0
		})
	}
	if cutAt == 0 {
		cutAt = pick(func(h string) int {
			r, _ := utf8.DecodeLastRuneInString(h)
			if strings.ContainsRune("。！？!?；;", r) {
				return 1
			}
			return 0
		})
	}
	if cutAt == 0 {
		// 和 SafeSplit 同义：落在链接里就退到链接开头，链接从 0 开头就进到链接结尾。
		cutAt = at
		if k := sort.Search(len(spans), func(j int) bool { return spans[j][0] >= at }) - 1; k >= 0 && at < spans[k][1] {
			cutAt = spans[k][0]
			if cutAt == 0 {
				cutAt = spans[k][1]
			}
		}
		for insideToken(rest, cutAt) {
			cutAt = tokenStart(rest, cutAt)
		}
	}
	if cutAt <= 0 { // 开头就是一条超长链接：整条放进一个节点
		cutAt = at
	}
	return rest[:cutAt]
}

// tokenStart 找到 i 所在的“不含空白的一段”的开头。
func tokenStart(s string, i int) int {
	for i > 0 {
		r, w := utf8.DecodeLastRuneInString(s[:i])
		if unicode.IsSpace(r) {
			break
		}
		i -= w
	}
	return i
}

// insideToken 说切点 i 是否落在 @用户名 或 #话题 中间（前后都不是空白，且这一段以 @ 或 # 开头）。
func insideToken(s string, i int) bool {
	if i <= 0 || i >= len(s) {
		return false
	}
	before, _ := utf8.DecodeLastRuneInString(s[:i])
	after, _ := utf8.DecodeRuneInString(s[i:])
	if unicode.IsSpace(before) || unicode.IsSpace(after) {
		return false
	}
	start := i
	for start > 0 && i-start <= 60 {
		r, w := utf8.DecodeLastRuneInString(s[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= w
	}
	if i-start > 60 || start == i {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[start:])
	return (r == '@' || r == '#' || r == '＠' || r == '＃') && i-start > 0
}
