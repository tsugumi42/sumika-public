package linkreader

import "testing"

// X 和 pixiv 的网址形态：每种都要认出来，并改写成同一个干净链接（缓存键因此一致）。
func TestXPixivURLForms(t *testing.T) {
	for _, c := range []struct{ raw, platform, kind, id, clean string }{
		// X：正站、手机站、嵌入修复站、无账号形态、旧式 statuses
		{"https://x.com/paper_cloud/status/1900000000000000001", "x", "post", "1900000000000000001", "https://x.com/paper_cloud/status/1900000000000000001"},
		{"https://m.twitter.com/paper_cloud/status/1900000000000000001?s=20", "x", "post", "1900000000000000001", "https://x.com/paper_cloud/status/1900000000000000001"},
		{"https://m.x.com/paper_cloud/status/1900000000000000001", "x", "post", "1900000000000000001", "https://x.com/paper_cloud/status/1900000000000000001"},
		{"https://mobile.twitter.com/paper_cloud/status/1900000000000000001", "x", "post", "1900000000000000001", "https://x.com/paper_cloud/status/1900000000000000001"},
		{"https://fixvx.com/paper_cloud/status/1900000000000000001?t=abc", "x", "post", "1900000000000000001", "https://x.com/paper_cloud/status/1900000000000000001"},
		{"https://d.fxtwitter.com/paper_cloud/status/1900000000000000001", "x", "post", "1900000000000000001", "https://x.com/paper_cloud/status/1900000000000000001"},
		{"https://fixupx.com/paper_cloud/status/1900000000000000001/photo/1", "x", "post", "1900000000000000001", "https://x.com/paper_cloud/status/1900000000000000001"},
		{"https://x.com/i/web/status/1900000000000000001", "x", "post", "1900000000000000001", "https://x.com/i/status/1900000000000000001"},
		{"https://twitter.com/i/web/status/1900000000000000001?s=20", "x", "post", "1900000000000000001", "https://x.com/i/status/1900000000000000001"},
		{"https://twitter.com/statuses/1900000000000000001", "x", "post", "1900000000000000001", "https://x.com/i/status/1900000000000000001"},
		{"https://twitter.com/paper_cloud/statuses/1900000000000000001", "x", "post", "1900000000000000001", "https://x.com/paper_cloud/status/1900000000000000001"},
		// pixiv：作品、语言前缀、旧式、手机站、嵌入修复站、图片直链、小说短链、系列页
		{"https://www.pixiv.net/artworks/900001", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://www.pixiv.net/zh_tw/artworks/900001", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://www.pixiv.net/ko/artworks/900001", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://www.pixiv.net/member_illust.php?mode=medium&illust_id=900001", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://www.pixiv.net/member_illust.php?mode=manga&illust_id=900001", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://www.pixiv.net/member_illust.php?mode=manga_big&illust_id=900001&page=2", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://touch.pixiv.net/member_illust.php?mode=medium&illust_id=900001", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://touch.pixiv.net/artworks/900001?from=qq", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://phixiv.net/artworks/900001", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://phixiv.net/en/artworks/900001/2", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://ppxiv.net/artworks/900001", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://i.pximg.net/img-original/img/2026/11/01/12/30/45/900001_p0.png", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://i.pximg.net/img-master/img/2026/11/01/12/30/45/900001_p1_master1200.jpg", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://i.pximg.net/c/600x1200_90/img-master/img/2026/11/01/12/30/45/900001_p0_master1200.jpg?x=1", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://pixiv.cat/900001.png", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://pixiv.cat/900001-2.jpg", "pixiv", "artwork", "900001", "https://www.pixiv.net/artworks/900001"},
		{"https://www.pixiv.net/novel/show.php?id=900002", "pixiv", "novel", "900002", "https://www.pixiv.net/novel/show.php?id=900002"},
		{"https://www.pixiv.net/n/900002", "pixiv", "novel", "900002", "https://www.pixiv.net/novel/show.php?id=900002"},
		{"https://www.pixiv.net/user/900005/series/900003", "pixiv", "series", "900003", "https://www.pixiv.net/user/900005/series/900003"},
		{"https://www.pixiv.net/en/user/900005/series/900003?p=2", "pixiv", "series", "900003", "https://www.pixiv.net/user/900005/series/900003?p=2"},
		{"https://www.pixiv.net/novel/series/900004", "pixiv", "novel_series", "900004", "https://www.pixiv.net/novel/series/900004"},
	} {
		l, ok := Recognize(c.raw)
		if !ok {
			t.Errorf("认不出：%s", c.raw)
			continue
		}
		if l.Platform != c.platform || l.Kind != c.kind || l.ID != c.id || l.Clean != c.clean {
			t.Errorf("%s\n  得到 %s/%s %s %s\n  想要 %s/%s %s %s", c.raw, l.Platform, l.Kind, l.ID, l.Clean, c.platform, c.kind, c.id, c.clean)
		}
		// 干净链接再识别一遍结果不变（缓存键稳定）
		again, ok := Recognize(l.Clean)
		if !ok || again.Clean != l.Clean || again.ID != l.ID || again.Kind != l.Kind {
			t.Errorf("干净链接不稳定：%s -> %s -> %+v", c.raw, l.Clean, again)
		}
	}
}

func TestXPixivURLFormsRejected(t *testing.T) {
	for _, raw := range []string{
		"https://x.com/i/web/status/",
		"https://x.com/i/web/status/abc",
		"https://x.com/i/statuses/",
		"https://twitter.com/statuses/",
		"https://i.pximg.net/user-profile/img/2026/11/01/12/30/45/900001_170.png",
		"https://i.pximg.net/img-original/img/2026/11/01/900001_p0.png",
		"https://pixiv.cat/",
		"https://pixiv.cat/abc.png",
		"https://phixiv.net/",
		"https://touch.pixiv.net/",
		"https://www.pixiv.net/n/0",
		"https://www.pixiv.net/n/abc",
		"https://www.pixiv.net/member_illust.php?mode=big&illust_id=900001",
		"https://www.pixiv.net/member_illust.php?mode=manga",
		"https://www.pixiv.net/artworks/123456789012345678901234567890",
		"https://www.pixiv.net/user/abc/series/900003",
		"https://www.pixiv.net/user/900005/series/",
		"https://www.pixiv.net/novel/series/0",
		"https://www.pixiv.net/fr/artworks/900001",
	} {
		if l, ok := Recognize(raw); ok {
			t.Errorf("不该认出：%s -> %+v", raw, l)
		}
	}
}
