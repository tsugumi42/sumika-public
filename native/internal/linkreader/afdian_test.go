package linkreader

import (
	"strings"
	"testing"
)

func TestAfdianRecognizeAndClean(t *testing.T) {
	const post = "90000000000000000000000000000001"
	const album = "90000000000000000000000000000006"
	const albumPost = "90000000000000000000000000000005"
	for _, c := range []struct{ raw, kind, id, clean string }{
		{"https://afdian.com/a/example_creator", "creator", "example_creator", "https://afdian.com/a/example_creator"},
		{"https://AFDIAN.com/a/example_one?utm_source=qq&x=1", "creator", "example_one", "https://afdian.com/a/example_one"},
		{"https://www.afdian.com/a/example_two/", "creator", "example_two", "https://afdian.com/a/example_two"},
		{"https://afdian.com/p/" + post + "?utm_source=qq&share=1", "post", post, "https://afdian.com/p/" + post},
		{"https://ifdian.net/p/FEEDFACE000000000000000000000001?x=1", "post", "feedface000000000000000000000001", "https://afdian.com/p/feedface000000000000000000000001"},
		{"https://afdian.net/p/" + post + "#comment", "post", post, "https://afdian.com/p/" + post},
		{"afdian.com/p/" + post, "post", post, "https://afdian.com/p/" + post},
		{"https://afdian.com/album/" + album, "album", album, "https://afdian.com/album/" + album},
		{"https://afdian.com/album/" + album + "/" + albumPost + "?x=1", "album_post", album + "/" + albumPost, "https://afdian.com/album/" + album + "/" + albumPost},
		{"https://afdian.com/item/90000000000000000000000000000007?from=share", "item", "90000000000000000000000000000007", "https://afdian.com/item/90000000000000000000000000000007"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if !ok || l.Platform != "afdian" || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean {
				t.Fatalf("got %+v %v; want %s %s", l, ok, c.kind, c.clean)
			}
			if Clean(l.Clean) != c.clean {
				t.Fatalf("clean is not idempotent: %s", Clean(l.Clean))
			}
			// 文本里只抽取带协议的完整链接；裸域名不会被当成正文中的链接。
			if strings.HasPrefix(c.raw, "https://") {
				if got := Extract("分享（" + c.raw + "）"); len(got) != 1 || got[0].Clean != c.clean {
					t.Fatalf("text extraction: %+v", got)
				}
			}
		})
	}
	for _, raw := range []string{
		"https://afdian.com.evil.example/p/" + post,
		"https://evil-afdian.com/p/" + post,
		"https://afdian.com./p/" + post,
		"https://user:pass@afdian.com/p/" + post,
		"https://afdian.com:8443/p/" + post,
		"https://afdian.com/user/example_creator",
		"https://afdian.com/p/feedface0000000000000000000000",
		"https://afdian.com/p/" + post + "0",
		"https://afdian.com/p/not-an-id-not-an-id-not-an-id",
		"https://afdian.com/a/bad.slug",
		"https://afdian.com/a/中文",
		"https://afdian.com/a/x/y",
		"https://afdian.com/a/" + strings.Repeat("a", 65),
		"https://afdian.com/a/slug%2F",
		"https://afdian.com/album/" + album + "/" + albumPost + "/extra",
		"https://afdian.com/album/" + album + "/not-hex",
		"https://afdian.com/item/short",
		"https://afdian.com/",
		"https://afdian.com/a/",
	} {
		if l, ok := Recognize(raw); ok {
			t.Fatalf("accepted %q: %+v", raw, l)
		}
	}
}

// 爱发电的全部域名（含旧域和媒体 CDN）都必须走国内直连；删掉名单里的任何一条，这里和路由测试都会红。
func TestAfdianDomesticRoute(t *testing.T) {
	for _, host := range []string{"afdian.com", "www.afdian.com", "afdian.net", "ifdian.net", "afdiancdn.com", "pic1.afdiancdn.com", "vod.afdiancdn.com"} {
		if !domesticHost(host) {
			t.Fatalf("%s 应走国内直连", host)
		}
	}
}
