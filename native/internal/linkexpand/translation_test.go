package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

func xPost(text string) linkreader.Result {
	r := goldenBase("x", "post", "80002")
	r.Text = av(text)
	r.Blocks = []linkreader.Block{{Type: "text", Text: text}}
	return r
}

func descTr(r *linkreader.Result, o Options) string {
	return strings.Join(Build(r, "", o, 0).Describe(), "\n--\n")
}

// 有译文时文字版只放译文（用户 2026-11-13，规格-链接发送改版 B13）：「译文」下一行（翻译自外语），原文不出现；没有译文照旧写原文。
func TestTranslationOnlyReplacesOriginal(t *testing.T) {
	r := xPost("Hello rockets and moons")
	never := map[string]SiteSetting{"x": {Forward: "never"}}
	plain := descTr(&r, Options{Sites: never})
	with := descTr(&r, Options{Sites: never, Translation: "你好，火箭和月亮"})
	if strings.Contains(plain, "你好") || strings.Contains(plain, "翻译自") {
		t.Fatalf("没有译文时不该出现：%q", plain)
	}
	if strings.Contains(with, "Hello rockets") || strings.Contains(with, "——————") {
		t.Fatalf("有译文时原文和分隔线都不该出现：%q", with)
	}
	if !strings.Contains(with, "「你好，火箭和月亮」\n（翻译自外语）\n") {
		t.Fatalf("排版不是「译文」加翻译自：%q", with)
	}
	// 除了正文那一行和翻译自那一行，其余排版和没有译文时逐字节一样
	want := strings.Replace(plain, "「Hello rockets and moons」", "「你好，火箭和月亮」\n（翻译自外语）", 1)
	if with != want {
		t.Fatalf("译文以外的排版变了：\n%q\n%q", with, want)
	}
}

// 想要原文的自定义模板：$原文 永远是原文，$译文 是译文（译文是可选段，没有时整段省掉）；旧的「$正文文字 —— $译文」写法有译文时 $译文 让位，不会写两遍。
func TestTranslationOriginalPlaceholderAndLegacyTemplate(t *testing.T) {
	r := xPost("Hello rockets and moons")
	never := map[string]SiteSetting{"x": {Forward: "never"}}
	both := Options{Sites: never, Translation: "你好，火箭和月亮", Templates: map[string]string{"x": "$原文[\n——————\n$译文]"}}
	if got := descTr(&r, both); got != "Hello rockets and moons\n——————\n你好，火箭和月亮" {
		t.Fatalf("$原文 加 $译文：%q", got)
	}
	both.Translation = ""
	if got := descTr(&r, both); got != "Hello rockets and moons" {
		t.Fatalf("没有译文：%q", got)
	}
	legacy := Options{Sites: never, Translation: "你好，火箭和月亮", Templates: map[string]string{"x": "$正文文字[\n——————\n$译文]"}}
	if got := descTr(&r, legacy); got != "你好，火箭和月亮" {
		t.Fatalf("旧写法有译文时只放译文，不重复：%q", got)
	}
}

// 翻译失败、超时、不用翻（没有译文）：原文照旧，也没有“翻译自”。
func TestTranslationMissingKeepsOriginal(t *testing.T) {
	r := xPost("Hello rockets and moons")
	got := descTr(&r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}})
	if !strings.Contains(got, "「Hello rockets and moons」") || strings.Contains(got, "翻译自") {
		t.Fatalf("没有译文：%q", got)
	}
}

func TestTranslationBlankOrSpacesIsNone(t *testing.T) {
	r := xPost("Hello rockets and moons")
	plain := descTr(&r, Options{})
	for _, tr := range []string{"", " ", "\n\t"} {
		if got := descTr(&r, Options{Translation: tr}); got != plain {
			t.Fatalf("译文 %q 应当等于没有：%q", tr, got)
		}
	}
}

