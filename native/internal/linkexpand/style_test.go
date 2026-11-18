package linkexpand

import (
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 样式（卡片 / 图文 / 混合）：每个站点每种样式都能 Build 出消息，形状符合样式的说明。

func liveResult() linkreader.Result {
	r := goldenBase("bilibili", "live", "900001")
	r.Title, r.Text = av("纸船试航直播"), av("编的直播间简介")
	r.Category = av("生活 / 手工")
	r.Flags["live_status"] = linkreader.Field[any]{State: "available", Value: 1}
	r.Media = av([]linkreader.Media{goldenImage(1)})
	return withStats(r, map[string]int64{"online": 1200, "attention": 5600, "followers": 8800})
}
func spaceResult() linkreader.Result {
	r := goldenBase("bilibili", "user", "900002")
	r.Title, r.Text = av("纸灯实验室"), av("做折纸和纸灯的小实验室。")
	r.Category = av("纸灯实验室官方账号")
	r.Media = av([]linkreader.Media{goldenImage(1)})
	return withStats(r, map[string]int64{"followers": 8800, "following": 120, "videos": 56, "likes": 30000})
}

func allStyleSamples(t *testing.T) []linkreader.Result {
	var out []linkreader.Result
	for _, c := range goldenCases() {
		out = append(out, c.build(t))
	}
	out = append(out, liveResult(), spaceResult())
	return append(out, biliMoreSamples()...)
}

func styleOptions(site linktemplate.Site, st linktemplate.Style) Options {
	o := Options{Templates: map[string]string{}, Sites: map[string]SiteSetting{}}
	if st.Direct != "" {
		o.Templates[site.Key] = st.Direct
	}
	if st.Forward != "" {
		o.Sites[site.Key] = SiteSetting{Forward: st.Forward}
	}
	return o
}

func hasKind(p Part, kind string) bool {
	for _, it := range p.Body {
		if it.Kind == kind {
			return true
		}
	}
	return false
}

func TestEveryStyleBuildsForEverySite(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range allStyleSamples(t) {
		if !Supports(r.Platform, r.Kind) || (r.Title.State != "available" && r.Text.State != "available") {
			continue // 没有标题也没有正文的样本走“解析失败”文字，不分样式
		}
		site, _, _ := linktemplate.SiteByKey(linktemplate.SiteKey(r.Platform, r.Kind))
		for _, st := range site.Styles() {
			d := Build(&r, "-71", styleOptions(site, st), 0)
			if len(d.Parts) == 0 {
				t.Errorf("%s/%s 样式 %s 没有出消息", r.Platform, r.Kind, st.ID)
				continue
			}
			seen[site.Key+"/"+st.ID] = true
			switch st.ID {
			case linktemplate.StyleCard:
				if !hasKind(d.Parts[0], "card") {
					t.Errorf("%s 卡片样式第一条应含卡片：%+v", site.Key, d.Parts[0].Body)
				}
				for _, p := range d.Parts {
					if p.Kind == types.PartForward {
						t.Errorf("%s 卡片样式不该有合并转发", site.Key)
					}
				}
			case linktemplate.StyleText:
				if hasKind(d.Parts[0], "card") {
					t.Errorf("%s 图文样式不该有卡片", site.Key)
				}
				for _, p := range d.Parts {
					if p.Kind == types.PartForward {
						t.Errorf("%s 图文样式不该有合并转发", site.Key)
					}
				}
			case linktemplate.StyleMixed:
				if site.HasForward() {
					if !slicesAnyForward(d.Parts) {
						t.Errorf("%s 混合样式应有合并转发", site.Key)
					}
				} else if len(d.Parts) < 2 || !hasKind(d.Parts[0], "card") {
					t.Errorf("%s 混合样式应是卡片加另一条文字消息：%d 条", site.Key, len(d.Parts))
				}
			}
		}
	}
	for _, k := range []string{"bilibili-live/card", "bilibili-space/mixed", "x/mixed", "github/mixed", "pixiv-artwork/card"} {
		if !seen[k] {
			t.Errorf("样本没覆盖 %s", k)
		}
	}
}

func slicesAnyForward(parts []Part) bool {
	for _, p := range parts {
		if p.Kind == types.PartForward {
			return true
		}
	}
	return false
}

// 当前配置对应哪种样式：选样式写进去的值，能读回同一种样式；手写的模板显示自定义
func TestCurrentStyleRoundTrip(t *testing.T) {
	for _, site := range linktemplate.Sites() {
		if site.Key == "default" {
			continue
		}
		for _, st := range site.Styles() {
			if got := site.CurrentStyle(st.Direct, st.Forward); got != st.ID {
				t.Errorf("%s：写入样式 %s 读回 %q", site.Key, st.ID, got)
			}
		}
		if got := site.CurrentStyle("手写的｜$标题", ""); got != "" {
			t.Errorf("%s：手写模板应是自定义，得到 %q", site.Key, got)
		}
	}
	// 主人现在的配置：B 站视频写了 @封面文字，应认作图文
	bv, _, _ := linktemplate.SiteByKey("bilibili-video")
	if got := bv.CurrentStyle("@封面文字", ""); got != linktemplate.StyleText {
		t.Errorf("@封面文字 应是图文，得到 %q", got)
	}
	if got := bv.CurrentStyle("", ""); got != linktemplate.StyleDefault {
		t.Errorf("什么都没写应是按内置默认，得到 %q", got)
	}
}
