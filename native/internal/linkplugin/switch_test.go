package linkplugin

import (
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/linkreader"
)

// 排队前的开关判定：短链只看网站开关，别的要有发送格式且网站、类型都开着
func TestExpandableTable(t *testing.T) {
	off := false
	e := config.LinkExpand{Platforms: map[string]config.LinkPlatform{
		"x":        {Enabled: &off},
		"bilibili": {Kinds: map[string]bool{"video": false}},
	}}
	cases := []struct {
		platform, kind string
		want           bool
	}{
		{"x", "post", false},
		{"x", "short", false},
		{"bilibili", "video", false},
		{"bilibili", "short", true},
		{"bilibili", "dynamic", true},
		{"bilibili", "mall", true},
		{"bilibili", "live", true},
		{"bilibili", "article", true},
		{"bilibili", "episode", true},
		{"spotify", "show", false}, // 只能取数
		{"github", "issue", false}, // 只能取数
		{"youtube", "video", true},
	}
	for _, c := range cases {
		if got := expandable(e, linkreader.Link{Platform: c.platform, Kind: c.kind}); got != c.want {
			t.Errorf("%s/%s 得 %v，应为 %v", c.platform, c.kind, got, c.want)
		}
	}
}
