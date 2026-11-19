package linkexpand

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
)

// 纯文字推文画推特卡片（主人 2026-11-19，ADR-399，规格-链接发送改版 C4）。作者、链接、正文都是编的。

func textCardOn() Options { return Options{TextCard: true} }

// 纯文字推文：一条消息，卡片加干净链接，不带文字版。
func TestTextOnlyTweetGetsCard(t *testing.T) {
	d := build(xImages(0), textCardOn())
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
		t.Fatalf("要一条消息：%v", kinds(d))
	}
	if got := itemKinds(d.Parts[0].Body); got != "card,text" {
		t.Fatalf("要卡片加链接：%s", got)
	}
	if link := d.Parts[0].Body[1].Text; link != "https://example.invalid/x/80002" {
		t.Fatalf("链接要是干净链接，不带正文：%q", link)
	}
}

// 开关关着（零值）就是旧的文字版，和不认识这个字段时一样。
func TestTextCardOffKeepsTextLayout(t *testing.T) {
	d := build(xImages(0), Options{})
	if got := itemKinds(d.Parts[0].Body); got != "text" || d.Parts[0].Body[0].Text != xChosenText {
		t.Fatalf("关着要照旧文字版：%s %q", got, d.Parts[0].Body[0].Text)
	}
}

// 不算纯文字的形状照旧按模板出文字版：自己有图、视频、取不到的媒体、外链卡、长文，被引推文带视频、更深的引用。
func TestTextCardNotForOtherShapes(t *testing.T) {
	quoted := func(edit func(q *linkreader.Result)) linkreader.Result {
		r := xImages(0)
		q := xImages(0)
		q.ID = "80003"
		edit(&q)
		r.Quote = linkreader.Field[*linkreader.Result]{State: "available", Value: &q}
		return r
	}
	shapes := map[string]linkreader.Result{
		"一张图":  xImages(1),
		"三张图":  xImages(3),
		"只有视频": withVideoBlock(xImages(0)),
		"媒体取不到": func() linkreader.Result {
			r := xImages(1)
			r.Media.Value[0].URL = linkreader.Field[string]{State: "unsupported"}
			return r
		}(),
		"外链卡": func() linkreader.Result {
			r := xImages(0)
			r.LinkCard = linkreader.Field[linkreader.LinkPreview]{State: "available", Value: linkreader.LinkPreview{Title: av("编的外链")}}
			return r
		}(),
		"长文": func() linkreader.Result {
			r := xImages(0)
			r.Article = linkreader.Field[string]{State: "available", Value: "编的长文"}
			return r
		}(),
		"还有更深引用": func() linkreader.Result {
			r := xImages(0)
			r.MoreQuotes = linkreader.Field[bool]{State: "available", Value: true}
			return r
		}(),
		"被引推文带视频": quoted(func(q *linkreader.Result) { *q = withVideoBlock(*q) }),
		"被引推文又引用": quoted(func(q *linkreader.Result) {
			q.Quote = linkreader.Field[*linkreader.Result]{State: "available", Value: &linkreader.Result{}}
		}),
		"被引推文带外链卡": quoted(func(q *linkreader.Result) { q.LinkCard = linkreader.Field[linkreader.LinkPreview]{State: "available"} }),
	}
	for name, r := range shapes {
		want := jsonOf(t, build(r, Options{}))
		if got := jsonOf(t, build(r, textCardOn())); got != want {
			t.Errorf("%s：开关开着也该照旧\n%s\n%s", name, want, got)
		}
	}
}

// 被引推文只有图（或纯文字）也算纯文字：卡片里画被引推文，不另出被引推文的节点。
func TestTextCardWithQuotedImages(t *testing.T) {
	r := xImages(0)
	q := xImages(2)
	q.ID = "80003"
	r.Quote = linkreader.Field[*linkreader.Result]{State: "available", Value: &q}
	d := build(r, textCardOn())
	if len(d.Parts) != 1 || itemKinds(d.Parts[0].Body) != "card,text" {
		t.Fatalf("要卡片加链接：%v %s", kinds(d), itemKinds(d.Parts[0].Body))
	}
}

// 写了 x 自定义模板就听模板的；画师发法只管有图的推文，纯文字推文照样走卡片。
func TestTextCardRespectsCustomTemplate(t *testing.T) {
	for _, k := range []string{"x", "x-forward"} {
		o := textCardOn()
		o.Templates = map[string]string{k: "$链接"}
		if TextCardApplies(xPtr(xImages(0)), o) {
			t.Fatalf("写了 %s 模板不该走卡片", k)
		}
	}
	o := textCardOn()
	o.Artist = ArtistOriginalsOnly
	if d := build(xImages(0), o); itemKinds(d.Parts[0].Body) != "card,text" {
		t.Fatalf("画师发法不管纯文字推文：%s", itemKinds(d.Parts[0].Body))
	}
}

// 超过转发门槛的长推文也只直发一张卡片（卡片默认不封顶，长文整张画）；forward: always 也不进合并转发。
func TestTextCardLongTweetStaysDirect(t *testing.T) {
	r := xImages(0)
	long := strings.Repeat("很长的一句编的正文。", 3000)
	r.Text = av(long)
	r.Blocks = []linkreader.Block{{Type: "text", Text: long}}
	for _, o := range []Options{textCardOn(), {TextCard: true, Sites: map[string]SiteSetting{"x": {Forward: "always"}}}} {
		d := build(r, o)
		if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage || itemKinds(d.Parts[0].Body) != "card,text" {
			t.Fatalf("长推文也只直发卡片：%v", kinds(d))
		}
	}
	if d := build(r, Options{}); d.Parts[0].Kind != types.PartForward {
		t.Fatalf("对照：关着时长推文该进合并转发：%v", kinds(d))
	}
}

func xPtr(r linkreader.Result) *linkreader.Result { return &r }

// 走完准备：卡片画成推特卡片（宽 720 的图），后面跟链接文字，一条消息。
func TestTextCardPrepared(t *testing.T) {
	r := xImages(0)
	cards := linkcard.New()
	defer cards.Close()
	p := Preparer{Media: &pngLoader{}, Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true},
		Check: func(context.Context) error { return nil },
		Guard: func(context.Context, string) error { return nil }}
	d := Build(&r, "", textCardOn(), 0)
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if len(prepared.Parts) != 1 || imageSegs(prepared.Parts) != 1 {
		t.Fatalf("要一条消息、一张卡片：%d 段 %d 张图", len(prepared.Parts), imageSegs(prepared.Parts))
	}
	body := prepared.Parts[0].Body
	if body[0].Kind != types.BodyImage || body[0].Media.Width != 720 {
		t.Fatalf("第一段要是推特卡片：%+v", body[0])
	}
	if !strings.Contains(body[len(body)-1].Text, "https://example.invalid/x/80002") {
		t.Fatalf("最后要有链接：%+v", body[len(body)-1])
	}
}
