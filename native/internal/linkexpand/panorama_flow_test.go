package linkexpand

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 按地址回 JPEG 字节的假下载器（图是程序生成的竖条）。
type stripLoader struct {
	data  map[string][]byte
	calls []string
	made  []*types.MediaResource
}

func (s *stripLoader) Image(_ context.Context, _ string, src linkreader.Media) (Loaded, error) {
	s.calls = append(s.calls, src.URL.Value)
	b, ok := s.data[src.URL.Value]
	if !ok {
		return Loaded{}, errors.New("剧本里没有这个地址")
	}
	res := types.MemoryMedia("x", "image/jpeg", 1, 1, append([]byte(nil), b...))
	s.made = append(s.made, res)
	return Loaded{Resource: res, Kind: types.BodyImage}, nil
}
func (*stripLoader) Video(context.Context, string, *linkreader.Result, linkreader.Media, VideoLimits, bool) (Loaded, error) {
	return Loaded{}, errors.New("不该下载视频")
}
func (*stripLoader) Ugoira(context.Context, *linkreader.UgoiraInfo) (Loaded, error) {
	return Loaded{}, errors.New("不该下载动图")
}

func jpegBytes(t *testing.T, im image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, im, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// splitPost 造一条 X 帖子：len(imgs) 张图，宽高写进取数结果，同时给了 fx 的拼图（验证候选时不用它）。
func splitPost(t *testing.T, imgs []image.Image) (linkreader.Result, *stripLoader) {
	t.Helper()
	r := withMosaic(xImages(len(imgs)))
	l := &stripLoader{data: map[string][]byte{}}
	for i, im := range imgs {
		b := im.Bounds()
		r.Media.Value[i].Width, r.Media.Value[i].Height = av(b.Dx()), av(b.Dy())
		l.data[r.Media.Value[i].URL.Value] = jpegBytes(t, im)
	}
	return r, l
}

func testPreparer(l MediaLoader) *Preparer {
	caps := types.MediaCaps{Image: true, ImageBase64: true, ForwardImage: true}
	return &Preparer{Media: l, Direct: caps, Forward: caps, Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
}

func imageSegments(p types.Part) (n int) {
	count := func(body []types.BodySegment) {
		for _, s := range body {
			if s.Kind == types.BodyImage {
				n++
			}
		}
	}
	count(p.Body)
	for _, node := range p.Nodes {
		count(node.Body)
	}
	return n
}

func TestBuildPanoramaCandidateSkipsMosaic(t *testing.T) {
	banner := texturedBanner(1368, 568, 1)
	r, _ := splitPost(t, slice(banner, 560, 269, 269, 270))
	d := build(r, Options{})
	var kinds []string
	var first Item
	for _, it := range d.Parts[0].Body {
		kinds = append(kinds, it.Kind)
		if it.PanoramaN > 0 {
			first = it
		}
		if it.Media.URL.Value == r.Mosaic.Value.URL.Value {
			t.Fatal("候选时不该用 fx 的拼图")
		}
	}
	if first.PanoramaN != 4 || len(d.Parts) != 1 {
		t.Fatalf("第一张图应带 PanoramaN=4，且不附原图: %v parts=%d", kinds, len(d.Parts))
	}
	// 旧代码对这条帖子发 fx 拼图；高度差太多的不是候选，仍是拼图
	other, _ := splitPost(t, []image.Image{banner.SubImage(image.Rect(0, 0, 300, 568)), banner.SubImage(image.Rect(0, 0, 300, 560)), banner.SubImage(image.Rect(0, 0, 300, 568))})
	od := build(other, Options{Templates: map[string]string{"x": xMosaicTpl}}) // 内置默认不用拼图，写了 $拼图 的模板才用
	for _, it := range od.Parts[0].Body {
		if it.PanoramaN != 0 {
			t.Fatal("高度差太多不该是候选")
		}
	}
	if n := countMosaic(od, other); n != 1 {
		t.Fatalf("非候选应仍发 fx 拼图，实际 %d 处", n)
	}
}

func countMosaic(d Draft, r linkreader.Result) int {
	n := 0
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Media.URL.Value == r.Mosaic.Value.URL.Value {
				n++
			}
		}
	}
	return n
}

