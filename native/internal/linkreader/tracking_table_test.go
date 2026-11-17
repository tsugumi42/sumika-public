package linkreader

import (
	"context"
	"net/http"
	"testing"
)

// 清链接总表：每个平台至少一条 App 分享形态的脏链接（编号都是编的）。
// rec 是 Recognize 的 Clean（空串表示不认识）；any 是 CleanAny，也就是消息里随手出现的链接走的那条路。
// 保留表（时间点、分 P、播放列表）和“表外参数保留”都靠这张表守住。
var cleanTable = []struct{ name, in, rec, any string }{
	{"B站QQ小程序卡片", "https://www.bilibili.com/video/BV1xx411c7mD?mid=1QYt0BuLWBJmqGEsmC1AnQ%3D%3D&p=1",
		"https://www.bilibili.com/video/BV1xx411c7mD", "https://www.bilibili.com/video/BV1xx411c7mD"},
	{"B站视频带分P时间点", "https://www.bilibili.com/video/BV1xx411c7mD?p=3&t=90&spm_id_from=333.1007.tianma.1-1-1.click&vd_source=abcdef&from_source=share&seid=123&session_id=a&launch_id=1&up_id=2&spmid=x&jumpFrom=y&share_plat=android&share_from=ugc",
		"https://www.bilibili.com/video/BV1xx411c7mD?p=3&t=90", "https://www.bilibili.com/video/BV1xx411c7mD?p=3&t=90"},
	{"B站手机站进度和片段", "https://m.bilibili.com/video/BV1xx411c7mD?share_source=copy_web&start_progress=12000&p=2&buvid=a#t=5",
		"https://www.bilibili.com/video/BV1xx411c7mD?start_progress=12000&p=2#t=5", "https://m.bilibili.com/video/BV1xx411c7mD?start_progress=12000&p=2#t=5"},
	{"B站App路由参数", "https://www.bilibili.com/video/BV1xx411c7mD?-Arouter=story&p=2&t=8",
		"https://www.bilibili.com/video/BV1xx411c7mD?p=2&t=8", "https://www.bilibili.com/video/BV1xx411c7mD?p=2&t=8"},
	{"B站会员购详情页参数", "https://mall.bilibili.com/neul-next/detailuniversal/detail.html?itemsId=70001&page=detailuniversal_detail",
		"https://mall.bilibili.com/neul-next/detailuniversal/detail.html?itemsId=70001", "https://mall.bilibili.com/neul-next/detailuniversal/detail.html?itemsId=70001"},
	{"b23短链保留t", "https://b23.tv/AbCdEf?share_medium=android&share_source=copy_link&t=30",
		"https://b23.tv/AbCdEf?t=30", "https://b23.tv/AbCdEf?t=30"},
	{"B站嵌入播放器带分P", "https://player.bilibili.com/player.html?bvid=BV1xx411c7mD&page=2&t=30&autoplay=0",
		"https://www.bilibili.com/video/BV1xx411c7mD?p=2&t=30", "https://player.bilibili.com/player.html?bvid=BV1xx411c7mD&page=2&t=30&autoplay=0"},
	{"B站活动页带分P", "https://www.bilibili.com/festival/2026demo?bvid=BV1xx411c7mD&spm_id_from=1&p=4",
		"https://www.bilibili.com/video/BV1xx411c7mD?p=4", "https://www.bilibili.com/festival/2026demo?bvid=BV1xx411c7mD&p=4"},
	{"B站番剧编号短码带t", "https://b23.tv/ep123456?t=60&share_source=x",
		"https://www.bilibili.com/bangumi/play/ep123456?t=60", "https://b23.tv/ep123456?t=60"},
	{"B站番剧正式页带t", "https://www.bilibili.com/bangumi/play/ep123456?t=60&spm_id_from=333.1&from_spmid=x",
		"https://www.bilibili.com/bangumi/play/ep123456?t=60", "https://www.bilibili.com/bangumi/play/ep123456?t=60"},
	{"YouTube短链带t", "https://youtu.be/dQw4w9WgXcQ?si=abc&t=43",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=43", "https://youtu.be/dQw4w9WgXcQ?t=43"},
	{"YouTube播放列表", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL12345&index=3&t=1m30s&pp=ygU&feature=shared&ab_channel=Foo&app=desktop&si=x",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL12345&index=3&t=1m30s", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL12345&index=3&t=1m30s"},
	{"YouTube手机站start和time_continue", "https://m.youtube.com/watch?v=dQw4w9WgXcQ&start=30&time_continue=30&feature=youtu.be",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&start=30&time_continue=30", "https://m.youtube.com/watch?v=dQw4w9WgXcQ&start=30&time_continue=30"},
	{"YouTube短视频", "https://www.youtube.com/shorts/dQw4w9WgXcQ?feature=share",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "https://www.youtube.com/shorts/dQw4w9WgXcQ"},
	{"YouTube大小写和转义的键", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&%53I=x&T=10&Feature=x",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&T=10", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&T=10"},
	{"X推文删s和t", "https://x.com/someone/status/1234567890123?s=20&t=AbCdEf&ref_src=twsrc%5Etfw",
		"https://x.com/someone/status/1234567890123", "https://x.com/someone/status/1234567890123"},
	{"X搜索页的s和t不是追踪", "https://twitter.com/search?q=go&t=1&s=typed_query",
		"", "https://twitter.com/search?q=go&t=1&s=typed_query"},
	{"pixiv", "https://www.pixiv.net/artworks/12345678?utm_source=x&ref=y&from=z",
		"https://www.pixiv.net/artworks/12345678", "https://www.pixiv.net/artworks/12345678"},
	{"GitHub邮件链接保留片段", "https://github.com/octo/repo/issues/12?email_token=a&email_source=notifications&notification_referrer_id=NT_x#issuecomment-99",
		"https://github.com/octo/repo/issues/12#issuecomment-99", "https://github.com/octo/repo/issues/12#issuecomment-99"},
	{"抖音", "https://www.douyin.com/video/7300000000000000000?previous_page=app_code_link&enter_from=link&share_app_id=1128&share_link_id=a-b&iid=1&u_code=x&did=2&modal_id=7300000000000000000",
		"https://www.douyin.com/video/7300000000000000000?modal_id=7300000000000000000", "https://www.douyin.com/video/7300000000000000000?modal_id=7300000000000000000"},
	{"TikTok", "https://www.tiktok.com/@some.user/video/7300000000000000001?_t=8abc&_r=1&is_from_webapp=1&sender_device=pc&web_id=123&share_app_id=1233&share_link_id=a&lang=en",
		"https://www.tiktok.com/@some.user/video/7300000000000000001?lang=en", "https://www.tiktok.com/@some.user/video/7300000000000000001?lang=en"},
	{"网易云", "https://music.163.com/song?id=1234567&uct2=abc&dlt=0846&app_version=9.1&sc=wm&tn=&userid=1&uct=x",
		"https://music.163.com/song?id=1234567", "https://music.163.com/song?id=1234567"},
	{"网易云微信分享页", "https://music.163.com/song?id=2751093063&fx-wechatnew=t1&fx-wxqd=&fx-wordtest=&fx-listentest=t3&ts-wakeup=t1&ts-wakeup-reversal=&H5_DownloadVIPGift=&playerUIModeId=76001&PlayerStyles_SynchronousSharing=t3",
		"https://music.163.com/song?id=2751093063", "https://music.163.com/song?id=2751093063"},
	{"网易云片段路由", "https://music.163.com/#/song?id=1234567&uct2=x&dlt=0846",
		"https://music.163.com/song?id=1234567", "https://music.163.com/song?id=1234567"},
	{"Spotify播客保留t", "https://open.spotify.com/episode/4uLU6hMCjMI75M1A2tKUQC?si=abc&context=spotify%3Aplaylist%3Aabc&nd=1&dlsi=x&utm_source=copy-link&t=300",
		"https://open.spotify.com/episode/4uLU6hMCjMI75M1A2tKUQC?t=300", "https://open.spotify.com/episode/4uLU6hMCjMI75M1A2tKUQC?t=300"},
	{"Steam保留语言", "https://store.steampowered.com/app/620/Portal_2/?snr=1_5_9__205&l=schinese",
		"https://store.steampowered.com/app/620?l=schinese", "https://store.steampowered.com/app/620/Portal_2/?l=schinese"},
	{"BOOTH", "https://booth.pm/ja/items/1234567?utm_campaign=items_show&utm_medium=twitter&utm_source=x",
		"https://booth.pm/ja/items/1234567", "https://booth.pm/ja/items/1234567"},
	{"爱发电", "https://afdian.com/a/someone?utm_source=x&ref=y",
		"https://afdian.com/a/someone", "https://afdian.com/a/someone"},
	{"Bluesky只走通用表", "https://bsky.app/profile/someone.bsky.social/post/3kabcdefghijk?utm_source=x&ref_src=y",
		"https://bsky.app/profile/someone.bsky.social/post/3kabcdefghijk?ref_src=y", "https://bsky.app/profile/someone.bsky.social/post/3kabcdefghijk?ref_src=y"},
	{"Misskey只走通用表", "https://misskey.io/notes/9abcdefghi?utm_medium=x&s=1",
		"https://misskey.io/notes/9abcdefghi?s=1", "https://misskey.io/notes/9abcdefghi?s=1"},
	{"Mastodon只走通用表", "https://mastodon.social/@someone/112233445566?utm_medium=x&t=1",
		"https://mastodon.social/@someone/112233445566?t=1", "https://mastodon.social/@someone/112233445566?t=1"},
	{"小红书保留token", "https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?app_platform=ios&app_version=8.60&share_from_user_hidden=true&xsec_source=app_share&type=normal&xsec_token=ABfake%3D&author_share=1&xhsshare=WeixinSession&shareRedId=abc&apptime=1&share_id=x&appuid=u",
		"https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_source=app_share&type=normal&xsec_token=ABfake%3D", "https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_source=app_share&type=normal&xsec_token=ABfake%3D"},
	{"小黑盒", "https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id=123456&utm_source=x",
		"https://www.xiaoheihe.cn/app/bbs/link/123456", "https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id=123456"},
	{"亚马逊走通用表", "https://www.amazon.co.jp/dp/B000000000?ref_=nav_x&pd_rd_r=a&pf_rd_p=b&tag=foo-22&th=1&psc=1&linkCode=ll1",
		"", "https://www.amazon.co.jp/dp/B000000000?th=1&psc=1"},
	{"淘宝走通用表", "https://item.taobao.com/item.htm?id=123456&spm=a21.1&scm=1007.2&utparam=x&price=10",
		"", "https://item.taobao.com/item.htm?id=123456&price=10"},
	{"未识别站点保留t和片段", "https://example.com/watch?v=1&t=30&fbclid=x&utm_campaign=y&igsh=z#t=90",
		"", "https://example.com/watch?v=1&t=30#t=90"},
	{"表外参数的重复值空值次序", "https://example.com/a?z=2&a=%2F&a=&utm_source=x&b",
		"", "https://example.com/a?z=2&a=%2F&a=&b"},
}

func TestCleanTable(t *testing.T) {
	for _, c := range cleanTable {
		t.Run(c.name, func(t *testing.T) {
			l, ok := Recognize(c.in)
			if c.rec == "" {
				if ok {
					t.Errorf("Recognize 不该认识：%+v", l)
				}
			} else if !ok || l.Clean != c.rec {
				t.Errorf("Recognize.Clean = %q (%v)，要 %q", l.Clean, ok, c.rec)
			} else if again, ok := Recognize(l.Clean); !ok || again.Clean != l.Clean {
				t.Errorf("Recognize 不幂等：%q -> %q", l.Clean, again.Clean)
			}
			got, err := CleanAny(c.in)
			if err != nil || got != c.any {
				t.Errorf("CleanAny = %q %v，要 %q", got, err, c.any)
			}
			if again, err := CleanAny(got); err != nil || again != got {
				t.Errorf("CleanAny 不幂等：%q -> %q", got, again)
			}
			// 消息里出现的链接走 FindURLs，结果必须和 CleanAny 一致。
			if found := FindURLs("看 " + c.in + " 这个"); len(found) != 1 || found[0].Clean != c.any {
				t.Errorf("FindURLs.Clean = %+v，要 %q", found, c.any)
			}
		})
	}
}

// 保留表优先于任何追踪表：同一主机上，保留键不能出现在追踪表里（X 的 t 除外，它只在 X status 上生效）。
func TestLocatorNeverTracking(t *testing.T) {
	hosts := []string{"www.bilibili.com", "b23.tv", "www.youtube.com", "youtu.be", "open.spotify.com", "music.163.com", "www.douyin.com",
		"www.tiktok.com", "www.xiaohongshu.com", "www.pixiv.net", "github.com", "store.steampowered.com", "www.amazon.com", "item.taobao.com", "example.com"}
	for key := range locatorKeys {
		for _, host := range hosts {
			if isTracking(key, host, "/video/BV1xx411c7mD") {
				t.Errorf("%s 上的 %s 是定位参数，不能当追踪删掉", host, key)
			}
		}
	}
	if isTracking("t", "x.com", "/search") {
		t.Error("X 搜索页的 t 不是 status 追踪")
	}
	if !isTracking("t", "x.com", "/someone/status/1") || !isTracking("t", "t.co", "/AbC") {
		t.Error("X status 和 t.co 上的 t 应当删掉")
	}
	for key := range biliTracking {
		if _, ok := locatorKeys[key]; ok {
			t.Errorf("B 站追踪表含保留键 %s", key)
		}
	}
	for key := range commonTracking {
		if _, ok := locatorKeys[key]; ok {
			t.Errorf("通用追踪表含保留键 %s", key)
		}
	}
}

// 短链解析后的终点再清一次：b23.tv 和 t.co 的 Location 都带着分享参数。
func TestShortLinkTargetCleanedAgain(t *testing.T) {
	for _, c := range []struct{ from, location, want string }{
		{"https://b23.tv/AbCdEf", "https://www.bilibili.com/video/BV1xx411c7mD?p=2&t=33&share_source=copy_web&vd_source=a&spm_id_from=b", "https://www.bilibili.com/video/BV1xx411c7mD?p=2&t=33"},
		{"https://t.co/AbCd12", "https://x.com/someone/status/1234567890123?s=20&t=AbC", "https://x.com/someone/status/1234567890123"},
	} {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			resp := response(302, "")
			resp.Header.Set("Location", c.location)
			return resp, nil
		})
		got, err := f.Resolve(context.Background(), link(t, c.from))
		if err != nil || got.Clean != c.want {
			t.Errorf("%s -> %+v %v，要 %q", c.from, got, err, c.want)
		}
	}
}