func TestTranslationInForwardForm(t *testing.T) {
	r := xPost("Hello rockets and moons")
	o := Options{Translation: "你好，火箭和月亮", Sites: map[string]SiteSetting{"x": {Forward: "always"}}}
	got := descTr(&r, o)
	// 合并转发里的推文节点同样只放译文加翻译自（B13），原文和分隔线都不出现
	if !strings.Contains(got, "「你好，火箭和月亮」\n") || !strings.Contains(got, "（翻译自外语）") || strings.Contains(got, "Hello rockets") || strings.Contains(got, "——————") {
		t.Fatalf("合并转发里的推文节点只放译文：%q", got)
	}
	o.Translation = ""
	if strings.Contains(descTr(&r, o), "你好") {
		t.Fatal("没译文不该出现")
	}
}

func TestTranslationSourceOnlyOwnText(t *testing.T) {
	r := xPost("Own text here")
	q := xPost("Quoted text there")
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "poll"}, linkreader.Block{Type: "quote", Quote: &q})
	if got := TranslationSource(&r, Options{}); got != "Own text here" {
		t.Fatalf("只该翻自己的正文，得到 %q", got)
	}
	// 没有 text 块时用 Text
	r2 := goldenBase("x", "post", "80003")
	r2.Text = av("Only text field")
	if got := TranslationSource(&r2, Options{}); got != "Only text field" {
		t.Fatalf("得到 %q", got)
	}
	// 多个 text 块按原顺序、换行连接
	r3 := xPost("first")
	r3.Blocks = append(r3.Blocks, linkreader.Block{Type: "text", Text: "second"})
	if got := TranslationSource(&r3, Options{}); got != "first\nsecond" {
		t.Fatalf("得到 %q", got)
	}
}

func TestTranslationSourceGates(t *testing.T) {
	r := xPost("Hello rockets and moons")
	// 用户自定义模板里没有 $译文、$正文文字、$卡片：不调模型
	custom := Options{Templates: map[string]string{"x": "$作者\n$原文\n$链接", "x-forward": "$作者$节点$正文"}}
	if got := TranslationSource(&r, custom); got != "" {
		t.Fatalf("模板没用译文也要翻了：%q", got)
	}
	// 永不合并转发时合并转发那份用不到：只看直发模板
	if got := TranslationSource(&r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}, Templates: map[string]string{"x": "$作者\n$原文"}}); got != "" {
		t.Fatalf("永不合并转发时还看了转发模板：%q", got)
	}
	// 自定义模板写了 $译文：要翻（中英文名都认）
	for _, src := range []string{"$正文文字\n$译文", "$正文文字\n$Translation"} {
		if got := TranslationSource(&r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}, Templates: map[string]string{"x": src}}); got == "" {
			t.Fatalf("模板 %q 用了译文却没翻", src)
		}
	}
	// 写错的模板回退内置默认，内置默认里有 $译文
	if got := TranslationSource(&r, Options{Templates: map[string]string{"x": "$不存在的字段"}}); got == "" {
		t.Fatal("写错的模板应回退内置默认")
	}
	// 没接翻译的站点
	b := goldenBase("bilibili", "video", "BV1xx411c7mD")
	b.Text = av("Hello rockets and moons")
	if got := TranslationSource(&b, Options{}); got != "" {
		t.Fatalf("没接翻译的站点不翻：%q", got)
	}
	// 没有正文
	e := goldenBase("x", "post", "80004")
	if got := TranslationSource(&e, Options{}); got != "" {
		t.Fatalf("没有正文不翻：%q", got)
	}
}

// 字段表里有 $译文 的站点，和翻译接入点登记的站点必须是同一批；只改一边会让设置页开关和实际行为对不上
func TestTranslatableSitesMatchFieldTable(t *testing.T) {
	for _, p := range linktemplate.Catalog() {
		for _, k := range p.Kinds {
			if k.Support != linktemplate.SupportExpand || k.Site == "" {
				continue
			}
			site, _, _ := linktemplate.SiteByKey(k.Site)
			_, has := site.Set.Lookup("translation")
			if has != translatableSites[k.Site] {
				t.Errorf("站点 %s：字段表有译文=%v，translatableSites=%v", k.Site, has, translatableSites[k.Site])
			}
		}
	}
}
