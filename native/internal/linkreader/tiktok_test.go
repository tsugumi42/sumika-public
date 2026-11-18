package linkreader

import (
	"strings"
	"testing"
)

func TestTikTokRecognize(t *testing.T) {
	const video = "7000000000000000001"
	for _, c := range []struct{ raw, kind, id, clean string }{
		{"https://www.tiktok.com/@example.user/video/" + video, "video", video, "https://www.tiktok.com/@example.user/video/" + video},
		{"https://www.tiktok.com/@Example.User/video/" + video + "/?is_copy_url=1&is_from_webapp=v1&x=keep", "video", video, "https://www.tiktok.com/@example.user/video/" + video + "?x=keep"},
		{"https://m.tiktok.com/@example.user/photo/7000000000000000002", "video", "7000000000000000002", "https://www.tiktok.com/@example.user/video/7000000000000000002"},
		{"https://tiktok.com/@example.user/video/" + video, "video", video, "https://www.tiktok.com/@example.user/video/" + video},
		{"HTTPS://WWW.TIKTOK.COM/@example.user/video/" + video, "video", video, "https://www.tiktok.com/@example.user/video/" + video},
		{"https://www.tiktok.com/@example.user", "user", "example.user", "https://www.tiktok.com/@example.user"},
		{"https://www.tiktok.com/@Example.User/?lang=en&x=keep", "user", "example.user", "https://www.tiktok.com/@example.user?lang=en&x=keep"},
		{"https://vm.tiktok.com/ZMAAAAAAA/", "short", "", "https://vm.tiktok.com/ZMAAAAAAA/"},
		{"https://vt.tiktok.com/ZSAAAAAAA", "short", "", "https://vt.tiktok.com/ZSAAAAAAA"},
		{"https://www.tiktok.com/t/ZTAAAAAAA/", "short", "", "https://www.tiktok.com/t/ZTAAAAAAA/"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if !ok || l.Platform != "tiktok" || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean {
				t.Fatalf("got %+v %v; want kind=%s id=%s clean=%s", l, ok, c.kind, c.id, c.clean)
			}
			if again := Clean(l.Clean); again != c.clean {
				t.Fatalf("Clean not idempotent: %s -> %s", c.clean, again)
			}
		})
	}
}

func TestTikTokRejects(t *testing.T) {
	long := "https://www.tiktok.com/@" + strings.Repeat("a", 65) + "/video/7000000000000000001"
	for _, raw := range []string{
		// 非法 ID 和多余段
		"https://www.tiktok.com/@example.user/video/", "https://www.tiktok.com/@example.user/video/abc", "https://www.tiktok.com/@example.user/video/0123",
		"https://www.tiktok.com/@example.user/video/7000000000000000001/extra", "https://www.tiktok.com/@a/b/video/7000000000000000001",
		"https://www.tiktok.com/video/7000000000000000001", "https://www.tiktok.com/@", "https://www.tiktok.com/@example!/video/7000000000000000001",
		long,
		// 不支持的页面类型和语言前缀（未见实测样本）
		"https://www.tiktok.com/@example.user/live/7000000000000000001", "https://www.tiktok.com/@example.user/music/7000000000000000001",
		"https://www.tiktok.com/en/@example.user/video/7000000000000000001", "https://www.tiktok.com/tag/fyp", "https://www.tiktok.com/@example.user/liked",
		// 短链形状
		"https://vm.tiktok.com/", "https://vm.tiktok.com/abc/def", "https://www.tiktok.com/t/",
		// look-alike 主机
		"https://tiktok.com.evil.example/@example.user/video/7000000000000000001", "https://eviltiktok.com/@example.user/video/7000000000000000001",
		"https://tiktok.co/@example.user/video/7000000000000000001", "https://www.tiktok.com.evil.example/@example.user", "https://vm.tiktok.com.evil.example/ZMAAAAAAA/",
		// 凭据、非默认端口、非 http(s)
		"https://user:pass@www.tiktok.com/@example.user/video/7000000000000000001", "https://www.tiktok.com:8443/@example.user/video/7000000000000000001",
		"ftp://www.tiktok.com/@example.user/video/7000000000000000001",
	} {
		if l, ok := Recognize(raw); ok {
			t.Fatalf("accepted %q: %+v", raw, l)
		}
	}
}

func TestTikTokExtractFromText(t *testing.T) {
	got := Extract("看这个（https://www.tiktok.com/@example.user/video/7000000000000000001?is_copy_url=1）谢谢")
	if len(got) != 1 || got[0].Platform != "tiktok" || got[0].Clean != "https://www.tiktok.com/@example.user/video/7000000000000000001" {
		t.Fatalf("extract: %+v", got)
	}
	if got := Extract("https://tiktok.com.evil.example/@example.user/video/7000000000000000001"); len(got) != 0 {
		t.Fatalf("look-alike extracted: %+v", got)
	}
}
