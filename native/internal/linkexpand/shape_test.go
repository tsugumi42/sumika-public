package linkexpand

// 发送形状的回归测试（shape.go；2026-11-12 样张复核 #7 #8 #25 #43 #48 #57 #59）。数据都是编的。

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// mediaLoader 按地址回图；地址不在表里的图下载失败；视频一律给一段假视频。
type mediaLoader struct{ ok map[string]bool }

func (l mediaLoader) Image(_ context.Context, _ string, src linkreader.Media) (Loaded, error) {
	if !l.ok[src.URL.Value] {
		return Loaded{}, errors.New("编的：这张没下下来")
	}
	return Loaded{Kind: types.BodyImage, Resource: types.MemoryMedia("i", "image/png", 1, 1, []byte("png"))}, nil
}
func (mediaLoader) Video(context.Context, string, *linkreader.Result, linkreader.Media, VideoLimits, bool) (Loaded, error) {
	return Loaded{Kind: types.BodyVideo, Resource: types.MemoryMedia("v", "video/mp4", 1, 1, []byte("mp4"))}, nil
}
func (mediaLoader) Ugoira(context.Context, *linkreader.UgoiraInfo) (Loaded, error) {
	return Loaded{}, errors.New("编的")
}

func shapePreparer(l MediaLoader) *Preparer {
	caps := types.MediaCaps{Image: true, ImageBase64: true, Video: true, ForwardImage: true, ForwardVideo: true}
	return &Preparer{Media: l, Direct: caps, Forward: caps, Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
}

func prepareShape(t *testing.T, r linkreader.Result, d Draft, l MediaLoader) []types.Part {
	t.Helper()
	got, err := shapePreparer(l).Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = got.Release() })
	return got.Parts
}

func segKinds(body []types.BodySegment) string {
	var out []string
	for _, s := range body {
		out = append(out, s.Kind)
	}
	return strings.Join(out, ",")
}

// blueskyImages 是一条带 n 张图的 Bluesky 帖子（没有转发模板的站点）。
func blueskyImages(n int) linkreader.Result {
	r := siteResult("bluesky", "post", "90041/bbbbbbbbbbbbb")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper.example.invalid")
	r.Title, r.Text = av("编的相册"), av("编的相册")
	r.Stats["likes"] = av(int64(5))
	var ms []linkreader.Media
	for i := 1; i <= n; i++ {
		ms = append(ms, goldenImage(i))
	}
	r = withMedia(r, ms...)
	r.Blocks = append([]linkreader.Block{{Type: "text", Text: r.Text.Value}}, r.Blocks...)
	return r
}

// #7 #48：直发最多 3 张对所有站点都管（以前只管 X、pixiv、Telegram、B 站动态，BS-06 五张图挤一条）。
// 超过就进合并转发：第一个节点是全部文字（链接也在里面），后面一图一节点。
func TestDirectImageCapAllSites(t *testing.T) {
	d := build(blueskyImages(3), Options{})
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
		t.Fatalf("3 张直发一条：%v", kinds(d))
	}
	r := blueskyImages(5)
	d = build(r, Options{})
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartForward || len(d.Parts[0].Nodes) != 1 {
		t.Fatalf("5 张应进合并转发（文字和五张图在同一个节点，用户 2026-11-14）：%v", d.Describe())
	}
	head := itemsTextOf(d.Parts[0].Nodes[0])
	if !strings.Contains(head, r.CleanURL) || !strings.Contains(head, "编的相册") || !strings.Contains(head, "点赞 5") {
		t.Fatalf("第一个节点应是全部文字、带链接：%q", head)
	}
	if got := itemKinds(d.Parts[0].Nodes[0].Body); got != "text"+strings.Repeat(",image", 5) {
		t.Fatalf("文字在前、图跟在后面：%s", got)
	}
	// 上限改成 5 就直发
	if d = build(r, Options{DirectImageMax: 5}); d.Parts[0].Kind != types.PartMessage {
		t.Fatalf("上限 5 时直发：%v", kinds(d))
	}
	// “发过 N 次”接在第一个节点（文字）后面，不另发一条
	d = Build(&r, "-71", Options{}, 2)
	if len(d.Parts) != 1 || !strings.HasSuffix(itemsTextOf(d.Parts[0].Nodes[0]), "发过 2 次") {
		t.Fatalf("提示应接进第一个节点：%v", d.Describe())
	}
}

