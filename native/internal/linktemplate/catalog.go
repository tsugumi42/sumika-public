package linktemplate

import "slices"

// 站点和页面类型清单：一张表说清每个网站有哪些页面类型、各自支持到哪一步。
// 设置页的站点列表、展开决策（Supports）、站点键（SiteKey）、样张工具都读它；
// 新接一个站或一种页面，只在 catalog 里加一行，再到 sites.go 加对应的模板站点。
// 取数的分发在 internal/linkreader/fetch.go，识别在 recognize*.go，两处不读这张表；
// internal/linkexpand 的 catalog_test 用每行的 Example 把识别结果和这张表对一遍。

// Support 是一种页面类型现在能做到哪一步。
type Support int

const (
	SupportNone   Support = iota // 不支持：识别不出来，或只认得出链接、没有取数
	SupportFetch                 // 只能取数：能取到内容（给角色看、预览用），但不自动展开发出去
	SupportExpand                // 能展开：群里发链接会自动展开成消息
)

func (s Support) String() string {
	switch s {
	case SupportFetch:
		return "fetch"
	case SupportExpand:
		return "expand"
	}
	return "none"
}

// Label 是设置页上写的状态词。
func (s Support) Label() string {
	switch s {
	case SupportFetch:
		return "只能取数"
	case SupportExpand:
		return "能展开"
	}
	return "不支持"
}

// CatalogKind 是一个网站里的一种页面类型。
type CatalogKind struct {
	ID      string   // 配置键和取数结果里的 Kind（kinds.<ID>）
	Aliases []string // 识别或取数时的别名，对到同一行（例：B 站 opus 对到动态）
	Label   string   // 设置页显示的名字
	Support Support
	Site    string // 能展开时对应的模板站点键（sites.go）
	Note    string // 不支持或只能取数时写原因，一句话
	Example string // 编的示例链接，测试用它核对识别结果；没有就空
}

// CatalogPlatform 是一个网站。
type CatalogPlatform struct {
	ID    string // 配置键（platforms.<ID>），也是取数结果里的 Platform
	Label string
	Kinds []CatalogKind
}

const (
	noteFetchOnly = "能取数，还没有发送格式"
)

