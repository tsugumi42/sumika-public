package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 后来接进自动展开的站点：用编造的取数结果走一遍默认模板，写死期望的文字，字段缺了的地方不写“未返回”。

func siteResult(platform, kind, id string) linkreader.Result {
	r := goldenBase(platform, kind, id)
	r.Author = linkreader.Author{
		Name: nr[string](), Account: nr[string](), ID: nr[string](), Avatar: nr[string](),
	}
	r.Published = nr[time.Time]()
	r.Title, r.Text = nr[string](), nr[string]()
	r.Media = nr[[]linkreader.Media]()
	return r
}

func withMedia(r linkreader.Result, ms ...linkreader.Media) linkreader.Result {
	r.Media = av(ms)
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, linkreader.Block{Type: r.Media.Value[i].Type, Media: &r.Media.Value[i]})
	}
	return r
}

func describe(t *testing.T, r linkreader.Result) string {
	t.Helper()
	if !Supports(r.Platform, r.Kind) {
		t.Fatalf("%s/%s 应当在 Supports 里", r.Platform, r.Kind)
	}
	d := Build(&r, "-71", Options{}, 0)
	return strings.Join(d.Describe(), "\n---\n")
}

func siteCases() []struct {
	name string
	r    linkreader.Result
	want string
} {
	type c = struct {
		name string
		r    linkreader.Result
		want string
	}
	var out []c
	add := func(name string, r linkreader.Result, want string) { out = append(out, c{name, r, want}) }

	// 小黑盒游戏页：没有标题就不写标题行（以前写“游戏 编号”）；发行日期是原文
	r := siteResult("xiaoheihe", "game", "90001")
	r.Author.Name = av("纸舟工作室")
	r.Text = av("这是编的游戏简介。")
	r.PublishedText = av("2025年11月16日")
	add("xiaoheihe-game", r, "（回复原消息）\n开发商：纸舟工作室\n发行：2025年11月16日\nhttps://example.invalid/xiaoheihe/90001")
	// 取数端从 Steam 补到了游戏名：标题行写名字，不写编号；简介照取但默认模板不写
	r = siteResult("xiaoheihe", "game", "90003")
	r.Title = av("纸船物语")
	r.Text = av("编的简介")
	add("xiaoheihe-named", r, "（回复原消息）\n纸船物语\nhttps://example.invalid/xiaoheihe/90003")
	r = siteResult("xiaoheihe", "game", "90002")
	r.Text = av("只有简介")
	add("xiaoheihe-only-desc", r, "（回复原消息）\nhttps://example.invalid/xiaoheihe/90002")

	// BOOTH
	r = siteResult("booth", "item", "90010")
	r.Title, r.Text = av("纸船模型 套装"), av("编的商品说明")
	r.Author.Name, r.Author.Account = av("纸舟商店"), av("paper-shop")
	r.Category = av("3D 角色")
	r.Published = av(time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC))
	r.Stats["wish_lists"] = av(int64(120))
	r.Flags["end_of_sale"] = linkreader.Field[any]{State: "available", Value: true}
	r.Booth = &linkreader.BoothInfo{Price: av("¥ 600~")}
	r = withMedia(r, goldenImage(1))
	add("booth-item", r, "（回复原消息）\n[图片]\n纸船模型 套装\n店铺：纸舟商店\n价格：¥ 600~\n分类：3D 角色\n状态：已结束销售\n收藏：120\n上架：10-01 19:00\nhttps://example.invalid/booth/90010")
	r = siteResult("booth", "item", "90011")
	r.Title = av("只有标题")
	add("booth-bare", r, "（回复原消息）\n只有标题\nhttps://example.invalid/booth/90011")

	// Steam
	r = siteResult("steam", "app", "90020")
	r.Title, r.Text = av("纸船物语"), av("编的游戏简介")
	r.Author.Name = av("纸舟工作室")
	r.PublishedText = av("2025 年 11 月 16 日")
	r.Tags = av([]string{"冒险", "独立"})
	r.Stats["recommendations"] = av(int64(3200))
	r.Steam = &linkreader.SteamInfo{
		Type:        av("game"),
		Price:       av(linkreader.SteamPrice{Formatted: av("¥ 68.00"), DiscountPercent: av(int64(20))}),
		RequiredAge: av(17),
		Publishers:  av([]string{"纸舟发行"}),
	}
	r = withMedia(r, goldenImage(1))
	add("steam-app", r, "（回复原消息）\n[图片]\n纸船物语\n开发商：纸舟工作室\n发行商：纸舟发行\n价格：¥ 68.00\n折扣：-20%\n发行：2025 年 11 月 16 日\n标签：冒险、独立\n推荐数：3200\n年龄限制：17+\nhttps://example.invalid/steam/90020")
	r = siteResult("steam", "app", "90021")
	r.Title = av("免费游戏")
	r.Steam = &linkreader.SteamInfo{IsFree: av(true), Price: nr[linkreader.SteamPrice](), RequiredAge: av(0)}
	add("steam-free", r, "（回复原消息）\n免费游戏\n价格：免费\nhttps://example.invalid/steam/90021")
	r = siteResult("steam", "profile", "90022")
	r.Title, r.Author.Name = av("纸云"), av("纸云")
	add("steam-profile", r, "（回复原消息）\n纸云\nhttps://example.invalid/steam/90022")

	// 爱发电
	r = siteResult("afdian", "post", "90030")
	r.Title, r.Text = av("纸船日记"), av("编的正文开头")
	r.Author.Name = av("纸云")
	r.Published = av(time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC))
	r.Stats["likes"], r.Stats["comments"] = av(int64(12)), av(int64(3))
	add("afdian-post", r, "（回复原消息）\n纸船日记\n作者：纸云\n发布：11-01 18:30\n点赞 12 | 评论 3\nhttps://example.invalid/afdian/90030")
	r = siteResult("afdian", "post", "90031")
	r.Title = av("付费动态")
	r.Text = linkreader.Field[string]{State: "login_required", Reason: "sponsor_required"}
	r.Flags["sponsor_required"] = linkreader.Field[any]{State: "available", Value: true}
	r.Flags["sponsor_min_price"] = linkreader.Field[any]{State: "available", Value: "30.00"}
	add("afdian-locked", r, "（回复原消息）\n付费动态\n需要赞助才能看全文，最低 ¥30.00\nhttps://example.invalid/afdian/90031")

	// Bluesky：帖子带一张图，主页
	r = siteResult("bluesky", "post", "90040/aaaaaaaaaaaaa")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper.example.invalid")
	r.Published = av(time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC))
	r.Title, r.Text = av("这是一条编的帖子正文。"), av("这是一条编的帖子正文。")
	r.Stats["likes"], r.Stats["reposts"], r.Stats["replies"], r.Stats["quotes"] = av(int64(5)), av(int64(1)), av(int64(2)), av(int64(0))
	r.Flags["possibly_sensitive"] = linkreader.Field[any]{State: "available", Value: true}
	r = withMedia(r, goldenImage(1))
	r.Blocks = append([]linkreader.Block{{Type: "text", Text: r.Text.Value}}, r.Blocks...)
	add("bluesky-post", r, "（回复原消息）\nhttps://example.invalid/bluesky/90040/aaaaaaaaaaaaa\n纸云[@paper.example.invalid]：\n「这是一条编的帖子正文。」\n11-01 18:30\n敏感内容：是\n点赞 5 | 转发 1 | 回复 2 | 引用 0\n[图片]")
	r = siteResult("bluesky", "profile", "paper.example.invalid")
	r.Title, r.Text = av("纸云"), av("编的个人简介")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper.example.invalid")
	r.Stats["followers"], r.Stats["follows"], r.Stats["posts"] = av(int64(100)), av(int64(20)), av(int64(300))
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	add("bluesky-profile", r, "（回复原消息）\nhttps://example.invalid/bluesky/paper.example.invalid\n纸云[@paper.example.invalid]：\n「编的个人简介」\n粉丝 100 | 关注 20 | 帖子 300")

	// Misskey / Mastodon
	r = siteResult("misskey", "note", "misskey.io/90050")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper")
	r.Text = av("编的笔记")
	r.Flags["content_warning"] = linkreader.Field[any]{State: "available", Value: "剧透"}
	r.Stats["renotes"], r.Stats["replies"], r.Stats["reactions"] = av(int64(1)), av(int64(2)), av(int64(5))
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	add("misskey-note", r, "（回复原消息）\nhttps://example.invalid/misskey/misskey.io/90050\n纸云[@paper]：\n「编的笔记」\n内容提醒：剧透\n转发 1 | 回复 2 | 反应 5")
	r = siteResult("mastodon", "status", "mastodon.social/90051")
	r.Author.Account = av("paper")
	r.Text = av("编的嘟文")
	r.Stats["favourites"], r.Stats["reblogs"], r.Stats["replies"] = av(int64(4)), av(int64(0)), av(int64(1))
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	add("mastodon-status-no-name", r, "（回复原消息）\nhttps://example.invalid/mastodon/mastodon.social/90051\n[@paper]：\n「编的嘟文」\n点赞 4 | 转发 0 | 回复 1")

	// Spotify：单曲有时长和封面；歌单只有标题和封面
	r = siteResult("spotify", "track", "0000000000000000000001")
	r.Title, r.Author.Name = av("纸船"), av("河岸合奏团")
	r.Published = av(time.Date(2020, 3, 20, 0, 0, 0, 0, time.UTC))
	r.Flags["explicit"] = linkreader.Field[any]{State: "available", Value: true}
	audio := linkreader.Media{Type: "audio", URL: linkreader.Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Duration: av(200.04)}
	r = withMedia(r, audio, goldenImage(1))
	add("spotify-track", r, "（回复原消息）\n[图片]\n纸船\n歌手：河岸合奏团\n发行：2020-03-20\n时长：03:20\n显式内容：是\nhttps://example.invalid/spotify/0000000000000000000001")
	r = siteResult("spotify", "episode", "0000000000000000000002")
	r.Title, r.Author.Name = av("第一集"), av("纸舟电台")
	add("spotify-episode", r, "（回复原消息）\n第一集\n节目：纸舟电台\nhttps://example.invalid/spotify/0000000000000000000002")
	r = siteResult("spotify", "playlist", "0000000000000000000003")
	r.Title = av("编的歌单")
	add("spotify-playlist", r, "（回复原消息）\n编的歌单\nhttps://example.invalid/spotify/0000000000000000000003")

	// TikTok：没写文案的视频也要出；用户页
	r = siteResult("tiktok", "video", "7000000000000000001")
	r.Author.Name, r.Author.Account = av("纸灯"), av("paper_lamp")
	r.Stats["views"], r.Stats["likes"] = av(int64(1200)), av(int64(32))
	r.Stats["comments"] = linkreader.Field[int64]{State: "not_returned", Reason: "invalid_value"}
	video := linkreader.Media{Type: "video", URL: linkreader.Field[string]{State: "unsupported", Reason: "session_cookie_required"}, Thumbnail: av("https://example.invalid/cover.jpg"), Duration: av(21.0)}
	r = withMedia(r, video)
	add("tiktok-video-no-desc", r, "（回复原消息）\n[图片]\n纸灯 @paper_lamp | 00:21\n播放 1200 | 点赞 32\nhttps://example.invalid/tiktok/7000000000000000001")
	r = siteResult("tiktok", "user", "paper_lamp")
	r.Title, r.Text = av("纸灯"), av("编的签名")
	r.Author.Name, r.Author.Account = av("纸灯"), av("paper_lamp")
	r.Stats["followers"], r.Stats["following"], r.Stats["heart"], r.Stats["videos"] = av(int64(100)), av(int64(20)), av(int64(900)), av(int64(30))
	r.Flags["verified"] = linkreader.Field[any]{State: "available", Value: true}
	add("tiktok-user", r, "（回复原消息）\n纸灯\n编的签名\n@paper_lamp\n粉丝 100 | 关注 20 | 获赞 900 | 作品 30\nhttps://example.invalid/tiktok/paper_lamp")
	// B 站直播间：取数端把直播间的关注数记成粉丝，文字写“粉丝”，不写“关注”
	r = siteResult("bilibili", "live", "900001")
	r.Title, r.Author.Name = av("纸船试航直播"), av("纸灯实验室")
	r.Flags["live_status"] = linkreader.Field[any]{State: "available", Value: 1}
	r.Stats["online"], r.Stats["followers"] = av(int64(1200)), av(int64(8800))
	add("bilibili-live", r, "（回复原消息）\n纸船试航直播\n主播：纸灯实验室\n状态：直播中\n人气 1200 | 粉丝 8800\nhttps://example.invalid/bilibili/900001")
	return out
}

