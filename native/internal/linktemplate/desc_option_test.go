package linktemplate

import (
	"strings"
	"testing"
)

// 内置默认带“简介位”记号：showDesc 关着（默认）时一个记号都不漏出来，开着时多一行 $简介（用户 2026-11-13，R-219）
func TestDescMarkOnlyShowsWhenAsked(t *testing.T) {
	want := map[string]bool{
		"bilibili-video": true, "bilibili-live": true, "bilibili-bangumi": true, "bilibili-course": true, "bilibili-audio": true,
		"bilibili-collection": true, "bilibili-workshop": true, "pixiv-novel": true, "pixiv-series": true, "github": true,
		"xiaoheihe": true, "booth": true, "steam": true, "afdian": true,
	}
	for _, s := range Sites() {
		if s.HasDescOption() != want[s.Key] {
			t.Errorf("%s：HasDescOption=%v，应为 %v", s.Key, s.HasDescOption(), want[s.Key])
		}
		for _, forward := range []bool{false, true} {
			if forward && !s.HasForward() {
				continue
			}
			off, on := s.SourceWith(forward, false), s.SourceWith(forward, true)
			if strings.Contains(off, descMark) || strings.Contains(on, descMark) || (want[s.Key] && strings.Contains(off, "$简介")) {
				t.Errorf("%s forward=%v：默认关时不该有简介，记号不能漏出来：%q / %q", s.Key, forward, off, on)
			}
			if s.Source(forward) != off {
				t.Errorf("%s：Source 应等于关着的 SourceWith", s.Key)
			}
			if want[s.Key] && s.Key != "pixiv-series" && !forward && !strings.Contains(on, "[\n$简介]") {
				t.Errorf("%s：开着应带 [\n$简介]：%q", s.Key, on)
			}
			if _, err := Parse(on, s.Set); err != nil {
				t.Errorf("%s forward=%v：带简介的默认模板解析失败：%v", s.Key, forward, err)
			}
		}
		// 内置预设（图文、封面文字、混合）同样跟着开关；解析都不出错
		for _, p := range s.Presets {
			off, err := s.ResolveWith("@"+p.Name, false)
			on, err2 := s.ResolveWith("@"+p.Name, true)
			if err != nil || err2 != nil || strings.Contains(off, descMark) || strings.Contains(on, descMark) {
				t.Errorf("%s @%s：%v %v 记号漏出来：%q / %q", s.Key, p.Name, err, err2, off, on)
			}
			if _, err := s.ParseWith("@"+p.Name, true); err != nil {
				t.Errorf("%s @%s 带简介解析失败：%v", s.Key, p.Name, err)
			}
		}
	}
	// 设置页的预览、“套用”按钮写进配置的只是 @名字：任何公开入口都看不到位置记号
	for _, s := range Sites() {
		for _, p := range s.Presets {
			if out, err := s.Preview("@" + p.Name); err != nil || strings.Contains(out, "{{") {
				t.Errorf("%s @%s 预览漏记号：%q %v", s.Key, p.Name, out, err)
			}
		}
		for _, forward := range []bool{false, true} {
			if forward && !s.HasForward() {
				continue
			}
			if out, err := s.Preview(s.Source(forward)); err != nil || strings.Contains(out, "{{") {
				t.Errorf("%s 默认预览漏记号：%q %v", s.Key, out, err)
			}
		}
	}
	// 用户自己写的模板不经过替换：开关不影响，连长得像记号的字也原样
	gh, _, _ := SiteByKey("github")
	for _, desc := range []bool{false, true} {
		got, err := gh.ResolveWith("$标题{{简介}}\n$链接", desc)
		if err != nil || got != "$标题{{简介}}\n$链接" {
			t.Errorf("自定义模板应原样：desc=%v %q %v", desc, got, err)
		}
	}
	// 图文预设在 GitHub 上和默认一样，也有简介位
	if on, _ := gh.ResolveWith("@图文", true); !strings.Contains(on, "$简介") {
		t.Errorf("@图文 开着应带简介：%q", on)
	}
}
