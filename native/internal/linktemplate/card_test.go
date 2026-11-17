package linktemplate

import (
	"testing"
)

type cardEnvTest map[string]string

func (e cardEnvTest) Text(id string) (string, bool) { v, ok := e[id]; return v, ok }
func (cardEnvTest) Slot(string) bool                { return false }

func TestParseCardRejectsLayoutFields(t *testing.T) {
	for _, src := range []string{"$分段", "$合并转发", "$节点", "$封面", "$不存在", "$", "[$标题"} {
		if _, err := ParseCard(src); err == nil {
			t.Errorf("%q 应被拒绝", src)
		}
	}
	for _, src := range []string{"", "  ", "$标题", "$UP主｜$作者", "${图片数}张", "[$字数]", "$title $images"} {
		if _, err := ParseCard(src); err != nil {
			t.Errorf("%q 应通过：%v", src, err)
		}
	}
}

func TestRenderCardDropsWholeItemWhenFieldMissing(t *testing.T) {
	render := func(src string, env cardEnvTest) string {
		tpl, err := ParseCard(src)
		if err != nil {
			t.Fatal(err)
		}
		return RenderCard(tpl, env)
	}
	env := cardEnvTest{"title": "纸船试航", "chars": "共 12 字"}
	if got := render("$标题｜$字数", env); got != "纸船试航｜共 12 字" {
		t.Fatalf("%q", got)
	}
	if got := render("$标题｜$UP主", env); got != "" {
		t.Fatalf("缺字段应整项不出，不留“未返回”：%q", got)
	}
	if got := render("$标题[｜$UP主]", env); got != "纸船试航" {
		t.Fatalf("可选段缺字段只省这段：%q", got)
	}
	if got := render("$标题", cardEnvTest{"title": "  "}); got != "" {
		t.Fatalf("空白的值当没取到：%q", got)
	}
	if got := render("", env); got != "" {
		t.Fatalf("空模板是默认：%q", got)
	}
	if got := render("甲\n乙", env); got != "甲 乙" {
		t.Fatalf("换行应换成空格：%q", got)
	}
}

func TestCardSetHasNoLayoutAndNamesAreUnique(t *testing.T) {
	for _, f := range CardSet().Fields() {
		if f.Kind != KindText {
			t.Errorf("卡片文字只能有文字占位符：%+v", f)
		}
	}
	for _, name := range []string{"标题", "字数", "图片数", "UP主", "作者", "站点"} {
		if _, ok := CardSet().Lookup(name); !ok {
			t.Errorf("缺 $%s", name)
		}
	}
}
