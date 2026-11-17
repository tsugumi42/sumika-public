package main

// 第八步的测试：认格式（sniffAnim）、T-reg（加了 pngn 和 vpx 以后注册撞车）、动图走静态路径时给首帧。
// 样本全是现造的，见 anim_samples_test.go。

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vpxwebp "github.com/gen2brain/vpx/webp"
	xwebp "golang.org/x/image/webp"
)

func TestSniffAnim(t *testing.T) {
	t.Parallel()
	three := []image.Image{solid(8, 8, cRed), solid(8, 8, cGreen), solid(8, 8, cBlue)}
	cases := []struct {
		name string
		data []byte
		kind animKind
		n    int
	}{
		{"三帧 GIF", gifSample(t, 8, 8, []colorN{cRed, cGreen, cBlue}, []int{5, 5, 5}, 0), animGIF, 3},
		{"一帧 GIF 还是 GIF，帧数 1", gifSample(t, 8, 8, []colorN{cRed}, []int{5}, 0), animGIF, 1},
		{"三帧 APNG", apngSample(t, nil, three, []int{50, 50, 50}, 0), animAPNG, 3},
		{"默认图不在动画里的 APNG", apngSample(t, solid(8, 8, cBlue), three[:2], []int{50, 50}, 0), animAPNG, 2},
		{"普通 PNG 不是动图", facePNG(8, 8, red), animNone, 0},
		{"三帧动态 WebP", webpSample(t, three, []int{50, 50, 50}, 0), animWebP, 3},
		{"静态 WebP 不是动图", staticWebP(t), animNone, 0},
		{"JPEG", jpegBytes(8, 8, 0), animNone, 0},
		{"空", nil, animNone, 0},
		{"只有 PNG 签名", []byte("\x89PNG\r\n\x1a\n"), animNone, 0},
		{"只有 GIF 头", []byte("GIF89a\x01"), animGIF, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, n := sniffAnim(c.data)
			if k != c.kind || n != c.n {
				t.Fatalf("认成 %v、%d 帧，要 %v、%d 帧", k, n, c.kind, c.n)
			}
		})
	}
	// 截断、乱写：不 panic（每个前缀都试一遍）
	for _, data := range [][]byte{gifSample(t, 8, 8, []colorN{cRed, cGreen}, []int{5, 5}, 0), apngSample(t, nil, three, []int{50, 50, 50}, 0), webpSample(t, three, []int{50, 50, 50}, 0)} {
		for i := range data {
			sniffAnim(data[:i])
		}
	}
}

func staticWebP(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := vpxwebp.Encode(&buf, solid(16, 16, cRed), vpxwebp.EncodeOptions{Lossless: true}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 动图走静态路径（?w= 的响应、预判错了的老数据）：APNG 给默认图，动态 WebP 给第一帧，GIF 给第一帧
func TestDecodeStaticOfAnimatedGivesFirstFrame(t *testing.T) {
	three := []image.Image{solid(8, 8, cRed), solid(8, 8, cGreen), solid(8, 8, cBlue)}
	cases := []struct {
		name string
		data []byte
		want colorN
	}{
		{"APNG：默认图就是第一帧", apngSample(t, nil, three, []int{50, 50, 50}, 0), cRed},
		{"APNG：默认图不属于动画，要默认图不是动画第一帧", apngSample(t, solid(8, 8, cBlue), three[:2], []int{50, 50}, 0), cBlue},
		{"动态 WebP：x/image/webp 不认 ANMF，改用 vpx 给第一帧", webpSample(t, three, []int{50, 50, 50}, 0), cRed},
		{"GIF：第一帧", gifSample(t, 8, 8, []colorN{cGreen, cRed}, []int{5, 5}, 0), cGreen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := decodeStatic(c.data, 768)
			if err != nil {
				t.Fatalf("解不开：%v", err)
			}
			if w, h := b.Size(); w != 8 || h != 8 {
				t.Fatalf("位图 %d×%d，要 8×8", w, h)
			}
			if got := bitmapPixel(b, 4, 4); !near(got, toRGBA(c.want), 2) {
				t.Fatalf("中心像素 %v，要 %v", got, toRGBA(c.want))
			}
		})
	}
}

// T-reg (b)：导入 pngn、vpx 以后 image.Decode 对 WebP 会换人（证明注册确实撞了车）；
// 消息路径（decodeStatic）对同一张静态 PNG 和 WebP 的结果，与 png.Decode、x/image/webp 逐像素相同
func TestRegistrationCollision(t *testing.T) {
	// 有损 WebP：vpx 解出 *image.NYCbCrA，x/image/webp 解出 *image.YCbCr，类型不同
	var lossy bytes.Buffer
	if err := vpxwebp.Encode(&lossy, solid(32, 32, cRed), vpxwebp.EncodeOptions{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	viaImage, format, err := image.Decode(bytes.NewReader(lossy.Bytes()))
	if err != nil || format != "webp" {
		t.Fatalf("image.Decode 解有损 WebP：%v、格式 %q", err, format)
	}
	viaX, err := xwebp.Decode(bytes.NewReader(lossy.Bytes()))
	if err != nil {
		t.Fatalf("x/image/webp 解有损 WebP：%v", err)
	}
	viaVpx, err := vpxwebp.Decode(bytes.NewReader(lossy.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if typeName(viaImage) != typeName(viaVpx) || typeName(viaImage) == typeName(viaX) {
		t.Fatalf("image.Decode 给 %s，vpx 给 %s，x/image/webp 给 %s：注册没有换人，T-reg 的前提不成立了，要重新核对",
			typeName(viaImage), typeName(viaVpx), typeName(viaX))
	}

	// 消息路径的结果与各自的包逐像素相同
	b, err := decodeStatic(lossy.Bytes(), 768)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bitmapPixel(b, 16, 16), imagePixel(viaX, 16, 16); !near(got, want, 3) {
		t.Fatalf("静态 WebP 中心像素 %v，x/image/webp 是 %v", got, want)
	}
	pngData := facePNG(20, 10, blue)
	want, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	pb, err := decodeStatic(pngData, 768)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]int{{0, 0}, {10, 5}, {19, 9}} {
		if got, w := bitmapPixel(pb, p[0], p[1]), imagePixel(want, p[0], p[1]); !near(got, w, 1) {
			t.Fatalf("PNG (%d,%d) 是 %v，png.Decode 是 %v", p[0], p[1], got, w)
		}
	}
}

// T-reg (a)：native/ 下消息字节的解码路径不调 image.Decode；ui.DecodeBitmap 只许 images.go 的 decodeStatic 里那两处
// （JPEG 不大于 2048、BMP 这类，前面的分支已经把 PNG 和 WebP 拿走了）。扫源码，不依赖 go vet
func TestNoImageDecodeInMessagePath(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("读不到源码：%v", err)
	}
	var bitmapSites []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") {
				continue
			}
			if strings.Contains(code, "image.Decode(") {
				t.Errorf("%s:%d 调了 image.Decode：加了 pngn 和 vpx 以后它对 PNG、WebP 会换人", f, i+1)
			}
			if strings.Contains(code, "ui.DecodeBitmap(") {
				bitmapSites = append(bitmapSites, f)
			}
		}
	}
	if len(bitmapSites) != 2 || bitmapSites[0] != "images.go" || bitmapSites[1] != "images.go" {
		t.Errorf("ui.DecodeBitmap 的调用点 %v，只许 images.go 里两处", bitmapSites)
	}
}
