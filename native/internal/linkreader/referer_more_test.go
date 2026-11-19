package linkreader

import (
	"net/http"
	"testing"
)

// 新接的九个站都不能带 request() 默认写的 B 站 Referer；不需要来源的几家要整个删掉。
// 合并几条站点分支时，setMoreHeaders 里出现过只剩 case 行、没有 body 的分支（Go 的空 case 不会落到下一条），
// Bluesky、Misskey 的请求因此又带上了 B 站 Referer，各站自己的测试没守住这一条（2026-11-07）。
func TestMoreSitesRefererPolicy(t *testing.T) {
	const bili = "https://www.bilibili.com/"
	for _, c := range []struct {
		url, platform, want string // want 为空表示不能带 Referer
	}{
		{"https://public.api.bsky.app/xrpc/app.bsky.feed.getPostThread", "bluesky", ""},
		{"https://misskey.io/api/notes/show", "misskey", ""},
		{"https://mastodon.social/api/v1/statuses/1", "mastodon", ""},
		{"https://store.steampowered.com/api/appdetails?appids=1", "steam", ""},
		{"https://open.spotify.com/embed/track/x", "spotify", ""},
		{"https://www.xiaohongshu.com/explore/x", "xiaohongshu", ""},
		{"https://booth.pm/ja/items/1.json", "booth", "https://booth.pm/"},
		{"https://afdian.com/api/post/get-detail", "afdian", "https://afdian.com/"},
		{"https://www.tiktok.com/@a/video/1", "tiktok", "https://www.tiktok.com/"},
		{"https://www.xiaoheihe.cn/app/topic/game/pc/1", "xiaoheihe", "https://www.xiaoheihe.cn/"},
	} {
		if got := requestPlatform(c.url); got != c.platform {
			t.Errorf("%s 认成了 %q，期望 %q", c.url, got, c.platform)
			continue
		}
		req, _ := http.NewRequest(http.MethodGet, c.url, nil)
		req.Header.Set("Referer", bili)
		setMoreHeaders(req, c.platform)
		if got := req.Header.Get("Referer"); got != c.want {
			t.Errorf("%s 的 Referer 是 %q，期望 %q", c.platform, got, c.want)
		}
	}
}