func TestPreparePanoramaSendsOneStitchedImage(t *testing.T) {
	banner := texturedBanner(1368, 568, 1)
	r, l := splitPost(t, slice(banner, 560, 269, 269, 270))
	d := build(r, Options{})
	var notes []string
	p := testPreparer(l)
	p.Notice = func(s string) { notes = append(notes, s) }
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if len(prepared.Parts) != 1 || imageSegments(prepared.Parts[0]) != 1 {
		t.Fatalf("应只发拼好的一张: parts=%d", len(prepared.Parts))
	}
	var w, h int
	for _, s := range prepared.Parts[0].Body {
		if s.Kind == types.BodyImage {
			w, h = s.Media.Width, s.Media.Height
		}
	}
	if w != 1368 || h != 568 {
		t.Fatalf("拼好的尺寸应是 1368×568，实际 %d×%d", w, h)
	}
	if len(l.calls) != 4 {
		t.Fatalf("每张图只下载一次: %v", l.calls)
	}
	for i, m := range l.made {
		if m.Available() {
			t.Fatalf("不附原图时第 %d 张原图应已释放", i)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "通过") {
		t.Fatalf("应记一条判断结果: %v", notes)
	}
}

func TestPrepareNotSeamlessSendsPlainImages(t *testing.T) {
	// 高度一样但互不相干：按普通多图发，且不重复下载
	imgs := []image.Image{texturedBanner(300, 568, 21), texturedBanner(300, 568, 22), texturedBanner(300, 568, 23)}
	r, l := splitPost(t, imgs)
	d := build(r, Options{PanoramaOriginals: true})
	prepared, err := testPreparer(l).Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	total := 0
	for _, part := range prepared.Parts {
		total += imageSegments(part)
	}
	if len(prepared.Parts) != 1 || total != 3 || len(l.calls) != 3 {
		t.Fatalf("应是一条消息三张原图（没拼成就不再附转发），实际 parts=%d 图=%d 下载=%v", len(prepared.Parts), total, l.calls)
	}
}

func TestPreparePanoramaOriginalsInForward(t *testing.T) {
	banner := texturedBanner(1368, 568, 1)
	r, l := splitPost(t, slice(banner, 342, 342, 342, 342))
	d := build(r, Options{PanoramaOriginals: true})
	if len(d.Parts) != 2 || d.Parts[1].Kind != types.PartForward || len(d.Parts[1].Nodes) != 1 || len(d.Parts[1].Nodes[0].Body) != 4 {
		t.Fatalf("开了附原图：消息后面应有一个合并转发装四张原图: %+v", d.Parts)
	}
	prepared, err := testPreparer(l).Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if len(prepared.Parts) != 2 || imageSegments(prepared.Parts[0]) != 1 || prepared.Parts[1].Kind != types.PartForward || imageSegments(prepared.Parts[1]) != 4 {
		t.Fatalf("应是一张拼图 + 转发里四张原图: %d", len(prepared.Parts))
	}
	if len(l.calls) != 4 {
		t.Fatalf("原图不该重复下载: %v", l.calls)
	}
}

func TestPreparePanoramaTimeoutFallsBackToPlain(t *testing.T) {
	banner := texturedBanner(1368, 568, 1)
	r, l := splitPost(t, slice(banner, 342, 342, 342, 342))
	d := build(r, Options{})
	p := testPreparer(l)
	p.PanoramaTimeout = time.Nanosecond
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if len(prepared.Parts) != 1 || imageSegments(prepared.Parts[0]) != 4 {
		t.Fatalf("超时应按普通四张发: %d", imageSegments(prepared.Parts[0]))
	}
}

func TestEncodePanoramaShrinksToLimit(t *testing.T) {
	big := texturedBanner(1600, 900, 4)
	full, _, _, _, err := encodePanorama(big, false, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(len(full) / 2)
	data, _, w, h, err := encodePanorama(big, false, limit)
	if err != nil || int64(len(data)) > limit || w >= 1600 || h >= 900 {
		t.Fatalf("应缩小到限内: %d/%d %dx%d err=%v", len(data), limit, w, h, err)
	}
	if _, _, _, _, err = encodePanorama(big, false, 10); !errors.Is(err, linkreader.ErrMediaLimit) {
		t.Fatalf("缩不到限内应报超限: %v", err)
	}
}
