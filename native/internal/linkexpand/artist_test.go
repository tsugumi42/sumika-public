package linkexpand

import (
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 画师的推文发原图（规格-画师原图 第五节、验收 5～7）。作者、账号、图地址都是编的。

func artistBuild(r linkreader.Result, mode string, o Options) Draft {
	o.Artist = mode
	return build(r, o)
}

// withVideoBlock 给帖子加一个视频（媒体表和块列表都要有，内容是从块列表按顺序取的）
func withVideoBlock(r linkreader.Result) linkreader.Result {
	ms := append([]linkreader.Media(nil), r.Media.Value...)
	r.Media = av(append(ms, goldenVideo()))
	r.Blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	for i := range r.Media.Value {
		r.Blocks = append(r.Blocks, linkreader.Block{Type: r.Media.Value[i].Type, Media: &r.Media.Value[i]})
	}
	return r
}

func jsonOf(t *testing.T, d Draft) string {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 零值即旧行为：Artist 为空时，各种形状的 Build 输出和没有这个字段时逐字节相同（验收 5）。
func TestArtistZeroValueChangesNothing(t *testing.T) {
	shapes := map[string]linkreader.Result{}
	for n := 0; n <= 5; n++ {
		shapes["图"+string(rune('0'+n))] = xImages(n)
	}
	shapes["图加视频"] = withVideoBlock(xImages(1))
	for name, r := range shapes {
		for _, o := range []Options{{}, {DirectImageMax: 1}, {DirectImageMax: 9}, {Sites: map[string]SiteSetting{"x": {Forward: "never"}}}, {Templates: map[string]string{"x": "@卡片"}}} {
			want := jsonOf(t, build(r, o))
			o.Artist = ""
			if got := jsonOf(t, build(r, o)); got != want {
				t.Fatalf("%s：Artist 为空却变了\n%s\n%s", name, want, got)
			}
		}
	}
}

// 卡片加原图（默认）：卡片、链接、每张原图。1～2 张图直发一条；卡片也算一张，所以 3 张原图（共 4 个图项）按 B3 进合并转发。
func TestArtistCardAndOriginals(t *testing.T) {
	for n := 1; n <= 2; n++ {
		d := artistBuild(xImages(n), ArtistCardAndOriginals, Options{})
		if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
			t.Fatalf("%d 张：%v", n, kinds(d))
		}
		want := "card,text" + strings.Repeat(",image", n)
		if got := itemKinds(d.Parts[0].Body); got != want {
			t.Fatalf("%d 张：%s，想要 %s", n, got, want)
		}
		if link := d.Parts[0].Body[1].Text; link != "https://example.invalid/x/80002" {
			t.Fatalf("链接要是干净链接，不带正文：%q", link)
		}
	}
	d := artistBuild(xImages(3), ArtistCardAndOriginals, Options{})
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward {
		t.Fatalf("卡片加 3 张原图是 4 个图项，应该进合并转发：%v", kinds(d))
	}
	// 第一个节点是文字（链接），后面卡片、三张原图各一个节点
	nodes := d.Parts[0].Nodes
	if len(nodes) != 5 || nodes[0].Body[0].Kind != "text" || nodes[1].Body[0].Kind != "card" {
		t.Fatalf("节点：%+v", nodes)
	}
}

// 只发原图：链接、原图，没有卡片也没有正文；3 张直发，4 张起合并转发。
func TestArtistOriginalsOnly(t *testing.T) {
	for n := 1; n <= 3; n++ {
		d := artistBuild(xImages(n), ArtistOriginalsOnly, Options{})
		if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
			t.Fatalf("%d 张：%v", n, kinds(d))
		}
		if got, want := itemKinds(d.Parts[0].Body), "text"+strings.Repeat(",image", n); got != want {
			t.Fatalf("%d 张：%s，想要 %s", n, got, want)
		}
		if text := d.Parts[0].Body[0].Text; text != "https://example.invalid/x/80002" {
			t.Fatalf("只发链接，不发正文、名字、时间：%q", text)
		}
	}
	d := artistBuild(xImages(4), ArtistOriginalsOnly, Options{})
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward || len(d.Parts[0].Nodes) != 1 || itemKinds(d.Parts[0].Nodes[0].Body) != "text,image,image,image,image" {
		t.Fatalf("4 张应该合并转发（文字和 4 张图在同一个节点，用户 2026-11-14）：%v", kinds(d))
	}
}

