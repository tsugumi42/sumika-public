package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 帖子长短按实际会发出去的文字字节数判断（ADR-391），x、telegram 都是；B 站动态仍按字数。
// 用自定义直发模板 $正文文字，数出来的就是正文本身的字节，免得被链接、作者行、时间干扰。
func TestAutoForwardUsesSentBytes(t *testing.T) {
	shape := func(key, text string, over int) string {
		var r linkreader.Result
		if key == "telegram" {
			r = tgResult(text, 0)
		} else {
			r = xImages(0)
			r.Text = av(text)
			r.Blocks = []linkreader.Block{{Type: "text", Text: text}}
		}
		o := Options{Sites: map[string]SiteSetting{key: {ForwardOverBytes: intp(over)}}, Templates: map[string]string{key: "$正文文字"}}
		return strings.Join(kinds(build(r, o)), ",")
	}
	for _, key := range []string{"x", "telegram"} {
		for _, c := range []struct {
			name, text string
			over       int
			want       string
		}{
			{"恰好等于门槛直发", strings.Repeat("a", 100), 100, "message"},
			{"超出一个字节转发", strings.Repeat("a", 101), 100, "forward"},
			{"多字节字符按字节算恰好等于", strings.Repeat("字", 33), 99, "message"},
			{"多字节字符超出一个字转发", strings.Repeat("字", 34), 99, "forward"},
			{"老规则 300 字不再影响容器", strings.Repeat("字", 301), 16500, "message"},
		} {
			if got := shape(key, c.text, c.over); got != c.want {
				t.Errorf("%s %s：%s，要 %s", key, c.name, got, c.want)
			}
		}
	}
	// 默认门槛 16500 字节：5400 个汉字（16200 字节）直发，5600 个（16800 字节）转发
	if got := shape("x", strings.Repeat("字", 5400), 16500); got != "message" {
		t.Errorf("5400 个汉字应直发：%s", got)
	}
	if got := shape("x", strings.Repeat("字", 5600), 16500); got != "forward" {
		t.Errorf("5600 个汉字应转发：%s", got)
	}
	// B 站动态不变：按字数，默认 100 字
	dyn := caseNamed(t, "bili-dynamic")
	dyn.Text = av(strings.Repeat("字", 60))
	dyn.Blocks = []linkreader.Block{{Type: "text", Text: dyn.Text.Value}}
	if got := strings.Join(kinds(build(dyn, Options{})), ","); got != "message" {
		t.Errorf("B 站动态 60 个汉字应仍是短：%s", got)
	}
	dyn.Text = av(strings.Repeat("字", 101))
	dyn.Blocks = []linkreader.Block{{Type: "text", Text: dyn.Text.Value}}
	if got := strings.Join(kinds(build(dyn, Options{})), ","); got != "forward" {
		t.Errorf("B 站动态 101 个汉字应是长：%s", got)
	}
}

// 301 个汉字的老“长帖”标记（classifyX 的 >300）不再让 X 直发被拒；按实发字节远低于默认门槛（ADR-391）。
// 旧代码在这条上会进合并转发。
func TestXLongPostOver300CharsStaysDirect(t *testing.T) {
	r := xImages(0)
	r.Text = av(strings.Repeat("乙", 301))
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	if got := strings.Join(kinds(build(r, Options{})), ","); got != "message" {
		t.Fatalf("301 个汉字应直发：%s", got)
	}
}

// 只放译文时按译文字节算：短原文长译文转发，长原文短译文直发（ADR-391）。
func TestAutoForwardCountsTranslationOnly(t *testing.T) {
	r := xImages(0)
	r.Author.Name, r.Author.Account = av("纸云"), av("paper_cloud")
	r.Text = av("short source")
	r.Blocks = []linkreader.Block{{Type: "text", Text: "short source"}}
	o := Options{Sites: map[string]SiteSetting{"x": {ForwardOverBytes: intp(160)}}, Templates: map[string]string{"x": "$链接$正文文字"}}
	long := strings.Repeat("译", 60) // 180 字节
	d := build(r, o)
	if got := strings.Join(kinds(d), ","); got != "message" {
		t.Fatalf("没有译文时按原文：%s", got)
	}
	o.Translation = long
	if got := strings.Join(kinds(build(r, o)), ","); got != "forward" {
		t.Fatalf("短原文长译文应转发：%s", got)
	}
	r.Text = av(strings.Repeat("源", 100)) // 300 字节
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	o.Translation = "短译文" // 9 字节
	if got := strings.Join(kinds(build(r, o)), ","); got != "message" {
		t.Fatalf("长原文短译文应直发：%s", got)
	}
}

