package linktemplate

import (
	"strings"
	"testing"
)

// 复核问题 3：“图文”“混合”的说明要说清图多时的形状——有转发形态的站点只发前几张，没有的进合并转发。
func TestStyleHintsSayManyImagesShape(t *testing.T) {
	hint := func(key, id string) string {
		site, _, ok := SiteByKey(key)
		if !ok {
			t.Fatalf("没有站点 %s", key)
		}
		st, ok := site.StyleByID(id)
		if !ok {
			t.Fatalf("%s 没有样式 %s", key, id)
		}
		return st.Hint
	}
	if h := hint("bilibili-dynamic", StyleText); !strings.Contains(h, "只发前几张") {
		t.Fatalf("有转发形态的站点，图文要说图多只发前几张：%q", h)
	}
	for _, id := range []string{StyleText, StyleMixed} {
		if h := hint("bluesky", id); !strings.Contains(h, "合并转发") {
			t.Fatalf("没有转发形态的站点，%s 要说图多进合并转发：%q", id, h)
		}
	}
}
