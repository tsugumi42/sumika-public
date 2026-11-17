package linktemplate

import (
	"strings"
	"testing"
)

// 每个站点的内置默认模板、预设都必须能解析，并能用示例数据渲染出非空的结果
func TestEveryBuiltinTemplateParsesAndRenders(t *testing.T) {
	for _, s := range Sites() {
		for _, forward := range []bool{false, true} {
			if forward && !s.HasForward() {
				continue
			}
			tpl, err := Parse(s.Source(forward), s.Set)
			if err != nil {
				t.Fatalf("%s forward=%v：%v", s.Key, forward, err)
			}
			if got := Describe(tpl.Render(sampleEnv{s.Set}), nil); strings.TrimSpace(got) == "" {
				t.Fatalf("%s forward=%v 渲染为空", s.Key, forward)
			}
		}
		for _, p := range s.Presets {
			if _, err := s.Parse("@" + p.Name); err != nil {
				t.Fatalf("%s 预设 %s：%v", s.Key, p.Name, err)
			}
		}
		if _, err := s.Parse("@不存在"); err == nil {
			t.Fatalf("%s：不存在的预设应当报错", s.Key)
		}
	}
}

func TestSiteLookupAndKeys(t *testing.T) {
	if s, fwd, ok := SiteByKey("x-forward"); !ok || !fwd || s.Key != "x" {
		t.Fatal("x-forward")
	}
	if _, _, ok := SiteByKey("github-forward"); ok {
		t.Fatal("github 没有转发形态")
	}
	if _, _, ok := SiteByKey("nowhere"); ok {
		t.Fatal("不认识的站点")
	}
	for platform, want := range map[string]string{"bilibili/video": "bilibili-video", "bilibili/opus": "bilibili-dynamic", "x/article": "x", "netease/song": "netease", "spotify/track": "spotify", "spotify/show": "default", "xiaoheihe/post": "default", "misskey/note": "misskey", "mastodon/status": "misskey", "bluesky/profile": "bluesky", "steam/sharedfile": "steam", "afdian/album_post": "afdian", "tiktok/user": "tiktok", "booth/item": "booth", "xiaoheihe/game": "xiaoheihe"} {
		p, k, _ := strings.Cut(platform, "/")
		if got := SiteKey(p, k); got != want {
			t.Fatalf("%s → %s，应为 %s", platform, got, want)
		}
	}
}

// 预览用示例数据渲染：写法和配置一致，错误原样返回
func TestSitePreview(t *testing.T) {
	site, _, _ := SiteByKey("bilibili-video")
	got, err := site.Preview("@封面文字")
	if err != nil || !strings.Contains(got, "[封面]\n纸船试航\nP2/3：折纸步骤\nP1 开箱（02:15）\nP2 折纸步骤（08:40）\n纸灯实验室 | 共 3P | 总长 01:02:15\n播放 1.08亿 | 点赞 297.5万\n11-07 23:28\nhttps://example.invalid/item/1") {
		t.Fatalf("预览：%v\n%s", err, got)
	}
	// 数据字段还在：显式写上就有（默认模板不写）
	if got, err = site.Preview("$标题\n播放 $播放｜点赞 $点赞\n$热评"); err != nil || !strings.Contains(got, "播放 1.08亿｜点赞 297.5万\n热评：") {
		t.Fatalf("显式模板的预览：%v\n%s", err, got)
	}
	if _, err = site.Preview("$不存在"); err == nil {
		t.Fatal("非法模板的预览应当报错")
	}
}

// 规格-链接发送改版 B1：每个站点的内置默认模板（直发和转发）用样张渲染，每段文字里都没有空行。
// 看渲染片段而不是 Preview 的文字：Preview 在槽位两边加的换行是给人看的，不会发出去。
func TestTemplatesHaveNoBlankLines(t *testing.T) {
	for _, s := range Sites() {
		for _, forward := range []bool{false, true} {
			if forward && !s.HasForward() {
				continue
			}
			tpl, err := s.Parse(s.Source(forward))
			if err != nil {
				t.Fatalf("%s：%v", s.Key, err)
			}
			for _, p := range tpl.Render(sampleEnv{s.Set}) {
				if p.Kind == PieceText && strings.Contains(p.Text, "\n\n") {
					t.Errorf("%s（转发 %v）的内置模板有空行：%q", s.Key, forward, p.Text)
				}
			}
		}
	}
}

// 规格-链接发送改版 C1 的占位：x 站点登记了 @推特卡片 预设（卡片图加干净链接），默认仍是文字版。
func TestTweetCardPresetRegistered(t *testing.T) {
	site, _, _ := SiteByKey("x")
	src, err := site.Resolve("@推特卡片")
	if err != nil || src != "$卡片$链接" {
		t.Fatalf("@推特卡片：%q %v", src, err)
	}
	if strings.Contains(site.Source(false), "$卡片") {
		t.Fatal("x 的默认不该是卡片")
	}
}

// 用户 2026-11-12：非推文站点的内置默认不写平台名（$站点）、不写简介（$简介 字段还在，自定义模板写上就有）。
// 抖音视频、抖音图集、TikTok 的 $简介 是作品文案，当标题用，保留；default 是没有发送格式时的兜底，不发进群。
func TestBuiltinTemplatesNoSiteNameNoDesc(t *testing.T) {
	titleLike := map[string]bool{"douyin-video": true, "douyin-note": true, "tiktok": true, "default": true}
	for _, s := range Sites() {
		for _, forward := range []bool{false, true} {
			if forward && !s.HasForward() {
				continue
			}
			src := s.Source(forward)
			if strings.Contains(src, "$站点") {
				t.Errorf("%s（转发 %v）的内置模板写了平台名：%q", s.Key, forward, src)
			}
			if !titleLike[s.Key] && strings.Contains(src, "$简介") {
				t.Errorf("%s（转发 %v）的内置模板还带简介：%q", s.Key, forward, src)
			}
		}
		if _, ok := s.Set.Lookup("site"); !ok {
			t.Errorf("%s 的 $站点 字段没了", s.Key)
		}
	}
	for _, key := range []string{"bilibili-live", "bilibili-bangumi", "bilibili-audio", "bilibili-collection", "bilibili-course", "bilibili-workshop", "pixiv-novel", "pixiv-series", "github", "xiaoheihe", "booth", "steam", "afdian"} {
		s, _, _ := SiteByKey(key)
		if _, ok := s.Set.Lookup("desc"); !ok {
			t.Errorf("%s 的 $简介 字段没了", key)
		}
	}
}

// 平台本身以 @ 显示账号的站点，默认模板写 @$账号（$账号 的值不带 @）；写了 $账号 就必须紧跟在 @ 后面。
func TestDefaultTemplatesWriteHandleWithAt(t *testing.T) {
	withHandle := map[string]bool{"x": true, "bluesky": true, "misskey": true, "telegram": true}
	for _, s := range Sites() {
		for _, forward := range []bool{false, true} {
			if forward && !s.HasForward() {
				continue
			}
			src := s.Source(forward)
			if withHandle[s.Key] && !forward && !strings.Contains(src, "@$账号") {
				t.Errorf("%s 的默认模板没有写 @$账号：%q", s.Key, src)
			}
			if strings.Count(src, "$账号") != strings.Count(src, "@$账号") {
				t.Errorf("%s forward=%v 有不带 @ 的 $账号：%q", s.Key, forward, src)
			}
		}
	}
}