// directTextOf 数一条直发草稿里所有文字项的字节（不数图、视频、卡片）。
func directTextOf(d Draft) int {
	n := 0
	for _, p := range d.Parts {
		if p.Kind != types.PartMessage {
			continue
		}
		for _, it := range p.Body {
			if it.Kind == types.BodyText {
				n += len(it.Text)
			}
		}
	}
	return n
}

// $正文 槽位带出来的文字也算进直发字节（ADR-391）。旧代码只数 PieceText，模板里只写着 "$链接\n$正文" 时
// 6000 个汉字（18000 字节）会被当成 30 多字节直发。
func TestAutoForwardCountsBodySlotText(t *testing.T) {
	r := xImages(0)
	r.Text = av(strings.Repeat("纸", 6000))
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	tpl := map[string]string{"x": "$链接\n$正文"}
	n := directTextOf(build(r, Options{Sites: map[string]SiteSetting{"x": {ForwardOverBytes: intp(1 << 30)}}, Templates: tpl}))
	if n < 18000 {
		t.Fatalf("$正文 没算进直发字节：%d", n)
	}
	for _, c := range []struct {
		name string
		over int
		want string
	}{
		{"恰好等于门槛直发", n, "message"},
		{"少一个字节转发", n - 1, "forward"},
	} {
		o := Options{Sites: map[string]SiteSetting{"x": {ForwardOverBytes: intp(c.over)}}, Templates: tpl}
		if got := strings.Join(kinds(build(r, o)), ","); got != c.want {
			t.Errorf("%s：%s，要 %s", c.name, got, c.want)
		}
	}
}

// 默认模板也按排出来的全部文字算：门槛等于直发文字字节时直发，少一个字节转发。
func TestAutoForwardDefaultTemplateBoundary(t *testing.T) {
	r := xImages(1)
	r.Text = av("纸船顺着水流慢慢漂过石桥。")
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}, {Type: "image", Media: &r.Media.Value[0]}}
	n := directTextOf(build(r, Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}}))
	if n <= 0 {
		t.Fatal("没数到直发文字")
	}
	for _, c := range []struct {
		name string
		over int
		want string
	}{
		{"恰好等于门槛直发", n, "message"},
		{"少一个字节转发", n - 1, "forward"},
	} {
		o := Options{Sites: map[string]SiteSetting{"x": {ForwardOverBytes: intp(c.over)}}}
		if got := strings.Join(kinds(build(r, o)), ","); got != c.want {
			t.Errorf("%s：%s，要 %s", c.name, got, c.want)
		}
	}
}

// 原来 TestXExpansionBoundaries 里的边界在 autoForward 这边补回来：两条视频、有引用、只有标题的文章仍进合并转发。
func TestAutoForwardXBoundaries(t *testing.T) {
	twoVideos := withVideoBlock(withVideoBlock(xImages(0)))
	quoting := xImages(0)
	q := xImages(0)
	quoting.Quote = av(&q)
	article := xImages(0)
	article.Article = linkreader.Field[string]{State: "not_returned", Reason: "article_body_omitted"}
	for name, r := range map[string]linkreader.Result{"两条视频": twoVideos, "有引用": quoting, "只有标题的文章": article} {
		if got := strings.Join(kinds(build(r, Options{ForwardVideo: true})), ","); got != "forward" {
			t.Errorf("%s 应进合并转发：%s", name, got)
		}
	}
}

// 被引推文取数失败：保持直发，但正文里要有一句“引用的推文没取到，看原文”（删 classifyX 后的行为，ADR-391）。
func TestXQuoteFetchFailedStaysDirectWithNote(t *testing.T) {
	r := xImages(0)
	r.Quote = linkreader.Field[*linkreader.Result]{State: "failed", Reason: "timeout"}
	d := build(r, Options{})
	if got := strings.Join(kinds(d), ","); got != "message" {
		t.Fatalf("引用取数失败应直发：%s", got)
	}
	if !strings.Contains(d.Semantic(), "引用的推文没取到，看原文") {
		t.Fatalf("直发文字里缺说明：%q", d.Semantic())
	}
}
