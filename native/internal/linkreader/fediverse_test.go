package linkreader

import (
	"strings"
	"testing"
)

func TestFediverseRecognizeAndClean(t *testing.T) {
	for _, c := range []struct{ raw, platform, kind, id, clean string }{
		{"https://misskey.io/notes/fakenote00000001", "misskey", "note", "misskey.io/fakenote00000001", "https://misskey.io/notes/fakenote00000001"},
		{"misskey.io/notes/fakenote00000001?utm_source=qq&x=keep", "misskey", "note", "misskey.io/fakenote00000001", "https://misskey.io/notes/fakenote00000001?x=keep"},
		{"https://MISSKEY.IO/notes/fakenote00000001/", "misskey", "note", "misskey.io/fakenote00000001", "https://misskey.io/notes/fakenote00000001"},
		{"https://misskey.io/@example_user", "misskey", "user", "misskey.io/example_user", "https://misskey.io/@example_user"},
		{"https://misskey.io/@example_hako/?fbclid=x", "misskey", "user", "misskey.io/example_hako", "https://misskey.io/@example_hako"},
		{"https://mastodon.social/@example_user/9000000000000000001", "mastodon", "status", "mastodon.social/9000000000000000001", "https://mastodon.social/@example_user/9000000000000000001"},
		{"https://mastodon.social/web/@example_user/9000000000000000001", "mastodon", "status", "mastodon.social/9000000000000000001", "https://mastodon.social/@example_user/9000000000000000001"},
		{"https://mastodon.social/users/example_user/statuses/9000000000000000001/", "mastodon", "status", "mastodon.social/9000000000000000001", "https://mastodon.social/@example_user/9000000000000000001"},
		{"https://mastodon.social/@example_user/9000000000000000001?utm_campaign=x&lang=zh", "mastodon", "status", "mastodon.social/9000000000000000001", "https://mastodon.social/@example_user/9000000000000000001?lang=zh"},
		{"https://MASTODON.social:443/@example_user", "mastodon", "account", "mastodon.social/example_user", "https://mastodon.social/@example_user"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if !ok || l.Platform != c.platform || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean || Clean(l.Clean) != c.clean {
				t.Fatalf("got %+v %v; want %s %s %s", l, ok, c.platform, c.kind, c.clean)
			}
			// misskey.io、mastodon.social 不在聊天文本的通用起点表（wideHosts）里，裸域名提取不到；这里只检查带协议的形式。
			if strings.HasPrefix(c.raw, "https://") {
				if got := Extract("分享（" + c.raw + "）"); len(got) != 1 || got[0].Clean != c.clean {
					t.Fatalf("text extraction: %+v", got)
				}
			}
		})
	}
	for _, raw := range []string{
		// 形状不对：ID 大小写、长度、多余路径、未支持的页面。
		"https://misskey.io/notes/FAKENOTE00000001",
		"https://misskey.io/notes/abc",
		"https://misskey.io/notes/fakenote00000001/extra",
		"https://misskey.io/users/9abc",
		"https://misskey.io/channels/9b8huyo5e3",
		"https://misskey.io/@",
		"https://misskey.io/@a-b",
		"https://misskey.io/@example_user@other.example",
		// 形似域名：不是精确的实例主机。
		"https://www.misskey.io/notes/fakenote00000001",
		"https://misskey.io.evil.example/notes/fakenote00000001",
		"https://evilmisskey.io/notes/fakenote00000001",
		"https://misskey.design/notes/fakenote00000001",
		"https://mastodon.social.evil.example/@example_user/9000000000000000001",
		"https://mastodon.online/@example_user/9000000000000000001",
		"https://mastodon.social.example/@example_user",
		// Mastodon 的非法形状。
		"https://mastodon.social/@example_user/0",
		"https://mastodon.social/@example_user/12a",
		"https://mastodon.social/@example_user@other.example/9000000000000000001",
		"https://mastodon.social/@example_user/9000000000000000001/extra",
		"https://mastodon.social/tags/video",
		"https://mastodon.social/public",
		// 编码的斜杠不是路径分隔符：解码后会变成另一个目标，直接拒绝。
		"https://mastodon.social/@example_user%2F9000000000000000001",
		"https://misskey.io/notes%2Ffakenote00000001",
		// 凭据和非默认端口。
		"https://user:pass@misskey.io/notes/fakenote00000001",
		"https://misskey.io:8443/notes/fakenote00000001",
		"https://mastodon.social:8080/@example_user",
	} {
		if l, ok := Recognize(raw); ok {
			t.Fatalf("accepted %s: %+v", raw, l)
		}
	}
}
