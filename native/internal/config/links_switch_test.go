package config

import (
	"strings"
	"testing"
)

// 网站和页面类型开关：默认全开，关掉的站和类型不展开；别名对到清单里的同一类型。
func TestKindEnabledTable(t *testing.T) {
	off, on := false, true
	mk := func(p map[string]LinkPlatform) LinkExpand { return LinkExpand{Platforms: p} }
	cases := []struct {
		name           string
		e              LinkExpand
		platform, kind string
		want           bool
	}{
		{"没写任何开关：全开", mk(nil), "bilibili", "video", true},
		{"整站关", mk(map[string]LinkPlatform{"bilibili": {Enabled: &off}}), "bilibili", "video", false},
		{"整站关不影响别的站", mk(map[string]LinkPlatform{"bilibili": {Enabled: &off}}), "x", "post", true},
		{"整站显式开", mk(map[string]LinkPlatform{"bilibili": {Enabled: &on}}), "bilibili", "video", true},
		{"关一种类型", mk(map[string]LinkPlatform{"bilibili": {Kinds: map[string]bool{"live": false}}}), "bilibili", "live", false},
		{"关一种类型不影响同站别的类型", mk(map[string]LinkPlatform{"bilibili": {Kinds: map[string]bool{"live": false}}}), "bilibili", "video", true},
		{"别名按主类型：关动态也关 opus", mk(map[string]LinkPlatform{"bilibili": {Kinds: map[string]bool{"dynamic": false}}}), "bilibili", "opus", false},
		{"类型显式开", mk(map[string]LinkPlatform{"bilibili": {Kinds: map[string]bool{"live": true}}}), "bilibili", "live", true},
		{"整站关压过类型开", mk(map[string]LinkPlatform{"bilibili": {Enabled: &off, Kinds: map[string]bool{"video": true}}}), "bilibili", "video", false},
		{"清单里没有的类型不由开关管", mk(nil), "bilibili", "short", true},
		{"清单里没有的网站：开关没写就开", mk(nil), "nowhere", "x", true},
	}
	for _, c := range cases {
		if got := c.e.KindEnabled(c.platform, c.kind); got != c.want {
			t.Errorf("%s：%s/%s 得 %v，应为 %v", c.name, c.platform, c.kind, got, c.want)
		}
	}
}

func TestPlatformSwitchSavedAndValidated(t *testing.T) {
	raw := []byte("linkReader:\n  expand:\n    mode: whitelist\n    conversations: []\n")
	out, c, err := PatchLink(raw, "sumika.yml", LinkChange{Key: LinkPlatformEnabled, Site: "bilibili", Bool: false})
	if err != nil {
		t.Fatal(err)
	}
	out, c, err = PatchLink(out, "sumika.yml", LinkChange{Key: LinkKindEnabled, Site: "bilibili", Text: "live", Bool: false})
	if err != nil {
		t.Fatal(err)
	}
	e := c.LinkReader.Expand
	if e.PlatformEnabled("bilibili") || e.KindEnabled("bilibili", "video") || !e.KindEnabled("x", "post") {
		t.Fatalf("整站应关：%+v\n%s", e.Platforms, out)
	}
	_, c, err = PatchLink(out, "sumika.yml", LinkChange{Key: LinkPlatformEnabled, Site: "bilibili", Bool: true})
	if err != nil || !c.LinkReader.Expand.KindEnabled("bilibili", "video") || c.LinkReader.Expand.KindEnabled("bilibili", "live") {
		t.Fatal("重新打开网站后，单独关掉的类型应仍然关着", err)
	}
	for _, bad := range []LinkChange{
		{Key: LinkPlatformEnabled, Site: "nowhere", Bool: true},
		{Key: LinkKindEnabled, Site: "bilibili", Text: "nowhere", Bool: true},
		{Key: LinkKindEnabled, Site: "nowhere", Text: "video", Bool: true},
	} {
		if _, _, err := PatchLink(raw, "sumika.yml", bad); err == nil {
			t.Errorf("%+v 应被拒绝", bad)
		}
	}
	// 手写进配置的错键由校验指出来，不是静默忽略
	bad := []byte("linkReader:\n  expand:\n    platforms:\n      nowhere: {enabled: false}\n      bilibili: {kinds: {nowhere: false}}\n")
	if _, _, err := PatchLink(bad, "sumika.yml", LinkChange{Key: LinkEnabled, Bool: true}); err == nil || !strings.Contains(err.Error(), "platforms.nowhere") || !strings.Contains(err.Error(), "kinds.nowhere") {
		t.Fatalf("应指出两个错键：%v", err)
	}
}

