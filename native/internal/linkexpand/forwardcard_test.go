package linkexpand

import (
	"slices"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

func dynamicWith(text string, images int) linkreader.Result {
	r := caseNamed(nil2(), "bili-dynamic")
	r.Text = av(text)
	r.Blocks = nil
	for i := 0; i < images; i++ {
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "image", Media: new(goldenImage(i + 1))})
	}
	return r
}

// caseNamed 要 *testing.T；这些样本不会失败，用一个空壳满足签名。
func nil2() *testing.T { return &testing.T{} }

func forwardOf(d Draft) (Part, bool) {
	for _, p := range d.Parts {
		if p.Kind == types.PartForward {
			return p, true
		}
	}
	return Part{}, false
}

// 新写法：卡片四项按模板渲染，字数、图片数现场算；缺的字段整项退回默认（不留空壳）
func TestBuildCardFillsFromContent(t *testing.T) {
	r := dynamicWith(strings.Repeat("甲", 150), 3)
	f := ForwardOptions{Source: "$标题", News: []string{"$字数", "$图片数"}, Summary: "$UP主", Prompt: "$站点：$标题"}
	c := BuildCard(&r, f)
	if c == nil {
		t.Fatal("没有卡片")
	}
	if c.Source != "动态标题" || c.Summary != "纸灯实验室" || c.Prompt != "B站动态：动态标题" {
		t.Fatalf("卡片文字不对：%+v", c)
	}
	if !slices.Equal(c.News, []string{"共 150 字", "3 张图"}) {
		t.Fatalf("预览不对：%q", c.News)
	}
}

func TestBuildCardMissingFieldFallsBack(t *testing.T) {
	r := dynamicWith("正文", 0)
	r.Author.Name = linkreader.Field[string]{}
	c := BuildCard(&r, ForwardOptions{Source: "$UP主", News: []string{"$图片数", "$字数"}, Summary: "[$UP主]", Prompt: "作者 $UP主"})
	if c == nil || c.Source != "" || c.Summary != "" || c.Prompt != "" {
		t.Fatalf("缺字段的项应该整项不出：%+v", c)
	}
	if !slices.Equal(c.News, []string{"共 2 字"}) {
		t.Fatalf("没有图的那行应该不出，另一行照常：%q", c.News)
	}
	if got := BuildCard(&r, ForwardOptions{Source: "$UP主"}); got != nil {
		t.Fatalf("一项都没有应该是 nil：%+v", got)
	}
	if got := BuildCard(&r, ForwardOptions{}); got != nil {
		t.Fatalf("没设置应该是 nil：%+v", got)
	}
}

func TestBuildCardClipsToDisplayLength(t *testing.T) {
	r := dynamicWith("正文", 0)
	r.Title = av(strings.Repeat("长", 60))
	c := BuildCard(&r, ForwardOptions{Source: "$标题", Prompt: "$标题", News: []string{"$标题", "$标题", "$标题"}})
	if n := len([]rune(c.Source)); n > 16 || !strings.HasSuffix(c.Source, "…") {
		t.Fatalf("标题没截好：%q", c.Source)
	}
	if n := len([]rune(c.News[0])); n > 18 || len(c.News) != 2 {
		t.Fatalf("预览没截好或超过两行：%q", c.News)
	}
}

func TestTitleFallsBackToFirstLineOfText(t *testing.T) {
	r := dynamicWith("第一行\n第二行", 0)
	r.Title = linkreader.Field[string]{}
	c := BuildCard(&r, ForwardOptions{Source: "$标题"})
	if c == nil || c.Source != "第一行" {
		t.Fatalf("没有标题应该用正文第一行：%+v", c)
	}
}

// 节点名：设置写了就写死；没写发送时用她的 QQ 昵称，昵称取不到才退回原来的名字
func TestForwardNodeNames(t *testing.T) {
	long := caseNamed(nil2(), "x-long")
	d := build(long, Options{})
	p, ok := forwardOf(d)
	if !ok {
		t.Fatal("长帖应该转发")
	}
	for _, n := range p.Nodes {
		if n.Fixed || n.Name != NodeName {
			t.Fatalf("没设置节点名：应该用昵称、退路是原来的名字：%+v", n)
		}
	}
	d = build(long, Options{Forward: ForwardOptions{NodeName: "  纸鸢\n"}})
	p, _ = forwardOf(d)
	for _, n := range p.Nodes {
		if !n.Fixed || n.Name != "纸鸢" {
			t.Fatalf("设置了节点名：%+v", n)
		}
	}
	d = build(long, Options{Forward: ForwardOptions{NodeName: strings.Repeat("长", 80)}})
	p, _ = forwardOf(d)
	if n := len([]rune(p.Nodes[0].Name)); n != 40 {
		t.Fatalf("节点名没截到 40 个字：%d", n)
	}
	name, self := ForwardNodeName("   ", "退路")
	if name != "退路" || !self {
		t.Fatalf("全是空白应该当没写：%q %v", name, self)
	}
}

func TestPrepareCarriesCardAndSelf(t *testing.T) {
	r := caseNamed(nil2(), "x-long")
	d := build(r, Options{Forward: ForwardOptions{Source: "$站点"}})
	p, _ := forwardOf(d)
	if p.Card == nil || p.Card.Source != "X 推文" {
		t.Fatalf("转发段没有带卡片：%+v", p.Card)
	}
	for _, part := range d.Parts {
		if part.Kind != types.PartForward && part.Card != nil {
			t.Fatalf("不是转发的段不该有卡片：%+v", part)
		}
	}
}