// 原图地址原样交给发送（name=orig 的图在取数那一层就是原图），不换档、不缩。
func TestArtistKeepsOriginalImageURLs(t *testing.T) {
	r := xImages(2)
	for i := range r.Media.Value {
		r.Media.Value[i].URL = av("https://pbs.example.invalid/media/" + string(rune('a'+i)) + ".jpg?format=jpg&name=orig")
	}
	d := artistBuild(r, ArtistOriginalsOnly, Options{})
	var got []string
	for _, it := range d.Parts[0].Body {
		if it.Kind == "image" {
			got = append(got, it.Media.URL.Value)
		}
	}
	if len(got) != 2 || !strings.HasSuffix(got[0], "name=orig") || !strings.HasSuffix(got[1], "name=orig") {
		t.Fatalf("%v", got)
	}
}

// 图太多时 forward: never 照旧只发前几张；forward: always、自定义模板被画师的发法盖过（规格第一节取舍 1）。
func TestArtistOverridesTemplatesAndForwardSetting(t *testing.T) {
	never := Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}}
	d := artistBuild(xImages(5), ArtistOriginalsOnly, never)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
		t.Fatalf("never 不进转发：%v", kinds(d))
	}
	if n, _ := itemImages(d.Parts[0].Body); n != DefaultDirectImageMax {
		t.Fatalf("never 只发前 %d 张，得到 %d", DefaultDirectImageMax, n)
	}
	for _, o := range []Options{
		{Sites: map[string]SiteSetting{"x": {Forward: "always"}}},
		{Templates: map[string]string{"x": "纸张\n$正文文字$链接", "x-forward": "$合并转发$链接"}},
		{Templates: map[string]string{"x": "@卡片"}},
	} {
		d := artistBuild(xImages(2), ArtistOriginalsOnly, o)
		if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage || itemKinds(d.Parts[0].Body) != "text,image,image" {
			t.Fatalf("自定义写法 %+v 应该被盖过：%v %s", o, kinds(d), itemKinds(d.Parts[0].Body))
		}
	}
	// 直发图数上限 1、9
	d = artistBuild(xImages(2), ArtistOriginalsOnly, Options{DirectImageMax: 1})
	if d.Parts[0].Kind != types.PartForward {
		t.Fatalf("上限 1 时 2 张进转发：%v", kinds(d))
	}
	d = artistBuild(xImages(5), ArtistCardAndOriginals, Options{DirectImageMax: 9})
	if d.Parts[0].Kind != types.PartMessage || itemKinds(d.Parts[0].Body) != "card,text,image,image,image,image,image" {
		t.Fatalf("上限 9 时卡片加 5 张直发：%v %s", kinds(d), itemKinds(d.Parts[0].Body))
	}
}

// 不够条件的推文照旧：没有自己的图、只有视频、X 文章、不是帖子；以及未知的发法值（只认两个）。
func TestArtistNotEligibleStaysAsBefore(t *testing.T) {
	article := xImages(2)
	article.Article = av("文章正文")
	video := withVideoBlock(xImages(0))
	cases := map[string]linkreader.Result{"没有图": xImages(0), "只有视频": video, "X 文章": article}
	for name, r := range cases {
		want := jsonOf(t, build(r, Options{}))
		for _, mode := range []string{ArtistCardAndOriginals, ArtistOriginalsOnly} {
			if got := jsonOf(t, artistBuild(r, mode, Options{})); got != want {
				t.Errorf("%s（%s）应该照旧：\n%s\n%s", name, mode, want, got)
			}
		}
	}
	r := xImages(2)
	want := jsonOf(t, build(r, Options{}))
	if got := jsonOf(t, artistBuild(r, "乱写的值", Options{})); got != want {
		t.Errorf("不认识的发法值应该当没有：\n%s\n%s", want, got)
	}
	// 只有被引推文有图（自己没图）：照旧
	quoting := xImages(0)
	q := xImages(2)
	quoting.Quote = av(&q)
	if got, want := jsonOf(t, artistBuild(quoting, ArtistCardAndOriginals, Options{})), jsonOf(t, build(quoting, Options{})); got != want {
		t.Errorf("只有被引推文有图应该照旧")
	}
	// 非 X：Artist 不起作用
	pixiv := goldenBase("pixiv", "artwork", "90001")
	pixiv.Title = av("纸船试航")
	pixiv = withMedia(pixiv, goldenImage(1))
	pixiv.Stats["pages"] = av(int64(1))
	if got, want := jsonOf(t, artistBuild(pixiv, ArtistCardAndOriginals, Options{})), jsonOf(t, build(pixiv, Options{})); got != want {
		t.Errorf("非 X 的站点不受影响")
	}
}