func TestNewSitesDefaultTemplates(t *testing.T) {
	for _, c := range siteCases() {
		t.Run(c.name, func(t *testing.T) {
			got := describe(t, c.r)
			if got != c.want {
				t.Fatalf("输出不对：\n现在：\n%s\n想要：\n%s", got, c.want)
			}
			if strings.Contains(got, "未返回") {
				t.Fatalf("缺的字段不该写“未返回”：\n%s", got)
			}
		})
	}
}

// 取不到文件的媒体（Spotify 试听、TikTok 视频、Steam 预告）不排进消息；Bluesky 视频有地址时照排。
func TestUnsendableMediaStaysOutOfBody(t *testing.T) {
	r := siteResult("bluesky", "post", "90041/aaaaaaaaaaaab")
	r.Text = av("正文")
	pending := linkreader.Media{Type: "video", URL: linkreader.Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Thumbnail: av("https://example.invalid/t.jpg")}
	r = withMedia(r, pending, goldenImage(1))
	r.Blocks = append([]linkreader.Block{{Type: "text", Text: "正文"}}, r.Blocks...)
	d := Build(&r, "-71", Options{}, 0)
	items := d.Parts[0].Body
	videos, images := 0, 0
	for _, it := range items {
		switch it.Kind {
		case types.BodyVideo:
			videos++
		case types.BodyImage:
			images++
		}
	}
	if videos != 0 || images != 1 {
		t.Fatalf("视频 %d、图片 %d：取不到的视频不该排进去：%+v", videos, images, items)
	}
}

// 没有任何可发内容（标题、正文、作者、媒体都没取到）时不发任何文字，只标 NoContent 让调用方当失败处理。
func TestNewSiteWithNothingFails(t *testing.T) {
	r := siteResult("tiktok", "video", "7000000000000000009")
	d := Build(&r, "-71", Options{}, 0)
	if len(d.Parts) != 0 || !d.NoContent {
		t.Fatalf("%+v", d)
	}
}