// 选样式一次写两项：模板预设和合并转发模式；读回来是同一种样式，非法的样式和站点被拒
func TestSiteStyleWritesTemplateAndForward(t *testing.T) {
	raw := []byte("linkReader:\n  expand:\n    mode: whitelist\n    conversations: []\n")
	out, c, err := PatchLink(raw, "sumika.yml", LinkChange{Key: LinkSiteStyle, Site: "x", Text: "mixed"})
	if err != nil {
		t.Fatal(err)
	}
	e := c.LinkReader.Expand
	if e.Sites["x"].Forward != "always" {
		t.Fatalf("混合应写 forward: always：%+v", e.Sites)
	}
	_, c, err = PatchLink(out, "sumika.yml", LinkChange{Key: LinkSiteStyle, Site: "bilibili-video", Text: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.LinkReader.Expand.Templates["bilibili-video"]; got != "@图文" {
		t.Fatalf("B 站视频图文应写 @图文：%q", got)
	}
	// 没有合并转发形态的站点，只写模板，不写 forward
	_, c, err = PatchLink(raw, "sumika.yml", LinkChange{Key: LinkSiteStyle, Site: "github", Text: "card"})
	if err != nil || c.LinkReader.Expand.Templates["github"] != "@卡片" {
		t.Fatalf("github 卡片：%v", err)
	}
	if _, ok := c.LinkReader.Expand.Sites["github"]; ok {
		t.Fatal("github 没有转发形态，不该写 forward")
	}
	for _, bad := range []LinkChange{
		{Key: LinkSiteStyle, Site: "x", Text: "nowhere"},
		{Key: LinkSiteStyle, Site: "nowhere", Text: "card"},
		{Key: LinkSiteStyle, Site: "x-forward", Text: "card"},
	} {
		if _, _, err := PatchLink(raw, "sumika.yml", bad); err == nil {
			t.Errorf("%+v 应被拒绝", bad)
		}
	}
}

// 网站的发图方式（ADR-333）：没写和不认识的都是 auto，写进文件能读回来，不认识的取值和网站被拒绝；手写错值时整份配置不通过
func TestPlatformImageModeSavedAndValidated(t *testing.T) {
	if (LinkExpand{}).ImageModeOf("x") != ImageModeAuto {
		t.Fatal("没写应是 auto")
	}
	raw := []byte("linkReader:\n  expand:\n    mode: whitelist\n    conversations: []\n")
	out, c, err := PatchLink(raw, "sumika.yml", LinkChange{Key: LinkPlatformImage, Site: "x", Text: ImageModeRotate})
	if err != nil {
		t.Fatal(err)
	}
	e := c.LinkReader.Expand
	if e.ImageModeOf("x") != ImageModeRotate || e.ImageModeOf("bilibili") != ImageModeAuto {
		t.Fatalf("X 应是 rotate、别的站不变：%+v\n%s", e.Platforms, out)
	}
	_, c, err = PatchLink(out, "sumika.yml", LinkChange{Key: LinkPlatformEnabled, Site: "x", Bool: false})
	if err != nil || c.LinkReader.Expand.ImageModeOf("x") != ImageModeRotate {
		t.Fatal("改整站开关不该冲掉发图方式", err)
	}
	for _, bad := range []LinkChange{
		{Key: LinkPlatformImage, Site: "nowhere", Text: ImageModeRotate},
		{Key: LinkPlatformImage, Site: "x", Text: "sideways"},
		{Key: LinkPlatformImage, Site: "x", Text: ""},
	} {
		if _, _, err := PatchLink(raw, "sumika.yml", bad); err == nil {
			t.Fatalf("应拒绝：%+v", bad)
		}
	}
	handWritten := []byte("linkReader:\n  expand:\n    platforms:\n      x: {imageMode: sideways}\n")
	if _, err := Parse(handWritten, "sumika.yml"); err == nil || !strings.Contains(err.Error(), "imageMode") {
		t.Fatalf("手写不认识的发图方式应报错并点名键：%v", err)
	}
}

// 没有转发模板的站点（Bluesky 等）也能选“从不合并转发”（用户 2026-11-13，R-218）；“总是”仍不行，手写的配置同样检查
func TestNeverForwardAllowedWithoutForwardTemplate(t *testing.T) {
	raw := []byte("linkReader:\n  expand:\n    mode: whitelist\n    conversations: []\n")
	_, c, err := PatchLink(raw, "sumika.yml", LinkChange{Key: LinkSiteForward, Site: "bluesky", Text: "never"})
	if err != nil || c.LinkReader.Expand.Sites["bluesky"].Forward != "never" {
		t.Fatalf("bluesky 应能写 never：%v", err)
	}
	if _, _, err = PatchLink(raw, "sumika.yml", LinkChange{Key: LinkSiteForward, Site: "bluesky", Text: "always"}); err == nil {
		t.Fatal("bluesky 没有转发模板，always 应被拒绝")
	}
	e := LinkExpand{Sites: map[string]LinkSite{"bluesky": {Forward: "never"}}}
	if bad := e.checkTemplates(); len(bad) != 0 {
		t.Fatalf("手写 never 应通过：%v", bad)
	}
	e.Sites["bluesky"] = LinkSite{Forward: "always"}
	if bad := e.checkTemplates(); len(bad) != 1 || !strings.Contains(bad[0], "bluesky") {
		t.Fatalf("手写 always 应被拒：%v", bad)
	}
}

// 显示简介开关（R-219）：能写进配置、读回来；没有简介那一行的站点（X 等）不能开；手写错的整份配置不通过
func TestSiteShowDescSavedAndValidated(t *testing.T) {
	raw := []byte("linkReader:\n  expand:\n    mode: whitelist\n    conversations: []\n")
	_, c, err := PatchLink(raw, "sumika.yml", LinkChange{Key: LinkSiteShowDesc, Site: "github", Bool: true})
	if err != nil || !c.LinkReader.Expand.Sites["github"].ShowDesc {
		t.Fatalf("github 应能开：%v", err)
	}
	if _, _, err = PatchLink(raw, "sumika.yml", LinkChange{Key: LinkSiteShowDesc, Site: "x", Bool: true}); err == nil {
		t.Fatal("x 的默认模板没有简介那一行，开应被拒绝")
	}
	if _, _, err = PatchLink(raw, "sumika.yml", LinkChange{Key: LinkSiteShowDesc, Site: "x", Bool: false}); err != nil {
		t.Fatalf("关永远可以：%v", err)
	}
	if _, _, err = PatchLink(raw, "sumika.yml", LinkChange{Key: LinkSiteShowDesc, Site: "nowhere", Bool: true}); err == nil {
		t.Fatal("不认识的站点应被拒绝")
	}
	e := LinkExpand{Sites: map[string]LinkSite{"booth": {ShowDesc: true}}}
	if bad := e.checkTemplates(); len(bad) != 0 {
		t.Fatalf("booth 手写 showDesc 应通过：%v", bad)
	}
	e.Sites["bluesky"] = LinkSite{ShowDesc: true}
	if bad := e.checkTemplates(); len(bad) != 1 || !strings.Contains(bad[0], "bluesky") {
		t.Fatalf("bluesky 手写 showDesc 应被拒：%v", bad)
	}
}
