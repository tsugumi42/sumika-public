package linkexpand

import (
	"context"
	"strings"
	"testing"

	assetfonts "sumika-native/internal/assets/fonts"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
)

// 推特卡片有译文时只画译文，正文下面一行“翻译自日语”（用户 2026-11-13）；没有译文照旧画原文、不加标记。
// 画在卡片上的字都过文字守卫：守卫里有译文和标记，没有原文。
func TestTweetCardShowsOnlyTranslationWithNote(t *testing.T) {
	const orig = "冬コミ受かりました！スペースは東1ホールです"
	run := func(r linkreader.Result, o Options) (guarded string, d Draft) {
		cards := linkcard.NewWith(linkcard.Options{FontPaths: fontPaths()})
		defer cards.Close()
		var got []string // 守卫收到的字：下面单独断言
		p := Preparer{Media: &pngLoader{}, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true},
			Check: func(context.Context) error { return nil },
			Guard: func(_ context.Context, s string) error { got = append(got, s); return nil }}
		o.Templates = map[string]string{"x": "@卡片"}
		o.Sites = map[string]SiteSetting{"x": {Forward: "never"}}
		d = Build(&r, "", o, 0)
		prepared, err := p.Prepare(context.Background(), &r, &d)
		if err != nil {
			t.Fatal(err)
		}
		prepared.Release()
		return strings.Join(got, "\n"), d
	}

	r := xPost(orig)
	g, d := run(r, Options{Translation: "冬季同人展上了！摊位在东 1 厅"})
	_ = d
	if !strings.Contains(g, "冬季同人展上了！摊位在东 1 厅") || !strings.Contains(g, "翻译自日语") {
		t.Fatalf("只该有译文和标记：%q", g)
	}
	cardItem := false
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Kind == "card" && it.Translation != "" && it.TranslationNote == "翻译自日语" {
				cardItem = true
			}
		}
	}
	if !cardItem {
		t.Fatalf("卡片项没带译文：%v", d.Describe())
	}

	// 画上去的是不是只有译文（原文不画）：linkcard.TestTweetCardTranslationNote 量布局；这里看交给卡片的 Tweet
	if tw := (&Preparer{}).tweetData(context.Background(), &r, true); tw.Text != orig || tw.TranslationNote != "" {
		t.Fatalf("没有译文项时 Tweet 是原文：%+v", tw)
	}
	// 没有译文（原文是中文不用翻、翻译超时、没开）：画原文，不加标记
	g, _ = run(xPost("今天的纸船下水了"), Options{})
	if !strings.Contains(g, "今天的纸船下水了") || strings.Contains(g, "翻译自") {
		t.Fatalf("没有译文照旧画原文：%q", g)
	}
	// 源语言认不出：写“翻译自外语”（法语不标成英语）
	g, _ = run(xPost("Le bateau en papier a flotté toute la journée"), Options{Translation: "纸船整天都浮在水上"})
	if !strings.Contains(g, "翻译自外语") || strings.Contains(g, "翻译自英语") {
		t.Fatalf("认不出语种写外语：%q", g)
	}
	g, _ = run(xPost("The paper boat floated all day and that is the best part of this"), Options{Translation: "纸船整天都浮在水上，这是最棒的部分"})
	if !strings.Contains(g, "翻译自英语") {
		t.Fatalf("英语：%q", g)
	}
	// 纯文字消息也只放译文（用户 2026-11-13，规格-链接发送改版 B13）：「译文」下一行（翻译自日语），原文不出现
	text := textOf(t, build(xPost(orig), Options{Translation: "冬季同人展上了！", Sites: map[string]SiteSetting{"x": {Forward: "never"}}}))
	if !strings.Contains(text, "「冬季同人展上了！」\n（翻译自日语）\n") || strings.Contains(text, orig) {
		t.Fatalf("文字版只放译文加翻译自：%q", text)
	}
}

// 样式用了 $卡片 的 X 模板也要翻译：以前只有模板里写了 $译文 才请求翻译，卡片样式永远拿不到译文。
func TestTranslationSourceForTweetCardStyle(t *testing.T) {
	r := xPost("冬コミ受かりました！スペースは東1ホールです")
	for _, tpl := range []string{"@卡片", "@推特卡片", "$卡片$链接"} {
		if src := TranslationSource(&r, Options{Templates: map[string]string{"x": tpl}, Sites: map[string]SiteSetting{"x": {Forward: "never"}}}); src == "" {
			t.Errorf("模板 %q 画卡片，该请求翻译", tpl)
		}
	}
	if src := TranslationSource(&r, Options{Templates: map[string]string{"x": "$链接"}, Sites: map[string]SiteSetting{"x": {Forward: "never"}}}); src != "" {
		t.Errorf("没有卡片也没有 $译文，不该翻：%q", src)
	}
}

func fontPaths() []string {
	if f := assetfonts.WenYuanPath(); f != "" {
		return []string{f}
	}
	return nil
}
