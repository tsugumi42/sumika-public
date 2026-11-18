package linktemplate

import (
	"fmt"
	"strings"
)

// Preset 是一个内置模板：设置里写 @名字 就等于把这份拿来用，设置页的“套用”按钮也是写入这个引用。
type Preset struct {
	Name   string
	Source string
}

// Site 是一个站点（和形态）的模板定义：能用的占位符、内置默认模板、内置预设、转发和简介的默认设置。
type Site struct {
	Key     string // 配置键：bilibili-video、x……；转发模板的配置键是 Key + "-forward"
	Label   string // 设置页显示的名字
	Set     *Set
	Direct  string // 直发模板的内置默认（可写 @预设名）
	Forward string // 转发模板的内置默认；空表示这个站点没有转发形态
	Presets []Preset
	// ForwardOver 默认的“超过多少字折成合并转发”（0 不按长度折）；只有还按字数的站点（B 站）用它。
	// ForwardOverBytes 默认的“实际发出的文字超过多少 UTF-8 字节折成合并转发”（0 这个站点不按字节判断），x、telegram 用（ADR-391）。
	ForwardOver      int
	ForwardOverBytes int
	DescLimit        int
}

// HasForward 说这个站点有没有合并转发形态。
func (s Site) HasForward() bool { return s.Forward != "" }

// descMark 是内置模板里“简介那一行”的位置记号（用户 2026-11-13，R-219）：站点设置 showDesc 关着（默认）时去掉，
// 开着时换成 descLine。只出现在内置默认和内置预设里；用户自己写的模板不经过这个替换。
const (
	descMark = "{{简介}}"
	descLine = "[\n$简介]"
)

func withDesc(src string, desc bool) string {
	if desc {
		return strings.ReplaceAll(src, descMark, descLine)
	}
	return strings.ReplaceAll(src, descMark, "")
}

// HasDescOption 说这个站点的内置默认有没有可以打开的简介那一行（showDesc 只对这些站点有意义）。
func (s Site) HasDescOption() bool {
	if strings.Contains(s.Direct, descMark) || strings.Contains(s.Forward, descMark) {
		return true
	}
	for _, p := range s.Presets {
		if strings.Contains(p.Source, descMark) {
			return true
		}
	}
	return false
}

// Source 返回该形态的内置默认模板原文（已展开 @ 引用，不带简介那一行）。
func (s Site) Source(forward bool) string { return s.SourceWith(forward, false) }

// SourceWith 同 Source；desc 为真时带上简介那一行（站点设置 showDesc 开着）。
func (s Site) SourceWith(forward, desc bool) string {
	src := s.Direct
	if forward {
		src = s.Forward
	}
	out, _, err := s.resolveRaw(src, true)
	if err != nil {
		panic("linktemplate: 内置模板写错了：" + err.Error())
	}
	return withDesc(out, desc)
}

// resolveRaw 把 @预设名 换成预设的原文，别的原样返回。builtin 说结果是不是内置的（内置默认和内置预设带简介位记号，用户写的不带）；
// fromBuiltin 为真表示 src 本身是内置默认（Site.Direct、Site.Forward），不是用户写的。
func (s Site) resolveRaw(src string, fromBuiltin bool) (out string, builtin bool, err error) {
	name, ok := strings.CutPrefix(strings.TrimSpace(src), "@")
	if !ok {
		return src, fromBuiltin, nil
	}
	for _, p := range s.Presets {
		if p.Name == name {
			return p.Source, true, nil
		}
	}
	names := make([]string, len(s.Presets))
	for i, p := range s.Presets {
		names[i] = "@" + p.Name
	}
	return "", false, fmt.Errorf("这个站点没有内置模板 @%s（可用：%s）", name, strings.Join(names, "、"))
}

// Resolve 把 @预设名 换成预设的原文（不带简介那一行），别的原样返回。
func (s Site) Resolve(src string) (string, error) { return s.ResolveWith(src, false) }

// ResolveWith 同 Resolve；desc 为真时内置预设带上简介那一行。
func (s Site) ResolveWith(src string, desc bool) (string, error) {
	out, builtin, err := s.resolveRaw(src, false)
	if err != nil {
		return "", err
	}
	if builtin {
		out = withDesc(out, desc)
	}
	return out, nil
}

// Parse 解析配置里的模板写法（可以是 @预设名）；forward 选哪一份内置默认当空值。
func (s Site) Parse(src string) (*Template, error) { return s.ParseWith(src, false) }

// ParseWith 同 Parse；desc 为真时内置预设带上简介那一行。
func (s Site) ParseWith(src string, desc bool) (*Template, error) {
	resolved, err := s.ResolveWith(src, desc)
	if err != nil {
		return nil, &ParseError{Pos: 1, Msg: err.Error()}
	}
	return Parse(resolved, s.Set)
}

func txt(id, sample string, names ...string) Field {
	return Field{ID: id, Names: names, Kind: KindText, Sample: sample}
}
func slot(id, sample string, names ...string) Field {
	return Field{ID: id, Names: names, Kind: KindSlot, Sample: sample}
}