// 用户 2026-11-13（R-217、R-218）：站点设成 never、图多于上限时只发前 3 张，文字末尾标（3/总数），不拆成几条。
// 没有转发模板的 Bluesky 也能设 never。
func TestDirectImageCapNeverKeepsFirst(t *testing.T) {
	r := blueskyImages(7)
	d := Build(&r, "-71", Options{Sites: map[string]SiteSetting{"bluesky": {Forward: "never"}}}, 0)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage || d.Parts[0].Quote == "" {
		t.Fatalf("7 张应只剩一条消息（带引用）：%v", d.Describe())
	}
	if got := itemKinds(d.Parts[0].Body); strings.Count(got, "image") != 3 {
		t.Fatalf("只留前 3 张图：%s", got)
	}
	// 图都下得下来：文字末尾标（3/7），一共 3 张图
	ok := map[string]bool{}
	for i := 1; i <= 7; i++ {
		ok[goldenImage(i).URL.Value] = true
	}
	parts := prepareShape(t, r, d, mediaLoader{ok: ok})
	if len(parts) != 1 || countVisual(parts[0].Body) != 3 {
		t.Fatalf("准备后一条消息 3 张图：%d 条 %s", len(parts), segKinds(parts[0].Body))
	}
	if joined := segText(parts[0].Body); !strings.HasSuffix(joined, "（3/7）") || strings.Count(joined, "（3/") != 1 {
		t.Fatalf("文字末尾应标一次（3/7）：%q", joined)
	}
	// 前 3 张里有一张没下下来：标的是真正发出去的张数（2/7），不说谎
	ok[goldenImage(2).URL.Value] = false
	parts = prepareShape(t, r, d, mediaLoader{ok: ok})
	if countVisual(parts[0].Body) != 2 || !strings.Contains(segText(parts[0].Body), "（2/7）") {
		t.Fatalf("按实际发出的张数标：%q 图 %d", segText(parts[0].Body), countVisual(parts[0].Body))
	}
	// 没超过上限不标
	r3 := blueskyImages(3)
	d3 := Build(&r3, "-71", Options{Sites: map[string]SiteSetting{"bluesky": {Forward: "never"}}}, 0)
	ok3 := map[string]bool{}
	for i := 1; i <= 3; i++ {
		ok3[goldenImage(i).URL.Value] = true
	}
	if parts = prepareShape(t, r3, d3, mediaLoader{ok: ok3}); strings.Contains(segText(parts[0].Body), "（3/") {
		t.Fatalf("3 张不该标：%q", segText(parts[0].Body))
	}
	// X 设成 never 同样只留前 3 张（B-20、X-06 这类在图文样式里 26 张、4 张）
	d = build(xImages(4), Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}})
	if len(d.Parts) != 1 || strings.Count(itemKinds(d.Parts[0].Body), "image") != 3 || d.Parts[0].imageTotal != 4 {
		t.Fatalf("X never 4 张只留 3：%v 总数 %d", kinds(d), d.Parts[0].imageTotal)
	}
}

// segText 把一条消息里的文字段按顺序接起来。
func segText(body []types.BodySegment) string {
	var out []string
	for _, s := range body {
		if s.Kind == types.BodyText {
			out = append(out, s.Text)
		}
	}
	return strings.Join(out, "\n")
}

// 一段文字都没有的图（B 站动态 never、图在文字前）：标记放在第一张图前面，不丢
func TestMarkTruncatedWithoutText(t *testing.T) {
	body := []types.BodySegment{{Kind: types.BodyImage}, {Kind: types.BodyImage}, {Kind: types.BodyImage}}
	got := markTruncated(body, 3, 9)
	if segKinds(got) != "text,image,image,image" || got[0].Text != "（3/9）" {
		t.Fatalf("没有文字就在第一张图前加：%s %+v", segKinds(got), got[0])
	}
	if got := markTruncated([]types.BodySegment{{Kind: types.BodyText, Text: "字"}}, 0, 9); got[0].Text != "字" {
		t.Fatal("一张都没发出去不标")
	}
}

