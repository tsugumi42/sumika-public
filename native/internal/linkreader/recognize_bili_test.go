package linkreader

import "testing"

func TestRecognizeBiliPages(t *testing.T) {
	cases := []struct{ raw, kind, id, clean string }{
		// 专栏：三种手机端、短码形态都换成 read/cv
		{"https://www.bilibili.com/read/mobile?id=900101&share_source=qq", "article", "cv900101", "https://www.bilibili.com/read/cv900101"},
		{"https://www.bilibili.com/read/mobile/900101", "article", "cv900101", "https://www.bilibili.com/read/cv900101"},
		{"https://m.bilibili.com/read/mobile/900101?x=1", "article", "cv900101", "https://www.bilibili.com/read/cv900101"},
		{"https://b23.tv/cv900101", "article", "cv900101", "https://www.bilibili.com/read/cv900101"},
		// 番剧：md、旧域名、短码；ep、ss 短码不必再走跳转
		{"https://www.bilibili.com/bangumi/media/md900102/?spm_id_from=666", "media", "md900102", "https://www.bilibili.com/bangumi/media/md900102"},
		{"https://m.bilibili.com/bangumi/media/md900102", "media", "md900102", "https://www.bilibili.com/bangumi/media/md900102"},
		{"https://bangumi.bilibili.com/anime/900102", "media", "md900102", "https://www.bilibili.com/bangumi/media/md900102"},
		{"https://b23.tv/md900102", "media", "md900102", "https://www.bilibili.com/bangumi/media/md900102"},
		{"https://b23.tv/ep900103", "episode", "ep900103", "https://www.bilibili.com/bangumi/play/ep900103"},
		{"https://b23.tv/ss900104", "season", "ss900104", "https://www.bilibili.com/bangumi/play/ss900104"},
		{"https://m.bilibili.com/bangumi/play/ep900103", "episode", "ep900103", "https://www.bilibili.com/bangumi/play/ep900103"},
		// 课程、音频
		{"https://www.bilibili.com/cheese/play/ss900105?csource=x", "course", "ss900105", "https://www.bilibili.com/cheese/play/ss900105"},
		{"https://m.bilibili.com/cheese/play/ep900106", "course", "ep900106", "https://www.bilibili.com/cheese/play/ep900106"},
		{"https://www.bilibili.com/audio/au900107", "audio", "au900107", "https://www.bilibili.com/audio/au900107"},
		// 合集和系列：缺 type 按合集；旧式路径靠路径区分
		{"https://space.bilibili.com/900108/lists/900109?type=season&sid=1", "collection", "season:900108:900109", "https://space.bilibili.com/900108/lists/900109?type=season"},
		{"https://space.bilibili.com/900108/lists/900109", "collection", "season:900108:900109", "https://space.bilibili.com/900108/lists/900109?type=season"},
		{"https://space.bilibili.com/900108/lists/900110?type=series", "collection", "series:900108:900110", "https://space.bilibili.com/900108/lists/900110?type=series"},
		{"https://space.bilibili.com/900108/channel/collectiondetail?sid=900109", "collection", "season:900108:900109", "https://space.bilibili.com/900108/lists/900109?type=season"},
		{"https://space.bilibili.com/900108/channel/seriesdetail?sid=900110", "collection", "series:900108:900110", "https://space.bilibili.com/900108/lists/900110?type=series"},
		// 活动页和播放器嵌入页只抽视频编号
		{"https://www.bilibili.com/festival/fakeact?bvid=BV1Ab2Cd3Ef4&spm_id_from=x", "video", "BV1Ab2Cd3Ef4", "https://www.bilibili.com/video/BV1Ab2Cd3Ef4"},
		{"https://player.bilibili.com/player.html?aid=900111&page=2", "video", "av900111", "https://www.bilibili.com/video/av900111?p=2"},
		{"https://player.bilibili.com/player.html?bvid=bv1Ab2Cd3Ef4&aid=1", "video", "BV1Ab2Cd3Ef4", "https://www.bilibili.com/video/BV1Ab2Cd3Ef4"},
		// 微信旧分享
		{"https://www.bilibili.com/s/video/BV1Ab2Cd3Ef4?p=2", "video", "BV1Ab2Cd3Ef4", "https://www.bilibili.com/video/BV1Ab2Cd3Ef4?p=2"},
		// 直播间的手机端和 blanc 页
		{"https://live.bilibili.com/blanc/900112?liteVersion=true", "live", "900112", "https://live.bilibili.com/900112"},
		{"https://live.bilibili.com/h5/900112", "live", "900112", "https://live.bilibili.com/900112"},
		// 个人主页：手机端和各个子页都落到名片
		{"https://m.bilibili.com/space/900113", "user", "900113", "https://space.bilibili.com/900113"},
		{"https://space.bilibili.com/900113/video?tid=0&order=click", "user", "900113", "https://space.bilibili.com/900113"},
		{"https://space.bilibili.com/900113/lists", "user", "900113", "https://space.bilibili.com/900113"},
		{"https://space.bilibili.com/900113/article", "user", "900113", "https://space.bilibili.com/900113"},
		// 小店商品：三种入口都落到同一种页面
		{"https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail&itemsId=900114&track_id=__BGMT__", "workshop", "900114", "https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail&itemsId=900114"},
		{"https://gf.bilibili.com/item/detail/900114?x=1", "workshop", "900114", "https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail&itemsId=900114"},
		{"https://show.bilibili.com/platform/malldetail.html?itemsId=900114&from=x", "workshop", "900114", "https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail&itemsId=900114"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			got, ok := Recognize(c.raw)
			want := Link{Platform: "bilibili", Kind: c.kind, ID: c.id, Clean: c.clean, Raw: c.raw}
			if !ok || got != want {
				t.Fatalf("Recognize = %#v, %v; want %#v", got, ok, want)
			}
			// 正式写法再认一遍必须原样回来（取数前会再认一遍）
			again, ok := Recognize(got.Clean)
			if !ok || again.Kind != got.Kind || again.ID != got.ID || again.Clean != got.Clean {
				t.Fatalf("Clean 再认一遍变了：%#v", again)
			}
			if Clean(c.raw) != c.clean {
				t.Fatalf("Clean = %q", Clean(c.raw))
			}
		})
	}
}