func common(label string) []Field {
	return []Field{txt("url", "https://example.invalid/item/1", "链接", "url"), txt("site", label, "站点", "site"), slot("card", "卡片", "卡片", "card")}
}
func join(parts ...[]Field) []Field {
	var out []Field
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// DefaultForwardOverBytes 是“实际发出的文字超过多少 UTF-8 字节折成合并转发”的默认值：实测 QQ 群单条纯文字上限约 18.4 KB，
// 按 90% 取 16500 字节（ADR-391，2026-11-18 实测）。
const DefaultForwardOverBytes = 16500

const (
	// B 站视频（规格-链接发送改版 B8）：封面、标题（分 P 的名字或列表接在标题下）、“UP主 | 时长”、“播放 | 点赞”、投稿时间、干净链接，
	// 行间不空行、不带简介；链接在投稿时间底下（用户 2026-11-12）。UP主缺了那一行只剩时长。简介、充电、热评的字段还在，模板里写上就有。
	biliVideoText = "$封面$标题[\n$分P][\n$分P列表][\n[$UP主][ | $时长说明]][\n[播放 $播放][ | 点赞 $点赞]][\n$时间]{{简介}}\n$链接$视频"
	// X 帖子文字版（规格-链接发送改版 B2）：链接、“名字[@ID]：”、「正文」（带译文时「原文\n——————\n中文」）、发帖时间、图在文字下面，行间不空行；
	// 正文为空（纯图推文）整行省掉；时间一行在「正文」块下面、图片上面（用户 2026-11-12 定），格式同 $时间（今年“11-07 23:28”，往年带年份）。
	// 视频单独一条消息（$视频）；动图算图（$图片），跟文字同一条（用户 2026-11-13）。默认不写统计、拼图，字段都还在，自定义模板写上就有。
	// 有引用的推文进合并转发，被引推文自己一个节点（$被引推文）；直发模板也带上它，站点设成 never 时引用不会丢。
	// 名字和账号都取不到时“名字[@ID]：”整行省掉，不剩一个孤零零的“：”（外层可选段里只有嵌套段，一个都出不来就整段省）。
	// 冒号是 $冒号：只在有正文时有值，纯图推文的作者行后面不挂冒号（用户 2026-11-13，#32）。
	xTweetHead  = "$链接[\n[$昵称][" + `\[@$账号\]` + "][$冒号]][\n「$正文文字」][\n（$翻译说明）][\n$时间]" // \[ \] 是字面方括号
	xDirectText = xTweetHead + "$图片$视频$被引推文"
	// 画师的推文（规格-画师原图 第一节）：作者是用户确认过的画师、推文有自己的图时，不管 x 站点配的什么样式都用这两个模板：
	// 卡片加原图（默认）：卡片、干净链接、原图、视频；只发原图：干净链接、原图、视频。图数上限、转发、降级阶梯仍走 shape.go 的通用规则。
	XArtistCardOriginals = "$卡片$链接$图片$视频"
	XArtistOriginalsOnly = "$链接$图片$视频"
	// XArticleForward 是 X 文章（长文）的内置转发排法：文章的字和图是穿插的，不能像推文那样把字和图分开放，
	// 所以头部之后整段 $正文 按原顺序进节点。只在没写 x-forward 模板时用（linkexpand 判断是不是文章）。
	// 有译文时长文的文字块和小标题在原位换成译文，原文不出现（2026-11-14，规格-链接发送改版 B14），头部多一行“（翻译自日语）”；以前是末尾另起节点放“——————译文”。
	XArticleForward = "$合并转发$链接[\n[$昵称][" + `\[@$账号\]` + "]：][\n（$翻译说明）]$正文"
	// 帖子类站点（Bluesky、Misskey / Mastodon、Telegram）照推文排（用户 2026-11-13，规格-链接发送改版 B10 第 5 条）：
	// 链接、名字[@ID]：、「正文」、时间（主页的注册时间、内容提醒、敏感标记、统计接在后面）、图、视频；$账号 不带 @ 。
	// 正文为空整行省掉；图和动图在文字下面，视频单独一条消息（$视频）。
	postHead = "$链接[\n[$昵称][" + `\[@$账号\]` + "][$冒号]][\n「$正文文字」]"
	// B 站小游戏：封面、标题、作者 | 分类、发布时间、链接，行间不空行。游玩、点赞、收藏、评论的字段还在，模板里写上就有。
	biliToyText = "$封面$标题[\n作者：$作者[ | $分类]][\n$时间]\n$链接"
	// 下面这些非推文站点的内置默认：不写平台名（用户 2026-11-12：“B站直播 | ”“GitHub | ”“Steam 游戏 | ”这类前缀都去掉，$站点 字段还在），
	// 不写简介（$简介 字段照旧取，模板里写上就有），时间写法同推文（今年“11-12 17:45”，往年带年份）。
	biliLiveText  = "$封面$标题[\n主播：$主播][\n分区：$分区][\n状态：$状态][\n开播：$开播时间][\n[人气 $人气][ | 粉丝 $粉丝]]{{简介}}\n$链接"
	biliSpaceText = "$封面$昵称[\n$签名][\n认证：$认证][\n[粉丝 $粉丝][ | 关注 $关注][ | 投稿 $投稿][ | 获赞 $获赞]]\n$链接"
	biliShopText  = "$封面$标题[\n店铺：$店铺][\n价格：$价格[（原价 $原价）]][\n已售：$已售][\n状态：$状态][\n交付：$交付][\n标签：$标签]{{简介}}\n$链接"
	// 番剧默认是文字版（用户 2026-11-12：和视频一样），结构照视频：封面、标题、集、评分 | 状态、播放 | 追番、首播、链接；地区、风格、弹幕这些字段还在
	biliBangumi = "$封面$标题[\n$集][\n[评分 $评分][ | $状态]][\n[播放 $播放][ | 追番 $追番]][\n首播：$首播]{{简介}}\n$链接"
	biliCourse  = "$封面$标题[\nUP主：$UP主][\n价格：$价格[（$优惠）]][\n$课时][\n试看：$试看][\n播放 $播放]{{简介}}\n$链接"
	biliAudio   = "$封面$标题[\nUP主：$UP主][\n时长：$时长][\n发布：$发布时间][\n[播放 $播放][ | 收藏 $收藏][ | 评论 $评论][ | 分享 $分享][ | 投币 $投币]]{{简介}}\n$链接"
	biliColl    = "$封面$标题[\nUP主：$UP主][\n共 $视频数 个视频]{{简介}}[\n最前面：$列表]\n$链接"
	// B 站专栏（用户 2026-11-13 复核 B-1、B-2）：标题、UP主、时间（不带“发布：”，同动态）、阅读等数据；热评不默认写（字段 $热评 还在，评论不公开时那句“评论不公开”也只在这个字段里）。
	// 直发（短专栏）：头部、正文（文字图片按原顺序）、干净链接在最后，同动态；转发：第一个节点是头部加链接，后面正文一段一节点。
	biliArticleHead = "$标题[\nUP主：$UP主][\n$发布时间][\n[阅读 $阅读][ | 点赞 $点赞][ | 评论 $评论][ | 收藏 $收藏][ | 投币 $投币][ | 转发 $转发]]"
	// B 站动态（短的：正文不超过 forwardOver 字、图不超过直发上限 directImageMax 张，默认 3）：图在上、标题和正文、UP主和数据、发布时间、链接，行间不空行；
	// 长的或多图整条进合并转发（Forward 模板）。直发和转发都带发布时间（以前直发没有，两种发法不一致）。
	biliDynamicText = "$图片[$标题][\n$正文文字][\nUP主：$UP主][\n[点赞 $点赞][ | 评论 $评论][ | 转发 $转发]][\n$发布时间]\n$链接$其余内容"
	biliHead        = "[$标题][\nUP主：$UP主][\n[点赞 $点赞][ | 评论 $评论][ | 转发 $转发]][\n$发布时间]\n$链接" // 和直发同顺序，时间不带标签（#18）
	douyinHead      = "[$简介][\n作者：$作者][\n发布：$发布时间][\n[点赞 $点赞][ | 评论 $评论][ | 收藏 $收藏][ | 转发 $转发]]\n$链接"
)

var sites = withStyles(buildSites())

func buildSites() []Site {
	biliStats := []Field{txt("likes", "52", "点赞", "likes"), txt("comments", "8", "评论", "comments"), txt("shares", "4", "转发", "shares")}
	return []Site{
		{Key: "bilibili-video", Label: "B站视频",
			Set: NewSet(join(common("B站视频"), []Field{
				txt("title", "纸船试航", "标题", "title"), txt("part", "P2/3：折纸步骤", "分P", "part"), txt("partlist", "P1 开箱（02:15）\nP2 折纸步骤（08:40）", "分P列表", "partlist"),
				txt("author", "纸灯实验室", "UP主", "作者", "author"), txt("published", "11-01 18:30", "发布时间", "published"), txt("duration", "02:15", "时长", "duration"), txt("length", "共 3P | 总长 01:02:15", "时长说明", "length"),
				txt("views", "1.08亿", "播放", "views"), txt("favorites", "156.6万", "收藏", "favorites"), txt("shares", "4", "转发", "shares"), txt("comments", "8", "评论", "comments"),
				txt("danmaku", "6", "弹幕", "danmaku"), txt("likes", "297.5万", "点赞", "likes"), txt("coins", "129.3万", "投币", "coins"),
				txt("desc", "这是编的视频简介。", "简介", "desc"), txt("when", "11-07 23:28", "时间", "when"),
				txt("upower", "充电专属（有试看）", "充电", "upower"), txt("hot", "热评：\n1. 用户甲：这是编的评论（赞 12）", "热评", "hot"),
				slot("cover", "封面", "封面", "cover"), slot("video", "视频", "视频", "video"),
			})...),
			Direct:    biliVideoText,
			DescLimit: 100,
			Presets:   []Preset{{"封面文字", biliVideoText}}},
		{Key: "bilibili-mall", Label: "B站会员购",
			Set: NewSet(join(common("会员购"), []Field{
				txt("title", "纸舟工房 星灯摆件", "标题", "title"), txt("details", "状态：预售\n店铺：纸舟商店", "详情", "details"), txt("published", "10-22 19:55", "发布时间", "published"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct:  "@卡片",
			Presets: []Preset{{"卡片", "$卡片$链接"}, {"封面文字", "$标题\n$详情\n$封面\n$链接"}}},
		{Key: "bilibili-live", Label: "B站直播间", DescLimit: 200,
			Set: NewSet(join(common("B站直播间"), []Field{
				txt("title", "纸船试航直播", "标题", "title"), txt("author", "纸灯实验室", "主播", "author"), txt("area", "生活 / 手工", "分区", "area"),
				txt("status", "直播中", "状态", "status"), txt("online", "1200", "人气", "online"), txt("attention", "8800", "关注", "attention"), // 同粉丝（直播间的关注数就是粉丝数）
				txt("followers", "8800", "粉丝", "followers"), txt("published", "11-01 18:30", "开播时间", "published"),
				txt("tags", "折纸、手工", "标签", "tags"), txt("desc", "这是编的直播间简介。", "简介", "desc"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: biliLiveText},
		{Key: "bilibili-space", Label: "B站个人主页", DescLimit: 200,
			Set: NewSet(join(common("B站个人主页"), []Field{
				txt("title", "纸灯实验室", "昵称", "title", "name"), txt("desc", "做折纸和纸灯的小实验室。", "签名", "desc"),
				txt("official", "纸灯实验室官方账号", "认证", "official"),
				txt("followers", "8800", "粉丝", "followers"), txt("following", "120", "关注", "following"),
				txt("videos", "56", "投稿", "videos"), txt("likes", "30000", "获赞", "likes"),
				slot("cover", "头像", "头像", "cover", "封面"),
			})...),
			Direct: biliSpaceText},
		{Key: "bilibili-dynamic", Label: "B站动态", ForwardOver: 100,
			Set: NewSet(join(common("B站动态"), []Field{
				txt("title", "动态标题", "标题", "title"), txt("author", "纸灯实验室", "UP主", "作者", "author"), txt("published", "11-01 18:30", "发布时间", "published"),
				txt("text", "这是一条编的动态正文。", "正文文字", "text"),
			}, biliStats, []Field{slot("body", "正文", "正文", "body"), slot("images", "图片", "图片", "images"), slot("extra", "其余内容", "其余内容", "extra")})...),
			Direct:  biliDynamicText,
			Forward: "$合并转发" + biliHead + "$节点$正文"},
		{Key: "x", Label: "X / 推特", ForwardOverBytes: DefaultForwardOverBytes,
			Set: NewSet(join(common("X 推文"), []Field{
				txt("author", "纸云 @paper_cloud", "作者", "author"), txt("name", "纸云", "昵称", "name"), txt("account", "paper_cloud", "账号", "account"),
				txt("published", "11-01 18:30", "发布时间", "published"), txt("text", "这是一条编的推文正文。", "简介", "text"),
				txt("likes", "1005", "点赞", "likes"), txt("retweets", "140", "转发", "retweets"), txt("replies", "81", "回复", "replies"),
				txt("quotes", "0", "引用", "quotes"), txt("bookmarks", "3", "书签", "bookmarks"), txt("views", "36.6万", "浏览", "views"),
				txt("stats", "点赞 1005 | 转发 140 | 回复 81 | 引用 0 | 书签 3 | 浏览 36.6万", "统计", "stats"),
				txt("when", "11-07 23:28", "时间", "when"), txt("bodytext", "这是一条编的推文正文。", "正文文字", "bodytext"), txt("colon", "：", "冒号", "colon"),
				txt("translation", "这是一条编的译文。", "译文", "translation"),
				txt("origtext", "这是一条编的推文原文。", "原文", "origtext"), txt("tnote", "翻译自日语", "翻译说明", "tnote"),
				slot("body", "正文", "正文", "body"), slot("mosaic", "拼图", "拼图", "mosaic"), slot("images", "图片", "图片", "images"), slot("video", "视频", "视频", "video"),
				slot("quoted", "被引推文", "被引推文", "quoted"),
			})...),
			Direct:  xDirectText,
			Forward: "$合并转发" + xTweetHead + "$节点$图片$视频[$节点$被引推文]"},
		{Key: "pixiv-artwork", Label: "pixiv 作品",
			Set: NewSet(join(common("pixiv 作品"), []Field{
				txt("title", "纸船试航", "标题", "title"), txt("author", "纸云", "作者", "author"), txt("ai", "否", "AI"), txt("rating", "全年龄", "分级", "rating"), txt("pages", "3", "页数", "pages"),
				txt("series", "纸船连载 第3话", "系列", "series"), txt("prev", "第2话 上一话 https://example.invalid/prev", "上一话", "prev"), txt("next", "第4话 下一话 https://example.invalid/next", "下一话", "next"),
				txt("firstline", "[AI]🔞 | 纸船试航 | 纸云", "首行", "firstline"),
				slot("images", "图片", "图片", "images"),
			})...),
			// pixiv 文字版（规格-链接发送改版 B7）：首行“[AI]🔞 | 标题 | 作者名”，下一行作品链接（用户 2026-11-12 定），图在下。
			// 合并转发第一个节点同样是首行加干净链接，后面一图一节点（最多 10 张）。作者、系列、分级这些字段还在，模板里写上就有。
			// 首行取不到（标题、作者、两个标记全缺，比如旧缓存）就省掉，不写“未返回”，链接照旧在。
			Direct:  "[$首行\n]$链接$图片",
			Forward: "$合并转发[$首行\n]$链接$节点$图片"},
		{Key: "pixiv-novel", Label: "pixiv 小说", DescLimit: 300,
			Set: NewSet(join(common("pixiv 小说"), []Field{
				txt("title", "纸船试航", "标题", "title"), txt("author", "纸云", "作者", "author"), txt("ai", "否", "AI"), txt("rating", "全年龄", "分级", "rating"),
				txt("firstline", "[AI]🔞 | 纸船试航 | 纸云", "首行", "firstline"),
				txt("tags", "原创、纸船", "标签", "tags"), txt("words", "12345", "字数", "words"), txt("desc", "这是编的简介。", "简介", "desc"),
				txt("series", "纸灯物语 第84话", "系列", "series"), txt("prev", "第83话 上一话 https://example.invalid/prev", "上一话", "prev"), txt("next", "第85话 下一话 https://example.invalid/next", "下一话", "next"),
				slot("body", "正文节选", "正文", "body"), slot("cover", "封面", "封面", "cover"),
			})...),
			// 小说也套作品的首行（用户 2026-11-12）：“[AI]🔞 | 标题 | 作者名”，下一行链接；转发的第一个节点就是首行和链接。
			// 作者、AI、分级、简介这些字段还在，模板里写上就有。
			Direct:  "[$首行\n]$链接[\n系列：$系列][\n字数：$字数]{{简介}}$封面$正文",
			Forward: "$合并转发[$首行\n]$链接[\n系列：$系列][\n标签：$标签][\n字数：$字数]{{简介}}$封面$节点$正文"},
		{Key: "pixiv-series", Label: "pixiv 系列", DescLimit: 200,
			Set: NewSet(join(common("pixiv 系列"), []Field{
				txt("type", "漫画系列", "类型", "type"), txt("title", "纸船连载", "标题", "title"), txt("author", "纸云", "作者", "author"),
				txt("count", "共70话", "数量", "count"), txt("latest", "https://example.invalid/latest", "最新话", "latest"), txt("first", "https://example.invalid/first", "首话", "first"),
				txt("desc", "这是编的系列简介。", "简介", "desc"), txt("ai", "否", "AI"), txt("rating", "全年龄", "分级", "rating"),
				txt("firstline", "[AI]🔞 | 纸船连载 | 纸云", "首行", "firstline"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			// 漫画系列、小说系列也套作品的首行（用户 2026-11-12），下一行链接，再一行话数
			Direct: "[$首行\n]$链接[\n$数量]{{简介}}$封面"},
		{Key: "github", Label: "GitHub",
			Set: NewSet(join(common("GitHub"), []Field{
				txt("title", "paper/boat", "标题", "title"), txt("desc", "纸船的说明", "简介", "desc"), txt("language", "Go", "语言", "language"), txt("license", "MIT", "许可证", "license"),
				txt("stars", "12", "Star", "stars"), txt("forks", "3", "Fork", "forks"), txt("issues", "1", "Issue", "issues"), txt("updated", "10-01 08:00", "更新时间", "updated"),
			})...),
			Direct: "$标题{{简介}}[\n[语言：$语言][ | 许可证：$许可证]][\n[Star $Star][ | Fork $Fork][ | 未关闭Issue $Issue]][\n更新：$更新时间]\n$链接"},
		{Key: "youtube", Label: "YouTube",
			Set: NewSet(join(common("YouTube"), []Field{
				txt("title", "Paper Boat Test", "标题", "title"), txt("channel", "Paper Lab", "频道", "channel"), txt("published", "11-01 18:30", "发布时间", "published"), txt("duration", "03:21", "时长", "duration"),
				txt("views", "1000", "观看", "views"), txt("likes", "50", "点赞", "likes"), txt("comments", "7", "评论", "comments"),
				txt("when", "11-07 23:28", "时间", "when"),
				slot("cover", "封面", "封面", "cover"), slot("video", "视频", "视频", "video"),
			})...),
			// 视频文字版（B8），结构同 B 站视频：频道对应 UP主，观看对应播放
			Direct: "$封面$标题[\n[$频道][ | $时长]][\n[播放 $观看][ | 点赞 $点赞]][\n$时间]\n$链接$视频"},
		{Key: "douyin-video", Label: "抖音视频",
			Set: NewSet(join(common("抖音"), douyinFields(true), []Field{slot("cover", "封面", "封面", "cover"), slot("video", "视频", "视频", "video")})...),
			// 视频文字版（B8）：文案当标题，作者 | 时长，点赞，投稿时间，链接
			Direct: "$封面[$简介][\n[$作者][ | $时长]][\n点赞 $点赞][\n$时间]\n$链接$视频"},
		{Key: "douyin-note", Label: "抖音图集",
			Set:     NewSet(join(common("抖音图集"), douyinFields(false), []Field{slot("body", "正文", "正文", "body")})...),
			Direct:  douyinHead + "\n$正文",
			Forward: "$合并转发" + douyinHead + "$节点$正文"},
		{Key: "netease", Label: "网易云音乐",
			Set: NewSet(join(common("网易云音乐"), []Field{
				txt("title", "纸船", "标题", "title"), txt("artist", "河岸合奏团", "歌手", "artist"), txt("album", "折纸集", "专辑", "album"), txt("duration", "03:20", "时长", "duration"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: "$封面$标题[\n歌手：$歌手][\n专辑：$专辑][\n时长：$时长]\n$链接"},
		{Key: "xiaoheihe", Label: "小黑盒", DescLimit: 200,
			Set: NewSet(join(common("小黑盒"), []Field{
				txt("title", "纸舟物语", "标题", "title"), txt("appid", "90001", "游戏编号", "appid"), txt("developer", "纸舟工作室", "开发商", "developer"),
				txt("released", "2025年11月16日", "发行日期", "released"), txt("desc", "这是编的游戏简介。", "简介", "desc"),
			})...),
			// $标题 取不到游戏名时整行省掉（不再写“游戏 编号”），首行是开发商或发行
			Direct: "[$标题][\n开发商：$开发商][\n发行：$发行日期]{{简介}}\n$链接"},
		{Key: "booth", Label: "BOOTH", DescLimit: 200,
			Set: NewSet(join(common("BOOTH"), []Field{
				txt("title", "纸船模型 套装", "标题", "title"), txt("shop", "纸舟商店", "店铺", "shop"), txt("price", "¥ 600~", "价格", "price"),
				txt("category", "3D 角色", "分类", "category"), txt("tags", "原创、纸船", "标签", "tags"), txt("wishes", "120", "收藏", "wishes"),
				txt("status", "已结束销售", "状态", "status"), txt("published", "10-01 20:00", "发布时间", "published"), txt("desc", "这是编的商品简介。", "简介", "desc"),
				txt("variations", "规格：\n通常版 ¥ 600\n特装版 ¥ 1,200（售罄）", "规格", "variations"),
				slot("cover", "封面", "封面", "cover"), slot("images", "图片", "图片", "images"),
			})...),
			// BOOTH（用户 2026-11-12）：图最多 3 张（$图片，商品图按原顺序），价格之后列出各个规格和价格
			Direct: "$图片$标题[\n店铺：$店铺][\n价格：$价格][\n$规格][\n分类：$分类][\n状态：$状态][\n收藏：$收藏][\n上架：$发布时间]{{简介}}\n$链接"},
		{Key: "steam", Label: "Steam",
			Set: NewSet(join(common("Steam 游戏"), []Field{
				txt("title", "纸船物语", "标题", "title"), txt("developer", "纸舟工作室", "开发商", "developer"), txt("publisher", "纸舟发行", "发行商", "publisher"),
				txt("price", "¥ 68.00", "价格", "price"), txt("discount", "-20%", "折扣", "discount"), txt("released", "2025 年 11 月 16 日", "发行日期", "released"),
				txt("tags", "冒险、独立", "标签", "tags"), txt("recommendations", "3200", "推荐数", "recommendations"), txt("age", "17+", "年龄限制", "age"),
				txt("desc", "这是编的游戏简介。", "简介", "desc"), slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: "$封面$标题[\n开发商：$开发商][\n发行商：$发行商][\n价格：$价格][\n折扣：$折扣][\n发行：$发行日期][\n标签：$标签][\n推荐数：$推荐数][\n年龄限制：$年龄限制]{{简介}}\n$链接"},
		{Key: "afdian", Label: "爱发电", DescLimit: 300,
			Set: NewSet(join(common("爱发电动态"), []Field{
				txt("title", "纸船日记", "标题", "title"), txt("author", "纸云", "作者", "author"), txt("published", "11-01 18:30", "发布时间", "published"),
				txt("likes", "12", "点赞", "likes"), txt("comments", "3", "评论", "comments"), txt("posts", "40", "动态数", "posts"),
				txt("status", "需要赞助才能看全文，最低 ¥30", "状态", "status"), txt("desc", "这是编的正文开头。", "简介", "desc"),
				slot("cover", "封面", "封面", "cover"), slot("images", "图片", "图片", "images"), slot("video", "视频", "视频", "video"),
			})...),
			// 免费视频能下载就发视频（$视频 单独一条，用户 2026-11-12）；图用 $图片（全部配图，直发最多 3 张、多了合并转发，不再只发第一张）
			Direct: "$图片$标题[\n作者：$作者][\n发布：$发布时间][\n[点赞 $点赞][ | 评论 $评论][ | 动态 $动态数]][\n$状态]{{简介}}\n$链接$视频"},
		{Key: "bluesky", Label: "Bluesky",
			Set: NewSet(join(common("Bluesky 帖子"), []Field{
				txt("author", "纸云 @paper.example.invalid", "作者", "author"), txt("name", "纸云", "昵称", "name"), txt("account", "paper.example.invalid", "账号", "account"),
				txt("published", "11-01 18:30", "发布时间", "published"), txt("text", "这是一条编的帖子正文。", "简介", "text"), txt("bodytext", "这是一条编的帖子正文。", "正文文字", "bodytext"), txt("colon", "：", "冒号", "colon"),
				txt("likes", "5", "点赞", "likes"), txt("reposts", "1", "转发", "reposts"), txt("replies", "2", "回复", "replies"), txt("quotes", "0", "引用", "quotes"),
				txt("followers", "100", "粉丝", "followers"), txt("follows", "20", "关注", "follows"), txt("posts", "300", "帖子数", "posts"),
				txt("joined", "2016-03-16", "注册时间", "joined"),
				txt("stats", "点赞 5 | 转发 1 | 回复 2 | 引用 0", "统计", "stats"), txt("sensitive", "是", "敏感", "sensitive"),
				slot("body", "正文", "正文", "body"), slot("images", "图片", "图片", "images"), slot("video", "视频", "视频", "video"),
			})...),
			Direct: postHead + "[\n$发布时间][\n注册：$注册时间][\n敏感内容：$敏感][\n$统计]$图片$视频"},
		{Key: "telegram", Label: "Telegram", ForwardOverBytes: DefaultForwardOverBytes,
			Set: NewSet(join(common("Telegram 帖子"), []Field{
				txt("author", "纸云频道 @paper_cloud", "作者", "author"), txt("name", "纸云频道", "频道名", "name", "昵称"), txt("account", "paper_cloud", "账号", "account"),
				txt("published", "11-01 18:30", "发布时间", "published"), txt("text", "这是一条编的频道帖子正文。", "简介", "text"), txt("bodytext", "这是一条编的频道帖子正文。", "正文文字", "bodytext"), txt("colon", "：", "冒号", "colon"),
				txt("views", "1.2万", "浏览", "views"), txt("stats", "浏览 1.2万", "统计", "stats"),
				slot("body", "正文", "正文", "body"), slot("images", "图片", "图片", "images"), slot("video", "视频", "视频", "video"),
			})...),
			// 合并转发里帖子信息、正文、图放同一个节点（用户 2026-11-14）：链接、作者行（有正文才带冒号）、正文、时间、统计，图跟在后面；
			// 视频仍单独成节点（toNodes）。不写「」：转发里本来就是整段正文。
			Direct:  postHead + "[\n$发布时间][\n$统计]$图片$视频",
			Forward: "$合并转发$链接[\n[$昵称][" + `\[@$账号\]` + "][$冒号]][\n$正文文字][\n$发布时间][\n$统计]$图片$视频"},
		{Key: "misskey", Label: "Misskey / Mastodon",
			Set: NewSet(join(common("Misskey 帖子"), []Field{
				txt("author", "纸云 @paper", "作者", "author"), txt("name", "纸云", "昵称", "name"), txt("account", "paper", "账号", "account"),
				txt("published", "11-01 18:30", "发布时间", "published"), txt("text", "这是一条编的帖子正文。", "简介", "text"), txt("bodytext", "这是一条编的帖子正文。", "正文文字", "bodytext"), txt("colon", "：", "冒号", "colon"),
				txt("warning", "剧透", "内容提醒", "warning"), txt("sensitive", "是", "敏感", "sensitive"), txt("joined", "2016-03-16", "注册时间", "joined"),
				txt("stats", "转发 1 | 回复 2 | 反应 5", "统计", "stats"),
				slot("body", "正文", "正文", "body"), slot("images", "图片", "图片", "images"), slot("video", "视频", "视频", "video"),
			})...),
			Direct: postHead + "[\n$发布时间][\n注册：$注册时间][\n内容提醒：$内容提醒][\n敏感内容：$敏感][\n$统计]$图片$视频"},
		{Key: "spotify", Label: "Spotify",
			Set: NewSet(join(common("Spotify 单曲"), []Field{
				txt("title", "纸船", "标题", "title"), txt("artist", "河岸合奏团", "歌手", "artist"), txt("show", "纸舟电台", "节目", "show"),
				txt("released", "2026-03-20", "发行日期", "released"), txt("duration", "03:20", "时长", "duration"), txt("explicit", "是", "显式标记", "explicit"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: "$封面$标题[\n歌手：$歌手][\n节目：$节目][\n发行：$发行日期][\n时长：$时长][\n显式内容：$显式标记]\n$链接"},
		{Key: "tiktok", Label: "TikTok",
			Set: NewSet(join(common("TikTok 视频"), []Field{
				txt("title", "纸灯", "标题", "title"), txt("text", "这是编的作品文案。", "简介", "text"),
				txt("author", "纸灯 @paper_lamp", "作者", "author"), txt("published", "11-01 13:00", "发布时间", "published"), txt("duration", "00:21", "时长", "duration"),
				txt("views", "1200", "播放", "views"), txt("likes", "32", "点赞", "likes"), txt("comments", "4", "评论", "comments"), txt("shares", "6", "转发", "shares"), txt("collects", "5", "收藏", "collects"),
				txt("followers", "100", "粉丝", "followers"), txt("following", "20", "关注", "following"), txt("hearts", "900", "获赞", "hearts"), txt("videos", "30", "作品数", "videos"),
				txt("verified", "是", "认证", "verified"), txt("when", "11-01 13:00", "时间", "when"),
				slot("cover", "封面", "封面", "cover"), slot("video", "视频", "视频", "video"),
			})...),
			// 视频文字版（B8），结构同 B 站视频；用户主页没有标题和时长，那几行省掉，粉丝那一行只有主页才有
			Direct: "$封面[$标题][\n$简介][\n[$作者][ | $时长]][\n[播放 $播放][ | 点赞 $点赞]][\n[粉丝 $粉丝][ | 关注 $关注][ | 获赞 $获赞][ | 作品 $作品数]][\n$时间]\n$链接$视频"},
		{Key: "bilibili-toy", Label: "B站小游戏",
			Set: NewSet(join(common("B站小游戏"), []Field{
				txt("title", "纸舟小游戏", "标题", "title"), txt("author", "纸灯实验室", "作者", "UP主", "author"), txt("category", "游戏", "分类", "category"),
				txt("published", "10-26 09:00", "发布时间", "published"), txt("when", "10-26 09:00", "时间", "when"),
				txt("plays", "5.1万", "游玩", "plays"), txt("likes", "523", "点赞", "likes"), txt("favorites", "667", "收藏", "favorites"),
				txt("comments", "59", "评论", "comments"), txt("shares", "0", "分享", "shares"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: biliToyText},
		{Key: "bilibili-workshop", Label: "B站小店商品", DescLimit: 200,
			Set: NewSet(join(common("B站小店"), []Field{
				txt("title", "纸舟工房 折纸教程合集", "标题", "title"), txt("shop", "纸舟的小店", "店铺", "shop"),
				txt("price", "¥0.10", "价格", "price"), txt("original", "¥1.00", "原价", "original"), txt("sold", "2万+", "已售", "sold"),
				txt("status", "在售", "状态", "status"), txt("delivery", "线上交付、支付后自动发货", "交付", "delivery"),
				txt("tags", "折纸、教程", "标签", "tags"), txt("desc", "这是编的商品简介。", "简介", "desc"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: biliShopText},
		{Key: "bilibili-article", Label: "B站专栏", ForwardOver: 100, DescLimit: 200,
			Set: NewSet(join(common("B站专栏"), []Field{
				txt("title", "折纸入门笔记", "标题", "title"), txt("author", "纸灯实验室", "UP主", "作者", "author"), txt("published", "11-01 18:30", "发布时间", "published"),
				txt("views", "1200", "阅读", "views"), txt("likes", "52", "点赞", "likes"), txt("comments", "8", "评论", "comments"),
				txt("favorites", "31", "收藏", "favorites"), txt("coins", "10", "投币", "coins"), txt("shares", "4", "转发", "shares"),
				txt("desc", "这是编的专栏开头。", "简介", "desc"),
				txt("hot", "热评：\n1. 用户甲：这是编的评论（赞 12）", "热评", "hot"),
				slot("cover", "封面", "封面", "cover"), slot("body", "正文", "正文", "body"),
			})...),
			Direct:  biliArticleHead + "$正文\n$链接",
			Forward: "$合并转发" + biliArticleHead + "\n$链接$节点$正文"},
		{Key: "bilibili-bangumi", Label: "B站番剧", DescLimit: 200,
			Set: NewSet(join(common("B站番剧"), []Field{
				txt("title", "纸船物语", "标题", "title"), txt("episode", "第1话 纸船出航", "集", "episode"),
				txt("rating", "9.8", "评分", "rating"), txt("ratingcount", "21万", "评分人数", "ratingcount"), txt("status", "已完结，共12话", "状态", "status"),
				txt("areas", "日本", "地区", "areas"), txt("styles", "冒险、校园", "风格", "styles"), txt("published", "2026-04-12", "首播", "published"),
				txt("views", "2500000", "播放", "views"), txt("danmaku", "39000", "弹幕", "danmaku"), txt("likes", "19000", "点赞", "likes"),
				txt("coins", "23000", "投币", "coins"), txt("follows", "80000", "追番", "follows"), txt("desc", "这是编的番剧简介。", "简介", "desc"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct:  biliBangumi,
			Presets: []Preset{{"封面文字", biliBangumi}}},
		{Key: "bilibili-course", Label: "B站课程", DescLimit: 200,
			Set: NewSet(join(common("B站课程"), []Field{
				txt("title", "纸艺入门课", "标题", "title"), txt("desc", "从零开始学折纸", "简介", "desc"), txt("author", "纸灯实验室", "UP主", "作者", "author"),
				txt("price", "256 B币", "价格", "price"), txt("discount", "券后188 B币", "优惠", "discount"), txt("lessons", "已完结 | 共20课时", "课时", "lessons"),
				txt("trial", "6 课时可试看", "试看", "trial"), txt("views", "1790000", "播放", "views"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: biliCourse},
		{Key: "bilibili-audio", Label: "B站音频", DescLimit: 200,
			Set: NewSet(join(common("B站音频"), []Field{
				txt("title", "纸船谣", "标题", "title"), txt("author", "纸灯实验室", "UP主", "作者", "author"), txt("duration", "03:59", "时长", "duration"),
				txt("published", "11-01 18:30", "发布时间", "published"), txt("views", "13000", "播放", "views"), txt("favorites", "370", "收藏", "favorites"),
				txt("comments", "50", "评论", "comments"), txt("shares", "40", "分享", "shares"), txt("coins", "23", "投币", "coins"),
				txt("desc", "这是编的音频简介。", "简介", "desc"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: biliAudio},
		{Key: "bilibili-collection", Label: "B站合集和系列", DescLimit: 200,
			Set: NewSet(join(common("B站合集"), []Field{
				txt("kind", "合集", "类型", "kind"), txt("title", "折纸教程合集", "标题", "title"), txt("author", "纸灯实验室", "UP主", "作者", "author"),
				txt("videos", "48", "视频数", "videos"), txt("desc", "这是编的合集简介。", "简介", "desc"),
				txt("items", "《第一课》《第二课》《第三课》", "列表", "items"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: biliColl},
		{Key: "default", Label: "默认（其他站点）",
			Set: NewSet(join(common("链接"), []Field{
				txt("title", "示例标题", "标题", "title"), txt("author", "示例作者", "作者", "author"), txt("text", "示例简介", "简介", "text"), txt("published", "11-01 18:30", "发布时间", "published"),
				slot("cover", "封面", "封面", "cover"),
			})...),
			Direct: "$封面$标题[\n$作者][\n$简介]\n$链接"},
	}
}

func douyinFields(video bool) []Field {
	f := []Field{
		txt("text", "抖音文案", "简介", "text"), txt("author", "纸灯", "作者", "author"), txt("published", "11-01 13:00", "发布时间", "published"),
		txt("likes", "32", "点赞", "likes"), txt("comments", "4", "评论", "comments"), txt("favorites", "5", "收藏", "favorites"), txt("shares", "6", "转发", "shares"),
	}
	if video {
		f = append(f, txt("duration", "00:18", "时长", "duration"), txt("when", "11-01 13:00", "时间", "when"))
	}
	return f
}

// Sites 按展示顺序返回全部站点。
func Sites() []Site { return append([]Site(nil), sites...) }

// SiteByKey 按配置键找站点；键可以带 -forward 后缀，返回的 forward 说它是不是转发模板的键。
func SiteByKey(key string) (site Site, forward bool, ok bool) {
	base, isForward := strings.CutSuffix(key, "-forward")
	for _, s := range sites {
		if s.Key == base {
			if isForward && !s.HasForward() {
				return Site{}, false, false
			}
			return s, isForward, true
		}
	}
	return Site{}, false, false
}

// SiteKey 把取数结果的平台和种类对到站点键（查 catalog）；还没有发送格式的返回 default（调用方自己决定发不发）。
func SiteKey(platform, kind string) string {
	if k, ok := LookupKind(platform, kind); ok && k.Support == SupportExpand && k.Site != "" {
		return k.Site
	}
	return "default"
}

// ForwardMode 是每个站点的“合并转发”设置。
const (
	ForwardAuto   = "auto"
	ForwardAlways = "always"
	ForwardNever  = "never"
)

type sampleEnv struct{ set *Set }

func (e sampleEnv) Text(id string) (string, bool) {
	for _, f := range e.set.fields {
		if f.ID == id && f.Kind == KindText {
			return f.Sample, true
		}
	}
	return "", false
}
func (e sampleEnv) Slot(string) bool { return true }

// Preview 用该站点的示例数据渲染一份模板（写法同配置，可以是 @预设名），图片类写成 [封面] 之类的占位；不联网、不取真数据。
func (s Site) Preview(src string) (string, error) {
	tpl, err := s.Parse(src)
	if err != nil {
		return "", err
	}
	label := func(id string) string {
		for _, f := range s.Set.fields {
			if f.ID == id {
				return f.Sample
			}
		}
		return id
	}
	return Describe(tpl.Render(sampleEnv{s.Set}), label), nil
}
