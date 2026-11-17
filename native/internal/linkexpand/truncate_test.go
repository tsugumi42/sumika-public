package linkexpand

import (
	"strings"
	"testing"
)

// 简介因字数上限被截断：截在最近的标点后，补“……”，下一行写“完整内容见原文”（规格-链接发送改版 B10，用户 2026-11-13）
func TestDescTruncatesAtPunctuation(t *testing.T) {
	r := caseNamed(t, "github")
	r.Text = av(strings.Repeat("说", 20) + "，这是第一句话。" + strings.Repeat("再", 30))
	o := Options{Sites: map[string]SiteSetting{"github": {DescLimit: intp(32)}}, Templates: map[string]string{"github": "$标题\n说明：$简介\n$链接"}}
	d := build(r, o)
	if s := d.Semantic(); !strings.Contains(s, "说明："+strings.Repeat("说", 20)+"，这是第一句话。……\n完整内容见原文\n") {
		t.Fatalf("应截在句号后: %q", s)
	}
}

// 正文节选：截断（这里或取数端）才写“完整内容见原文”，整篇都给了就不写
func TestNovelExcerptNotice(t *testing.T) {
	for _, c := range []struct {
		name      string
		body      string
		truncated bool
		notice    bool
	}{
		{"短篇给全了", "短小说。", false, false},
		{"取数端只给了开头", "开头。", true, true},
		{"超过 1500 字", strings.Repeat("长长长。", 500), false, true},
	} {
		r := caseNamed(t, "pixiv-novel")
		r.Text = av(c.body)
		r.Text.Truncated = c.truncated
		d := build(r, Options{})
		got := d.Semantic()
		if has := strings.Contains(got, "完整内容见原文"); has != c.notice {
			t.Errorf("%s：提示 %v，应为 %v", c.name, has, c.notice)
		}
		if strings.Contains(got, "（仅展示开头") || strings.Contains(got, "（已截取）") {
			t.Errorf("%s：旧写法还在: %q", c.name, got)
		}
	}
}

// 每个站点的“显示简介”开关（用户 2026-11-13，R-219）：默认关，内置默认不写简介；打开后内置默认多一行简介，按该站 descLimit 截；
// 用户自己写的模板不受开关影响（写了 $简介 照出）。
func TestShowDescSwitch(t *testing.T) {
	r := caseNamed(t, "github")
	r.Text = av("这是仓库的说明。" + strings.Repeat("很", 300))
	off := build(r, Options{})
	if s := off.Semantic(); strings.Contains(s, "这是仓库的说明") {
		t.Fatalf("默认不显示简介：%q", s)
	}
	on := build(r, Options{Sites: map[string]SiteSetting{"github": {ShowDesc: true, DescLimit: intp(20)}}})
	s := on.Semantic()
	if !strings.Contains(s, "这是仓库的说明。") || !strings.Contains(s, "完整内容见原文") || strings.Contains(s, strings.Repeat("很", 20)) {
		t.Fatalf("打开后带简介并截断（20 字，截在句号后）：%q", s)
	}
	// 简介在标题下面、链接上面的某一行，行间不空行
	if strings.Contains(s, "\n\n") {
		t.Fatalf("不该有空行：%q", s)
	}
	// 自定义模板：开关关着也照出 $简介，开着不重复加
	for _, show := range []bool{false, true} {
		d := build(r, Options{Sites: map[string]SiteSetting{"github": {ShowDesc: show, DescLimit: intp(5)}}, Templates: map[string]string{"github": "$标题\n$简介\n$链接"}})
		if got := d.Semantic(); strings.Count(got, "这是仓库") != 1 {
			t.Fatalf("自定义模板写了 $简介：show=%v %q", show, got)
		}
	}
	// 预设 @图文 也跟着开关
	d := build(r, Options{Sites: map[string]SiteSetting{"github": {ShowDesc: true, DescLimit: intp(20)}}, Templates: map[string]string{"github": "@图文"}})
	if got := d.Semantic(); !strings.Contains(got, "这是仓库的说明。") {
		t.Fatalf("@图文 开着应带简介：%q", got)
	}
}