// 拆开的横幅（几张同高的图）：画师的推文不拼回，每张原图各发各的（规格第一节取舍 2）。
func TestArtistDoesNotStitchPanorama(t *testing.T) {
	r := xImages(3)
	for i := range r.Media.Value {
		r.Media.Value[i].Width, r.Media.Value[i].Height = av(400), av(1000)
	}
	if panoramaCandidate(&r) == nil {
		t.Fatal("夹具应该是拆图候选")
	}
	plain := build(r, Options{})
	stitched := false
	for _, it := range plain.Parts[0].Body {
		stitched = stitched || it.PanoramaN > 0
	}
	if !stitched {
		t.Fatal("对照：不是画师时应该标拆图候选")
	}
	for _, mode := range []string{ArtistCardAndOriginals, ArtistOriginalsOnly} {
		d := artistBuild(r, mode, Options{})
		var walk func([]Item)
		walk = func(items []Item) {
			for _, it := range items {
				if it.PanoramaN > 0 || it.PanoramaOrig || it.PanoramaKeep {
					t.Fatalf("%s：画师的推文不该标拆图：%+v", mode, it)
				}
			}
		}
		for _, p := range d.Parts {
			walk(p.Body)
			for _, n := range p.Nodes {
				walk(n.Body)
			}
		}
		if mode == ArtistOriginalsOnly && len(d.Parts) == 1 && d.Parts[0].Kind == types.PartMessage {
			if n, all := itemImages(d.Parts[0].Body); n != 3 || all != 3 {
				t.Fatalf("三张原图各算一张：%d %d", n, all)
			}
		}
	}
}

// 视频照旧单独一条：画师的推文图加视频，图一条、视频一条。
func TestArtistVideoStaysStandalone(t *testing.T) {
	r := withVideoBlock(xImages(1))
	d := artistBuild(r, ArtistOriginalsOnly, Options{ForwardVideo: true, VideoEnabled: true})
	if len(d.Parts) < 1 {
		t.Fatal("没有消息")
	}
	var video, standalone bool
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Kind == "video" {
				video, standalone = true, it.Standalone
			}
		}
	}
	if !video || !standalone {
		t.Fatalf("视频应该在直发消息里标 Standalone：%s", jsonOf(t, d))
	}
}

// 提示（“发过 N 次”）照旧接在第一条上。
func TestArtistPreviousNoteStillAppended(t *testing.T) {
	r := xImages(1)
	d := Build(&r, "-71", Options{Artist: ArtistOriginalsOnly}, 2)
	body := d.Parts[0].Body
	if got := itemKinds(body); got != "text,image,text" || body[0].Text != "https://example.invalid/x/80002" || !strings.Contains(body[2].Text, "发过 2 次") {
		t.Fatalf("%s %+v", itemKinds(body), body)
	}
}

// 正文很长（本来要进合并转发）、带被引推文：画师的推文仍是直发的卡片或原图，不进转发（规格第一节取舍 1）。
func TestArtistLongTextAndQuoteStillDirect(t *testing.T) {
	r := xImages(2)
	r.Text = av(strings.Repeat("长", 400))
	r.Expansion = av("long")
	q := xImages(0)
	r.Quote = av(&q)
	if d := build(r, Options{}); d.Parts[0].Kind != types.PartForward {
		t.Fatalf("对照：不是画师时长推文进转发：%v", kinds(d))
	}
	for mode, want := range map[string]string{ArtistOriginalsOnly: "text,image,image", ArtistCardAndOriginals: "card,text,image,image"} {
		d := artistBuild(r, mode, Options{})
		if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage || itemKinds(d.Parts[0].Body) != want {
			t.Fatalf("%s：%v %s", mode, kinds(d), itemKinds(d.Parts[0].Body))
		}
	}
}