// #57 #59：直发的消息里视频不和文字挤一条（MK-04、MA-02 是 [文字, 视频, 文字] 一条）；文字并成一条，视频另起一条。
func TestDirectVideoNotWithText(t *testing.T) {
	r := siteResult("misskey", "note", "90050")
	r.Author.Name, r.Author.Account = av("纸云"), av("paper")
	r.Text = av("编的视频帖")
	v := goldenVideo()
	r = withMedia(r, v)
	r.Blocks = append([]linkreader.Block{{Type: "text", Text: r.Text.Value}}, r.Blocks...)
	d := build(r, Options{VideoEnabled: true})
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Kind == types.BodyVideo && !it.Standalone {
				t.Fatalf("视频应单独一条：%+v", d.Parts)
			}
		}
	}
	parts := prepareShape(t, r, d, mediaLoader{})
	if len(parts) != 2 || segKinds(parts[0].Body) != "text" || segKinds(parts[1].Body) != types.BodyVideo {
		var got []string
		for _, p := range parts {
			got = append(got, segKinds(p.Body))
		}
		t.Fatalf("应是文字一条、视频一条：%v", got)
	}
	if !strings.Contains(parts[0].Body[0].Text, "编的视频帖") || !strings.Contains(parts[0].Body[0].Text, r.CleanURL) {
		t.Fatalf("前后的文字并成一条：%q", parts[0].Body[0].Text)
	}
}

// 单视频推文（没有引用）：文字一条、视频另起一条，不进合并转发（复核第 19 节：X-08、X-24 这样是对的，留住）。
func TestSingleVideoTweetShape(t *testing.T) {
	r := xImages(0)
	v := goldenVideo()
	r = withMedia(r, v)
	r.Blocks = append([]linkreader.Block{{Type: "text", Text: r.Text.Value}}, r.Blocks[1:]...)
	d := build(r, Options{VideoEnabled: true})
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
		t.Fatalf("单视频推文直发：%v", kinds(d))
	}
	parts := prepareShape(t, r, d, mediaLoader{})
	if len(parts) != 2 || segKinds(parts[0].Body) != "text" || segKinds(parts[1].Body) != types.BodyVideo {
		t.Fatalf("文字一条、视频一条：%d 段", len(parts))
	}
}

