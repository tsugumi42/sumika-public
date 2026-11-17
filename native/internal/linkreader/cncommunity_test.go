package linkreader

import "testing"

// 样本 ID 一律编造且短于 7 位，避免进 git 的文件出现真实号码形状。
func TestCNRecognizeAndClean(t *testing.T) {
	for _, c := range []struct{ raw, platform, kind, id, clean string }{
		{"https://www.xiaoheihe.cn/app/topic/game/pc/90001", "xiaoheihe", "game", "90001", "https://www.xiaoheihe.cn/app/topic/game/pc/90001"},
		{"xiaoheihe.cn/app/topic/game/pc/90001/?utm_source=qq", "xiaoheihe", "game", "90001", "https://www.xiaoheihe.cn/app/topic/game/pc/90001"},
		{"https://API.XIAOHEIHE.CN/game/share_game_detail?appid=90001&x=1", "xiaoheihe", "game", "90001", "https://www.xiaoheihe.cn/app/topic/game/pc/90001"},
		{"https://www.xiaoheihe.cn/app/bbs/link/90002", "xiaoheihe", "post", "90002", "https://www.xiaoheihe.cn/app/bbs/link/90002"},
		{"https://www.xiaoheihe.cn/app/bbs/link/0A1B2C3D4E5F?share_from=x", "xiaoheihe", "post", "0A1B2C3D4E5F", "https://www.xiaoheihe.cn/app/bbs/link/0A1B2C3D4E5F"},
		{"https://www.xiaoheihe.cn/bbs/post_share?link_id=90002&share_from=x", "xiaoheihe", "post", "90002", "https://www.xiaoheihe.cn/app/bbs/link/90002"},
		{"https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?link_id=90002", "xiaoheihe", "post", "90002", "https://www.xiaoheihe.cn/app/bbs/link/90002"},
		{"https://www.xiaoheihe.cn/app/user/profile/90003/", "xiaoheihe", "user", "90003", "https://www.xiaoheihe.cn/app/user/profile/90003"},
		{"https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_token=ABfake&xsec_source=pc_feed&xhsshare=CopyLink", "xiaohongshu", "note", "6a1b2c3d4e5f6a1b2c3d4e5f", "https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_token=ABfake&xsec_source=pc_feed"},
		{"https://xiaohongshu.com/discovery/item/6A1B2C3D4E5F6A1B2C3D4E5F?xsec_token=ABfake", "xiaohongshu", "note", "6a1b2c3d4e5f6a1b2c3d4e5f", "https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f?xsec_token=ABfake"},
		{"https://www.xiaohongshu.com/user/profile/6a1b2c3d4e5f6a1b2c3d4e5f", "xiaohongshu", "user", "6a1b2c3d4e5f6a1b2c3d4e5f", "https://www.xiaohongshu.com/user/profile/6a1b2c3d4e5f6a1b2c3d4e5f"},
		{"http://xhslink.com/o/AbC123", "xiaohongshu", "short", "", "https://xhslink.com/o/AbC123"},
		{"https://xhslink.cn/o/AbC123/", "xiaohongshu", "short", "", "https://xhslink.cn/o/AbC123"},
	} {
		t.Run(c.raw, func(t *testing.T) {
			l, ok := Recognize(c.raw)
			if !ok || l.Platform != c.platform || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean {
				t.Fatalf("got %+v %v; want %s %s %s %s", l, ok, c.platform, c.kind, c.id, c.clean)
			}
			if again, ok := Recognize(l.Clean); !ok || again.Clean != l.Clean || Clean(l.Clean) != l.Clean {
				t.Fatalf("clean not idempotent: %q", l.Clean)
			}
		})
	}
	for _, raw := range []string{
		"https://xiaoheihe.com/app/topic/game/pc/90001",
		"https://xiaoheihe.cn.evil.example/app/topic/game/pc/90001",
		"https://www.xiaoheihe.cn/app/topic/game/mobile/90001",
		"https://www.xiaoheihe.cn/app/topic/game/pc/0",
		"https://www.xiaoheihe.cn/app/topic/game/pc/090001",
		"https://www.xiaoheihe.cn/app/topic/game/pc/90001/extra",
		"https://www.xiaoheihe.cn/app/bbs/link/xyz",
		"https://www.xiaoheihe.cn/app/bbs/link/0a1b2c3d4e5",
		"https://user:pass@www.xiaoheihe.cn/app/topic/game/pc/90001",
		"https://www.xiaoheihe.cn:8443/app/topic/game/pc/90001",
		"https://api.xiaoheihe.cn/game/share_game_detail?appid=90001&appid=90002",
		"https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5",
		"https://www.xiaohongshu.com/explore/6a1b2c3d4e5f6a1b2c3d4e5f/extra",
		"https://xiaohongshu.com.evil.example/explore/6a1b2c3d4e5f6a1b2c3d4e5f",
		"https://www.xiaohongshu.com/search_result/6a1b2c3d4e5f6a1b2c3d4e5f",
		"https://xhslink.co/o/AbC123",
		"https://xhslink.com/AbC123",
		"https://evilxhslink.com/o/AbC123",
		"https://www.xiaohongshu.com:8443/explore/6a1b2c3d4e5f6a1b2c3d4e5f",
	} {
		if l, ok := Recognize(raw); ok {
			t.Errorf("accepted %s: %+v", raw, l)
		}
	}
}