func TestRecognizeBiliRejects(t *testing.T) {
	for _, raw := range []string{
		"https://b23.tv/md900102/x",
		"https://www.bilibili.com/read/mobile", "https://www.bilibili.com/read/mobile?id=abc", "https://www.bilibili.com/read/mobile?id=1&id=2",
		"https://www.bilibili.com/bangumi/media/xx900102", "https://bangumi.bilibili.com/anime/abc", "https://bangumi.bilibili.com/",
		"https://www.bilibili.com/cheese/play/xx1", "https://www.bilibili.com/cheese/play/ss0",
		"https://www.bilibili.com/audio/am900107", "https://www.bilibili.com/audio/au",
		"https://space.bilibili.com/900108/lists/900109?type=other", "https://space.bilibili.com/900108/lists/0?type=season",
		"https://space.bilibili.com/900108/channel/collectiondetail", "https://space.bilibili.com/900108/channel/collectiondetail?sid=a",
		"https://space.bilibili.com/900108/fans/follow", "https://space.bilibili.com/900108/favlist?fid=1", "https://space.bilibili.com/900108/upower", "https://space.bilibili.com/abc",
		"https://www.bilibili.com/festival/fakeact", "https://www.bilibili.com/festival/fakeact?bvid=short",
		"https://player.bilibili.com/player.html", "https://player.bilibili.com/player.html?aid=abc", "https://player.bilibili.com/other.html?aid=1",
		"https://live.bilibili.com/blanc/abc", "https://live.bilibili.com/h5/",
		"https://gf.bilibili.com/item/detail/abc", "https://gf.bilibili.com/shop/1", "https://show.bilibili.com/platform/detail.html?id=1", "https://show.bilibili.com/platform/malldetail.html",
		"https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail", "https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail&itemsId=0",
		"https://m.bilibili.com/space/abc",
	} {
		t.Run(raw, func(t *testing.T) {
			if got, ok := Recognize(raw); ok {
				t.Fatalf("不该认出来：%#v", got)
			}
		})
	}
}

// 会员购商品仍然走会员购：只有 mall-up_itemDetail 才是小店。
func TestRecognizeMallStaysMall(t *testing.T) {
	l, ok := Recognize("https://mall.bilibili.com/neul-next/detailuniversal/detail.html?itemsId=900115&page=detailuniversal_detail")
	if !ok || l.Kind != "mall" || l.ID != "900115" {
		t.Fatalf("%#v %v", l, ok)
	}
}

// 聊天里不带 https:// 的裸写也要抓得到。
func TestExtractBiliBareHosts(t *testing.T) {
	text := "看这个 gf.bilibili.com/item/detail/900114 还有 player.bilibili.com/player.html?aid=900111 以及 bangumi.bilibili.com/anime/900102 和 show.bilibili.com/platform/malldetail.html?itemsId=900116"
	got := Extract(text)
	var kinds []string
	for _, l := range got {
		kinds = append(kinds, l.Kind+":"+l.ID)
	}
	want := []string{"workshop:900114", "video:av900111", "media:md900102", "workshop:900116"}
	if len(kinds) != len(want) {
		t.Fatalf("抓到 %v，想要 %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("抓到 %v，想要 %v", kinds, want)
		}
	}
}