// #25 X-07：4 张同高的图在 Build 时当拆图候选（按一张算、直发），Prepare 判出不是横幅后 4 张原图不能挤一条：改成合并转发。
// 真横幅拆的 4 张仍拼回一张直发（TestPreparePanoramaSendsOneStitchedImage）。
func TestPanoramaNotStitchedOverCapGoesForward(t *testing.T) {
	imgs := []image.Image{texturedBanner(300, 568, 31), texturedBanner(300, 568, 32), texturedBanner(300, 568, 33), texturedBanner(300, 568, 34)}
	r, l := splitPost(t, imgs)
	d := Build(&r, "-71", Options{}, 1)
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage || d.Parts[0].Body[1].PanoramaN != 4 {
		t.Fatalf("Build 时是拆图候选、直发：%v", d.Describe())
	}
	got, err := testPreparer(l).Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Release()
	if len(got.Parts) != 1 || got.Parts[0].Kind != types.PartForward || len(got.Parts[0].Nodes) != 1 {
		t.Fatalf("没拼成的 4 张应进合并转发（文字和图在同一个节点）：%d 段", len(got.Parts))
	}
	head := got.Parts[0].Nodes[0]
	if len(head.Body) != 5 || !strings.Contains(head.Body[0].Text, r.CleanURL) || !strings.Contains(head.Body[0].Text, "发过 1 次") || head.Name != NodeName || !head.Self {
		t.Fatalf("第一个节点是全部文字：%+v", head)
	}
	if got.Parts[0].Quote != "-71" {
		t.Fatalf("引用留在段上（发送端对转发段去掉）：%q", got.Parts[0].Quote)
	}
	// never：只留前 3 张，文字末尾标（3/4），不拆成两条（用户 2026-11-13，R-217）
	r2, l2 := splitPost(t, imgs)
	d = Build(&r2, "-71", Options{Sites: map[string]SiteSetting{"x": {Forward: "never"}}}, 0)
	got2, err := testPreparer(l2).Prepare(context.Background(), &r2, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer got2.Release()
	if len(got2.Parts) != 1 || segKinds(got2.Parts[0].Body) != "text,image,image,image" || !strings.HasSuffix(segText(got2.Parts[0].Body), "（3/4）") {
		t.Fatalf("never 时只留前 3 张并标（3/4）：%d 段 %s %q", len(got2.Parts), segKinds(got2.Parts[0].Body), segText(got2.Parts[0].Body))
	}
}

func pixivPages(n int) linkreader.Result {
	r := goldenBase("pixiv", "artwork", "90002")
	r.Title = av("纸船试航")
	var ms []linkreader.Media
	for i := 1; i <= n; i++ {
		ms = append(ms, goldenImage(i))
	}
	r = withMedia(r, ms...)
	r.Stats["pages"] = av(int64(n))
	return r
}

// #43 P-10：作品 4 页、图一张都没取到（匿名被拒），不进只有一个文字节点的合并转发，直接发一条文字。
func TestPixivNoImagesNotForward(t *testing.T) {
	r := goldenBase("pixiv", "artwork", "90003")
	r.Title = av("纸船试航")
	r.Media = linkreader.Field[[]linkreader.Media]{State: "login_required", Reason: "anonymous_access_denied"}
	r.Stats["pages"] = av(int64(4))
	d := build(r, Options{})
	if len(d.Parts) != 1 || d.Parts[0].Kind != types.PartMessage {
		t.Fatalf("没取到图应直发一条：%v", d.Describe())
	}
	if !strings.Contains(strings.Join(d.Describe(), "\n"), r.CleanURL) {
		t.Fatalf("链接要在：%v", d.Describe())
	}
}

// #43 的另一半：取数给了 4 张图（进了转发），下载只成了 2 张，转发收回成一条消息；成了 4 张照旧转发。
func TestForwardByImagesCollapsesWhenFewPrepared(t *testing.T) {
	r := pixivPages(4)
	d := Build(&r, "-71", Options{}, 0)
	if d.Parts[0].Kind != types.PartForward {
		t.Fatalf("4 张进转发：%v", kinds(d))
	}
	parts := prepareShape(t, r, d, mediaLoader{ok: map[string]bool{goldenImage(1).URL.Value: true, goldenImage(3).URL.Value: true}})
	if len(parts) != 1 || parts[0].Kind != types.PartMessage || imageSegments(parts[0]) != 2 || parts[0].Quote != "-71" {
		t.Fatalf("只下到 2 张应收回成一条带引用的消息：%+v", parts)
	}
	if parts[0].Body[0].Kind != types.BodyText || !strings.Contains(parts[0].Body[0].Text, r.CleanURL) {
		t.Fatalf("文字在前、带链接：%s", segKinds(parts[0].Body))
	}
	all := map[string]bool{}
	for i := 1; i <= 4; i++ {
		all[goldenImage(i).URL.Value] = true
	}
	d = Build(&r, "-71", Options{}, 0)
	if parts = prepareShape(t, r, d, mediaLoader{ok: all}); len(parts) != 1 || parts[0].Kind != types.PartForward {
		t.Fatalf("4 张都下到照旧转发：%+v", parts)
	}
	// 长推文（因为字多进的转发）图没下下来也不收回
	long := xImages(1)
	long.Text = av(strings.Repeat("长", 400))
	long.Blocks[0].Text = long.Text.Value
	d = build(long, Options{})
	if parts = prepareShape(t, long, d, mediaLoader{}); parts[0].Kind != types.PartForward {
		t.Fatalf("字多的转发不收回：%+v", parts)
	}
}

// #43（复核补）：没有转发模板的站点（Bluesky）因图多进的合并转发，图没下下来也收回成一条，不发只剩文字的转发（BS-02）。
func TestShapeForwardCollapsesNoTemplateSite(t *testing.T) {
	r := blueskyImages(5)
	d := Build(&r, "-71", Options{}, 0)
	if d.Parts[0].Kind != types.PartForward {
		t.Fatalf("5 张进转发：%v", kinds(d))
	}
	parts := prepareShape(t, r, d, mediaLoader{})
	if len(parts) != 1 || parts[0].Kind != types.PartMessage || parts[0].Quote != "-71" || imageSegments(parts[0]) != 0 {
		t.Fatalf("图全没下下来应收回成一条带引用的消息：%+v", parts)
	}
	if parts[0].Body[0].Kind != types.BodyText || !strings.Contains(parts[0].Body[0].Text, r.CleanURL) {
		t.Fatalf("文字在前、带链接：%s", segKinds(parts[0].Body))
	}
	d = Build(&r, "-71", Options{}, 0)
	parts = prepareShape(t, r, d, mediaLoader{ok: map[string]bool{goldenImage(2).URL.Value: true, goldenImage(4).URL.Value: true}})
	if len(parts) != 1 || parts[0].Kind != types.PartMessage || imageSegments(parts[0]) != 2 {
		t.Fatalf("只下到 2 张应收回成一条两张图的消息：%+v", parts)
	}
	all := map[string]bool{}
	for i := 1; i <= 5; i++ {
		all[goldenImage(i).URL.Value] = true
	}
	d = Build(&r, "-71", Options{}, 0)
	if parts = prepareShape(t, r, d, mediaLoader{ok: all}); len(parts) != 1 || parts[0].Kind != types.PartForward || imageSegments(parts[0]) != 5 {
		t.Fatalf("5 张都下到照旧转发：%+v", parts)
	}
}
