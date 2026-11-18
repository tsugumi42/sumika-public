package linkexpand

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkreader"
)

// 旋转（规格-链接解析模板第七节）由发送端对已经准备好的整张静态图做：卡片是渲染好的图，封面画在里面，
// 转整张图封面就跟着转了；视频缩略图、封面单发时也是普通静态图。这里验证 Rotate180 本身对卡片可用且是真的转了 180°。

func cardBytes(t *testing.T) []byte {
	t.Helper()
	r := mallResult(t)
	pic := mediaPNG(t, color.RGBA{R: 255, A: 255})
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: mallTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(strings.NewReader(string(pic)))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	cards := linkcard.New()
	defer cards.Close()
	p := Preparer{Media: NewMedia(f, t.TempDir()), Cards: cards, Direct: types.MediaCaps{Image: true, ImageBase64: true},
		Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
	d := Build(&r, "-71", goldenOptions("card"), 0)
	prepared, err := p.Prepare(context.Background(), &r, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	seg := prepared.Parts[0].Body[0]
	if seg.Kind != types.BodyImage {
		t.Fatalf("第一段应是卡片图：%+v", seg)
	}
	var raw []byte
	if err := seg.Media.Read(func(r io.Reader) error { var e error; raw, e = io.ReadAll(r); return e }); err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodePNG(t *testing.T, raw []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func readAll(t *testing.T, res *types.MediaResource) []byte {
	t.Helper()
	var raw []byte
	if err := res.Read(func(r io.Reader) error { var e error; raw, e = io.ReadAll(r); return e }); err != nil {
		t.Fatal(err)
	}
	return raw
}

// 卡片整张图转 180°：左上角像素等于原图右下角，再转一次回到原样；尺寸不变
func TestRotate180TurnsWholeCard(t *testing.T) {
	plain := cardBytes(t)
	orig := types.MemoryMedia("card", "image/png", 0, 0, plain)
	turned, err := Rotate180(orig)
	if err != nil {
		t.Fatal(err)
	}
	if turned.MIME != "image/png" {
		t.Fatal("PNG 应保持 PNG", turned.MIME)
	}
	raw := readAll(t, turned)
	if bytes.Equal(plain, raw) {
		t.Fatal("旋转后字节和原来一样，被拒的卡片会原样再被拒")
	}
	a, b := decodePNG(t, plain), decodePNG(t, raw)
	if a.Bounds() != b.Bounds() {
		t.Fatal("旋转不该改尺寸", a.Bounds(), b.Bounds())
	}
	w, h := a.Bounds().Dx(), a.Bounds().Dy()
	for _, pt := range [][2]int{{0, 0}, {w - 1, h - 1}, {w / 3, h / 4}, {w - 5, 7}} {
		r1, g1, b1, a1 := a.At(pt[0], pt[1]).RGBA()
		r2, g2, b2, a2 := b.At(w-1-pt[0], h-1-pt[1]).RGBA()
		if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
			t.Fatalf("(%d,%d) 应转到 (%d,%d)", pt[0], pt[1], w-1-pt[0], h-1-pt[1])
		}
	}
	again, err := Rotate180(turned)
	if err != nil {
		t.Fatal(err)
	}
	c := decodePNG(t, readAll(t, again))
	for y := 0; y < h; y += 7 {
		for x := 0; x < w; x += 7 {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := c.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 {
				t.Fatalf("转两次应回到原样，(%d,%d) 不同", x, y)
			}
		}
	}
	// 原资源不动：调用方负责释放，转之后仍可读
	if _, err := Rotate180(orig); err != nil {
		t.Fatal("原资源应保持可读", err)
	}
}

// JPEG 保持 JPEG；动图、WebP、坏数据不转，返回错误（调用方据此跳过这一形式并写明原因）
func TestRotate180FormatsAndFailures(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, img, nil); err != nil {
		t.Fatal(err)
	}
	if out, err := Rotate180(types.MemoryMedia("j", "image/jpeg", 4, 2, jb.Bytes())); err != nil || out.MIME != "image/jpeg" || out.Width != 4 || out.Height != 2 {
		t.Fatalf("JPEG：%+v %v", out, err)
	}
	var gb bytes.Buffer
	if err := gif.Encode(&gb, img, nil); err != nil {
		t.Fatal(err)
	}
	for name, res := range map[string]*types.MediaResource{
		"GIF":   types.MemoryMedia("g", "image/gif", 4, 2, gb.Bytes()),
		"坏数据":   types.MemoryMedia("x", "image/png", 1, 1, []byte{1, 2, 3}),
		"空资源":   nil,
		"已释放资源": released(),
	} {
		if out, err := Rotate180(res); err == nil || out != nil {
			t.Fatalf("%s 不该转成功", name)
		}
	}
}

func released() *types.MediaResource {
	r := types.MemoryMedia("r", "image/png", 1, 1, []byte{1})
	_ = r.Release()
	return r
}