// B 站动态分流：短的（正文不超过 forwardOver 字、图不超过 directImageMax 张，默认 3）一条消息；长的或多图整条进合并转发
func TestDynamicShortGoesDirect(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		images int
		set    SiteSetting
		want   string
	}{
		{"短文无图直发", "一句话", 0, SiteSetting{}, "message"},
		{"短文两图直发", "一句话", 2, SiteSetting{}, "message"},
		{"短文三图直发", "一句话", 3, SiteSetting{}, "message"},
		{"短文四图转发", "一句话", 4, SiteSetting{}, "forward"},
		{"刚好 100 字直发", strings.Repeat("甲", 100), 1, SiteSetting{}, "message"},
		{"101 字转发", strings.Repeat("甲", 101), 1, SiteSetting{}, "forward"},
		{"阈值调到 200 字直发", strings.Repeat("甲", 150), 0, SiteSetting{ForwardOver: intp(200)}, "message"},
		{"阈值 0 不按字数折", strings.Repeat("甲", 500), 0, SiteSetting{ForwardOver: intp(0)}, "message"},
		{"短文 always 转发", "一句话", 0, SiteSetting{Forward: "always"}, "forward"},
		{"长文 never 直发", strings.Repeat("甲", 500), 0, SiteSetting{Forward: "never"}, "message"},
	}
	for _, c := range cases {
		d := build(dynamicWith(c.text, c.images), Options{Sites: map[string]SiteSetting{"bilibili-dynamic": c.set}})
		if got := strings.Join(kinds(d), ","); got != c.want {
			t.Errorf("%s：%s，要 %s", c.name, got, c.want)
		}
	}
	// 直发上限各站统一（R-201）：调成 2 张，三图的动态就走转发
	if got := strings.Join(kinds(build(dynamicWith("一句话", 3), Options{DirectImageMax: 2})), ","); got != "forward" {
		t.Errorf("上限 2 张时三图动态应当转发：%s", got)
	}
}

func TestDynamicShortKeepsOtherContentInForward(t *testing.T) {
	r := dynamicWith("一句话", 1)
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "video", Media: new(goldenImage(9))})
	r.Blocks[len(r.Blocks)-1].Media.Type = "video"
	// 视频留在转发里是生产默认（expand.send.forwardVideo）；关着时视频拆成转发后面单独一条，见 TestForwardVideoOffSplitsVideos
	if got := strings.Join(kinds(build(r, Options{ForwardVideo: true})), ","); got != "forward" {
		t.Fatalf("带视频的动态应该转发：%s", got)
	}
	r = dynamicWith("一句话", 0)
	r.Text.Truncated = true
	if got := strings.Join(kinds(build(r, Options{})), ","); got != "forward" {
		t.Fatalf("正文被截断的动态应该转发：%s", got)
	}
}

// 短动态的排法：图在上，标题和正文，UP主和数据，发布时间（和进合并转发时一致），链接，行间不空行
func TestDynamicShortLayout(t *testing.T) {
	d := build(dynamicWith("一句话", 1), Options{})
	if len(d.Parts) != 1 {
		t.Fatalf("应该只有一条消息：%v", kinds(d))
	}
	body := d.Parts[0].Body
	if len(body) != 2 || body[0].Kind != types.BodyImage {
		t.Fatalf("图应该在最上面：%+v", body)
	}
	want := "动态标题\n一句话\nUP主：纸灯实验室\n点赞 3 | 评论 2 | 转发 1\n11-01 18:30\nhttps://example.invalid/bilibili/70001"
	if body[1].Text != want {
		t.Fatalf("文字排法不对：\n%q\n要 %q", body[1].Text, want)
	}
}

// 外部内容里带占位符字样：值原样输出，不会被当模板再解析一遍
func TestCardValuesAreNotReparsedAsTemplates(t *testing.T) {
	r := dynamicWith("正文", 0)
	r.Title = av("$作者 和 $图片数 [x]")
	c := BuildCard(&r, ForwardOptions{Source: "$标题"})
	if c == nil || c.Source != "$作者 和 $图片数 [x]" {
		t.Fatalf("值被当模板解析了：%+v", c)
	}
}

// 站点设成 never：带视频、引用、投票的动态仍直发，其余内容不能悄悄丢
func TestDynamicNeverKeepsOtherContent(t *testing.T) {
	r := dynamicWith("一句话", 1)
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "video", Media: new(goldenImage(9))})
	r.Blocks[len(r.Blocks)-1].Media.Type = "video"
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "quote_unavailable", Reason: "deleted"})
	d := build(r, Options{Sites: map[string]SiteSetting{"bilibili-dynamic": {Forward: "never"}}})
	if got := strings.Join(kinds(d), ","); strings.Contains(got, "forward") {
		t.Fatalf("never 不该进合并转发：%s", got)
	}
	videos, quote := 0, false
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Kind == types.BodyVideo {
				videos++
			}
			if it.Kind == types.BodyText && strings.Contains(it.Text, tombstoneText("deleted")) {
				quote = true
			}
		}
	}
	if videos != 1 || !quote {
		t.Fatalf("never 丢了视频或引用：videos=%d quote=%v %+v", videos, quote, d.Parts)
	}
	// 短动态不受影响：没有其余内容，仍是一条消息
	if got := strings.Join(kinds(build(dynamicWith("一句话", 1), Options{})), ","); got != "message" {
		t.Fatalf("短动态应只有一条消息：%s", got)
	}
}