var catalog = []CatalogPlatform{
	{ID: "bilibili", Label: "哔哩哔哩", Kinds: []CatalogKind{
		{ID: "video", Label: "视频", Support: SupportExpand, Site: "bilibili-video", Example: "https://www.bilibili.com/video/BV1xx411c7mD"},
		{ID: "dynamic", Aliases: []string{"opus"}, Label: "动态和图文", Support: SupportExpand, Site: "bilibili-dynamic", Example: "https://t.bilibili.com/900000000000001"},
		{ID: "article", Label: "专栏", Support: SupportExpand, Site: "bilibili-article", Example: "https://www.bilibili.com/read/cv900001"},
		{ID: "live", Label: "直播间", Support: SupportExpand, Site: "bilibili-live", Example: "https://live.bilibili.com/900001"},
		{ID: "user", Label: "个人主页", Support: SupportExpand, Site: "bilibili-space", Example: "https://space.bilibili.com/900002"},
		{ID: "mall", Label: "会员购", Support: SupportExpand, Site: "bilibili-mall", Example: "https://mall.bilibili.com/neul-next/index.html?page=detailuniversal_detail&itemsId=900003"},
		{ID: "episode", Aliases: []string{"season", "media"}, Label: "番剧", Support: SupportExpand, Site: "bilibili-bangumi", Example: "https://www.bilibili.com/bangumi/play/ep900004"},
		{ID: "course", Label: "课程", Support: SupportExpand, Site: "bilibili-course", Example: "https://www.bilibili.com/cheese/play/ss900005"},
		{ID: "audio", Label: "音频", Support: SupportExpand, Site: "bilibili-audio", Example: "https://www.bilibili.com/audio/au900006"},
		{ID: "collection", Label: "合集和系列", Support: SupportExpand, Site: "bilibili-collection", Example: "https://space.bilibili.com/900007/lists/900008?type=season"},
		{ID: "workshop", Label: "小店商品", Support: SupportExpand, Site: "bilibili-workshop", Example: "https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail&itemsId=900009"},
		{ID: "toy", Label: "小游戏", Support: SupportExpand, Site: "bilibili-toy", Example: "https://www.bilibili.com/toy/zhizhou-youxi/index.html"},
		// 下面几种还没做：不认链接，原因写在这里，拿到样本再补（docs/待办.md）。
		{ID: "favlist", Label: "收藏夹和播单", Support: SupportNone, Note: "还不认这种链接：要先拿到公开收藏夹的真实链接样本"},
		{ID: "upower_dynamic", Label: "充电专属动态", Support: SupportNone, Note: "没有可核实的样本，没充电能看到什么还不清楚"},
		{ID: "live_replay", Label: "直播回放", Support: SupportNone, Note: "还不认这种链接：没有真实的回放链接样本"},
		{ID: "ticket", Label: "票务、市集和魔力赏", Support: SupportNone, Note: "编号和会员购商品不是一套，也没有取数来源"},
		{ID: "other", Label: "话题、漫画、国际站", Support: SupportNone, Note: "要登录、按地区限制，或内容随时变，先不做"},
	}},
	{ID: "x", Label: "X / 推特", Kinds: []CatalogKind{
		{ID: "post", Aliases: []string{"article"}, Label: "推文、长文和投票", Support: SupportExpand, Site: "x", Example: "https://x.com/paper_cloud/status/1900000000000000001"},
	}},
	{ID: "pixiv", Label: "pixiv", Kinds: []CatalogKind{
		{ID: "artwork", Label: "插画、漫画和动图", Support: SupportExpand, Site: "pixiv-artwork", Example: "https://www.pixiv.net/artworks/90000001"},
		{ID: "novel", Label: "小说", Support: SupportExpand, Site: "pixiv-novel", Example: "https://www.pixiv.net/novel/show.php?id=90000002"},
		{ID: "series", Label: "漫画系列", Support: SupportExpand, Site: "pixiv-series", Example: "https://www.pixiv.net/user/90005/series/90003"},
		{ID: "novel_series", Label: "小说系列", Support: SupportExpand, Site: "pixiv-series", Example: "https://www.pixiv.net/novel/series/90004"},
	}},
	{ID: "github", Label: "GitHub", Kinds: []CatalogKind{
		{ID: "repository", Label: "仓库", Support: SupportExpand, Site: "github", Example: "https://github.com/paper-lab/boat"},
		{ID: "issue", Label: "Issue", Support: SupportFetch, Note: noteFetchOnly, Example: "https://github.com/paper-lab/boat/issues/7"},
		{ID: "pull_request", Label: "合并请求", Support: SupportFetch, Note: noteFetchOnly, Example: "https://github.com/paper-lab/boat/pull/8"},
		{ID: "release", Label: "发布版本", Support: SupportFetch, Note: noteFetchOnly, Example: "https://github.com/paper-lab/boat/releases/tag/v1.0"},
		{ID: "gist", Label: "Gist", Support: SupportFetch, Note: noteFetchOnly},
	}},
	{ID: "youtube", Label: "YouTube", Kinds: []CatalogKind{
		{ID: "video", Label: "视频", Support: SupportExpand, Site: "youtube", Example: "https://www.youtube.com/watch?v=aaaaaaaaaaa"},
	}},
	{ID: "douyin", Label: "抖音", Kinds: []CatalogKind{
		{ID: "video", Label: "视频", Support: SupportExpand, Site: "douyin-video", Example: "https://www.douyin.com/video/7000000000000000001"},
		{ID: "note", Label: "图集", Support: SupportExpand, Site: "douyin-note", Example: "https://www.douyin.com/note/7000000000000000002"},
	}},
	{ID: "netease", Label: "网易云音乐", Kinds: []CatalogKind{
		{ID: "song", Label: "歌曲", Support: SupportExpand, Site: "netease", Example: "https://music.163.com/song?id=900001"},
		{ID: "playlist", Label: "歌单", Support: SupportFetch, Note: noteFetchOnly, Example: "https://music.163.com/playlist?id=900002"},
		{ID: "album", Label: "专辑", Support: SupportFetch, Note: noteFetchOnly, Example: "https://music.163.com/album?id=900003"},
	}},
	{ID: "steam", Label: "Steam", Kinds: []CatalogKind{
		{ID: "app", Label: "游戏", Support: SupportExpand, Site: "steam", Example: "https://store.steampowered.com/app/900001"},
		{ID: "package", Label: "礼包", Support: SupportExpand, Site: "steam"},
		{ID: "sharedfile", Label: "创意工坊", Support: SupportExpand, Site: "steam"},
		{ID: "profile", Label: "个人资料页", Support: SupportExpand, Site: "steam"},
		{ID: "reviews", Label: "评测", Support: SupportNone, Note: "还不认这种链接：调研时评测接口匿名请求都被跳回首页，代码不识别、不请求（规格-链接解析 4.11）"},
	}},
	{ID: "booth", Label: "BOOTH", Kinds: []CatalogKind{
		{ID: "item", Label: "商品", Support: SupportExpand, Site: "booth"},
	}},
	{ID: "afdian", Label: "爱发电", Kinds: []CatalogKind{
		{ID: "creator", Label: "创作者主页", Support: SupportExpand, Site: "afdian"},
		{ID: "post", Label: "动态", Support: SupportExpand, Site: "afdian"},
		{ID: "album", Label: "专辑", Support: SupportExpand, Site: "afdian"},
		{ID: "album_post", Label: "专辑里的文章", Support: SupportExpand, Site: "afdian"},
		{ID: "item", Label: "商品", Support: SupportFetch, Note: noteFetchOnly},
	}},
	{ID: "bluesky", Label: "Bluesky", Kinds: []CatalogKind{
		{ID: "post", Label: "帖子", Support: SupportExpand, Site: "bluesky"},
		{ID: "profile", Label: "个人主页", Support: SupportExpand, Site: "bluesky"},
	}},
	{ID: "telegram", Label: "Telegram", Kinds: []CatalogKind{
		{ID: "post", Label: "公开频道帖子", Support: SupportExpand, Site: "telegram"},
	}},
	{ID: "misskey", Label: "Misskey", Kinds: []CatalogKind{
		{ID: "note", Label: "帖子", Support: SupportExpand, Site: "misskey"},
		{ID: "user", Label: "个人主页", Support: SupportExpand, Site: "misskey"},
	}},
	{ID: "mastodon", Label: "Mastodon", Kinds: []CatalogKind{
		{ID: "status", Label: "嘟文", Support: SupportExpand, Site: "misskey"},
		{ID: "account", Label: "个人主页", Support: SupportExpand, Site: "misskey"},
	}},
	{ID: "spotify", Label: "Spotify", Kinds: []CatalogKind{
		{ID: "track", Label: "单曲", Support: SupportExpand, Site: "spotify"},
		{ID: "album", Label: "专辑", Support: SupportExpand, Site: "spotify"},
		{ID: "playlist", Label: "歌单", Support: SupportExpand, Site: "spotify"},
		{ID: "artist", Label: "艺人", Support: SupportExpand, Site: "spotify"},
		{ID: "episode", Label: "播客单集", Support: SupportExpand, Site: "spotify"},
		{ID: "show", Label: "播客", Support: SupportFetch, Note: noteFetchOnly},
	}},
	{ID: "tiktok", Label: "TikTok", Kinds: []CatalogKind{
		{ID: "video", Label: "视频", Support: SupportExpand, Site: "tiktok"},
		{ID: "user", Label: "个人主页", Support: SupportExpand, Site: "tiktok"},
	}},
	{ID: "xiaohongshu", Label: "小红书", Kinds: []CatalogKind{
		{ID: "note", Label: "笔记", Support: SupportFetch, Note: noteFetchOnly},
		{ID: "user", Label: "个人主页", Support: SupportFetch, Note: noteFetchOnly},
	}},
	{ID: "xiaoheihe", Label: "小黑盒", Kinds: []CatalogKind{
		{ID: "post", Label: "帖子", Support: SupportFetch, Note: noteFetchOnly},
		{ID: "game", Label: "游戏页", Support: SupportExpand, Site: "xiaoheihe"},
		{ID: "user", Label: "个人主页", Support: SupportFetch, Note: noteFetchOnly},
	}},
}

