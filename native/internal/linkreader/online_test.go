//go:build online

// 在线测试组：只放真实公开链接，真去网站上取一次，默认不跑。
// 平时的解析测试用 testdata 下编造的夹具；这里断言作者、正文、媒体数这类稳定特征，不写原文。
package linkreader

import (
	"context"
	"strings"
	"testing"
)

func onlineFetcher(t *testing.T) *Fetcher {
	t.Helper()
	f, err := NewFetcher(FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func onlineFetch(t *testing.T, raw string) Result {
	t.Helper()
	l, ok := Recognize(raw)
	if !ok {
		t.Fatalf("认不出链接：%s", raw)
	}
	r, err := onlineFetcher(t).Fetch(context.Background(), l)
	if err != nil {
		t.Fatalf("%s：%v", raw, err)
	}
	return r
}

func wantOnlineAuthorAndTitle(t *testing.T, r Result, titleWord string) {
	t.Helper()
	if r.Author.Name.Value == "" || r.Title.Value == "" {
		t.Fatalf("作者或标题为空：%+v %+v", r.Author.Name, r.Title)
	}
	if titleWord != "" && !strings.Contains(r.Title.Value, titleWord) {
		t.Fatalf("标题不含 %q：%q", titleWord, r.Title.Value)
	}
}

func TestOnlineXPost(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://x.com/xiaomovps/status/2097987217302167636")
	if r.Author.Name.Value == "" || r.Author.Account.Value == "" {
		t.Fatalf("作者为空：%+v", r.Author)
	}
	if r.Text.Value == "" && r.Article.Value == "" {
		t.Fatalf("正文和长文都为空：%+v", r.Text)
	}
}

func TestOnlineBiliVideo(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://www.bilibili.com/video/av80433022")
	if r.Author.Name.Value == "" || r.Title.Value == "" || len(r.Media.Value) == 0 {
		t.Fatalf("视频字段不全：%+v media=%d", r.Title, len(r.Media.Value))
	}
	if r.SelectedPart.Value.CID == "" {
		t.Fatal("没有选中分 P")
	}
}

func TestOnlineBiliOpus(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://www.bilibili.com/opus/1266405475573825555")
	if r.Author.Name.Value == "" || r.Text.Value == "" || len(r.Blocks) == 0 {
		t.Fatalf("图文动态字段不全：%+v blocks=%d", r.Text, len(r.Blocks))
	}
}

func TestOnlineAfdianPost(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://afdian.com/p/018ca894ba2511eabed452540025c377")
	wantOnlineAuthorAndTitle(t, r, "")
	if r.Text.Value == "" && r.Media.State != "available" {
		t.Fatalf("正文和图片都为空：%+v %+v", r.Text, r.Media)
	}
}

func TestOnlineYouTube(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://youtu.be/jNQXAC9IVRw")
	wantOnlineAuthorAndTitle(t, r, "zoo")
	if r.Media.State != "available" || len(r.Media.Value) == 0 {
		t.Fatalf("没有封面：%+v", r.Media)
	}
}

func TestOnlineNeteaseSong(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://music.163.com/#/song?id=186016")
	wantOnlineAuthorAndTitle(t, r, "")
	if r.Media.State != "available" || len(r.Media.Value) == 0 {
		t.Fatalf("没有封面：%+v", r.Media)
	}
}

func TestOnlineSpotifyTrack(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://open.spotify.com/track/0VjIjW4GlUZAMYd2vXMi3b")
	wantOnlineAuthorAndTitle(t, r, "")
	if r.Media.State != "available" || len(r.Media.Value) == 0 {
		t.Fatalf("没有封面：%+v", r.Media)
	}
}

func TestOnlineSteamApp(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://store.steampowered.com/app/570/")
	wantOnlineAuthorAndTitle(t, r, "Dota")
	if r.Media.State != "available" || len(r.Media.Value) == 0 {
		t.Fatalf("没有头图：%+v", r.Media)
	}
}

func TestOnlineBoothItem(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://k-s.booth.pm/items/3054511")
	wantOnlineAuthorAndTitle(t, r, "")
	if r.Text.Value == "" || r.Media.State != "available" || len(r.Media.Value) == 0 {
		t.Fatalf("商品字段不全：%+v %+v", r.Text, r.Media)
	}
}

func TestOnlineMastodonStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("在线组默认不跑")
	}
	r := onlineFetch(t, "https://mastodon.social/@Gargron/117564197289781943")
	if r.Author.Account.Value == "" || r.Text.Value == "" {
		t.Fatalf("作者或正文为空：%+v %+v", r.Author, r.Text)
	}
	if len(r.Media.Value) == 0 {
		t.Fatal("没有图片")
	}
}

// 以下平台原本的夹具就是编造的，没有可沿用的真实链接；需要登录或被平台风控的也在这里说明。
func TestOnlineUnavailablePlatforms(t *testing.T) {
	for _, c := range []struct{ name, why string }{
		{"tiktok", "页面需要登录，CI 上常被风控"},
		{"pixiv", "作品页需要登录，未登录只回空壳"},
		{"douyin", "原夹具是编造的，没有真实链接可沿用"},
		{"heybox", "原夹具是编造的，没有真实链接可沿用"},
		{"github", "原夹具是编造的，没有真实链接可沿用"},
		{"bluesky", "原夹具是编造的，没有真实链接可沿用"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip(c.why)
		})
	}
}