// Catalog 按展示顺序返回全部网站（拷贝，调用方可以改）。
func Catalog() []CatalogPlatform {
	out := make([]CatalogPlatform, len(catalog))
	for i, p := range catalog {
		p.Kinds = slices.Clone(p.Kinds)
		out[i] = p
	}
	return out
}

// PlatformByID 按网站 ID 找网站。
func PlatformByID(id string) (CatalogPlatform, bool) {
	for _, p := range catalog {
		if p.ID == id {
			return p, true
		}
	}
	return CatalogPlatform{}, false
}

// LookupKind 按网站和页面类型（可以是别名）找清单里的一行。
func LookupKind(platform, kind string) (CatalogKind, bool) {
	p, ok := PlatformByID(platform)
	if !ok {
		return CatalogKind{}, false
	}
	for _, k := range p.Kinds {
		if k.ID == kind || slices.Contains(k.Aliases, kind) {
			return k, true
		}
	}
	return CatalogKind{}, false
}

// CanExpand 说这种页面有没有发送格式（群里发链接会不会自动展开）；只看清单，不看用户的开关。
func CanExpand(platform, kind string) bool {
	k, ok := LookupKind(platform, kind)
	return ok && k.Support == SupportExpand && k.Site != ""
}

// PlatformTranslatable 说一个网站有没有页面类型接了 $译文 字段（设置页据此显示这个网站的翻译开关）。
func PlatformTranslatable(p CatalogPlatform) bool {
	for _, k := range p.Kinds {
		if k.Support != SupportExpand || k.Site == "" {
			continue
		}
		if site, _, ok := SiteByKey(k.Site); ok {
			if _, has := site.Set.Lookup("translation"); has {
				return true
			}
		}
	}
	return false
}
