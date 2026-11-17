package linkexpand

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"math/rand"
	"testing"
)

// 全部是程序生成的假图：长图 = 几个低频二维正弦叠加再加渐变，切条后条条不同（单纯渐变切条会被误当变体集），
// 局部又平滑，真接缝的差很小。不放任何真实图片。

type wave struct{ fx, fy, ph, amp float64 }

func waves(seed int64) [3][]wave {
	rng := rand.New(rand.NewSource(seed))
	var out [3][]wave
	for c := range out {
		for range 5 {
			out[c] = append(out[c], wave{fx: 2 + rng.Float64()*9, fy: 1 + rng.Float64()*6, ph: rng.Float64() * 2 * math.Pi, amp: 20 + rng.Float64()*30})
		}
	}
	return out
}

// texturedBanner 画一张 w×h 的“横幅”：u、v 是 0..1 的归一化坐标，所以按比例缩小后图案不变。
func texturedBanner(w, h int, seed int64) *image.NRGBA {
	ws := waves(seed)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		v := float64(y) / float64(h)
		for x := range w {
			u := float64(x) / float64(w)
			var px [3]uint8
			for c := range 3 {
				val := 128 + 40*(u-0.5)*float64(c-1)
				for _, wv := range ws[c] {
					val += wv.amp * math.Sin(2*math.Pi*(wv.fx*u+wv.fy*v)+wv.ph)
				}
				px[c] = uint8(max(0, min(255, val)))
			}
			img.SetNRGBA(x, y, color.NRGBA{px[0], px[1], px[2], 255})
		}
	}
	return img
}

// slice 按给定宽度从左到右切成竖条（拷贝成独立的图）。
func slice(src *image.NRGBA, widths ...int) []image.Image {
	var out []image.Image
	x := 0
	for _, w := range widths {
		strip := image.NewNRGBA(image.Rect(0, 0, w, src.Bounds().Dy()))
		draw.Draw(strip, strip.Bounds(), src, image.Pt(x, 0), draw.Src)
		out = append(out, strip)
		x += w
	}
	return out
}

func fill(img *image.NRGBA, c color.Color) {
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
}

// variantSet 做 n 张同底图的变体：底图相同，只有中间一小块（脸）不同。base 为空用纯色底。
func variantSet(base *image.NRGBA, w, h, n int) []image.Image {
	var out []image.Image
	for i := range n {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		if base == nil {
			fill(img, color.NRGBA{235, 235, 235, 255})
		} else {
			draw.Draw(img, img.Bounds(), base, image.Point{}, draw.Src)
		}
		// 身体：每张一样；脸：每张换个颜色和位置偏移一点点
		draw.Draw(img, image.Rect(w/4, h/3, 3*w/4, h), image.NewUniform(color.NRGBA{60, 90, 160, 255}), image.Point{}, draw.Src)
		face := image.Rect(w*2/5, h/6, w*3/5, h/6+h/6)
		draw.Draw(img, face, image.NewUniform(color.NRGBA{uint8(200 + 10*i), uint8(150 - 20*i), 120, 255}), image.Point{}, draw.Src)
		out = append(out, img)
	}
	return out
}

// lineArtDiff 是白底线稿的差分：同样的几条线，每张多或少一两笔。
func lineArtDiff(w, h, n int) []image.Image {
	var out []image.Image
	for i := range n {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		fill(img, color.White)
		black := image.NewUniform(color.Black)
		draw.Draw(img, image.Rect(w/4, h/5, w/4+3, 4*h/5), black, image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(w/4, h/5, 3*w/4, h/5+3), black, image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(3*w/4-3, h/5, 3*w/4, 4*h/5), black, image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(w/2-1+5*i, h/3, w/2+2+5*i, h/2), black, image.Point{}, draw.Src) // 每张多一笔的位置不同
		out = append(out, img)
	}
	return out
}

func TestPanoramaPositiveCuts(t *testing.T) {
	// 真实样本的比例：2240、1077、1077、1078 缩到 1/4（总宽 1368，高 568）
	banner := texturedBanner(1368, 568, 1)
	cases := map[string][]image.Image{
		"真实比例（第一张占两格）": slice(banner, 560, 269, 269, 270),
		"等宽四条":         slice(banner, 342, 342, 342, 342),
		"两条":           slice(banner, 700, 668),
		"六条等宽":         slice(texturedBanner(1200, 400, 7), 200, 200, 200, 200, 200, 200),
	}
	for name, imgs := range cases {
		v := analyzePanorama(imgs)
		t.Logf("%s：%s", name, v)
		if !v.OK {
			t.Errorf("%s 应当认作拆开的横幅：%s", name, v)
		}
	}
}

func TestPanoramaNegatives(t *testing.T) {
	banner := texturedBanner(1368, 568, 1)
	strips := slice(banner, 342, 342, 342, 342)
	base := texturedBanner(340, 568, 3)
	cases := []struct {
		name, reason string
		imgs         []image.Image
	}{
		{"纯色背景的变体集", "", variantSet(nil, 400, 568, 4)},
		{"只改表情的变体集（有纹理底图）", "variant", variantSet(base, 340, 568, 3)},
		{"白底线稿差分", "", lineArtDiff(400, 568, 3)},
		{"几张不相干的同高图", "seam", []image.Image{texturedBanner(300, 568, 11), texturedBanner(300, 568, 12), texturedBanner(300, 568, 13)}},
		{"顺序颠倒", "seam", []image.Image{strips[1], strips[0], strips[2], strips[3]}},
		{"中间少了一条", "seam", []image.Image{strips[0], strips[2], strips[3]}},
		{"高度差太多", "height", []image.Image{strips[0], texturedBanner(342, 560, 1)}},
		{"只有一张", "too_few", []image.Image{strips[0]}},
	}
	for _, c := range cases {
		v := analyzePanorama(c.imgs)
		t.Logf("%s：%s", c.name, v)
		if v.OK {
			t.Errorf("%s 不该拼：%s", c.name, v)
		}
		if c.reason != "" && v.Reason != c.reason {
			t.Errorf("%s 的原因应是 %s，实际 %s", c.name, c.reason, v.Reason)
		}
	}
}

// jpegTrip 把每张图过一遍 JPEG（质量 q）再解码：真实的拆图是各自重新压缩的，缝两边会有压缩噪声。
func jpegTrip(t *testing.T, imgs []image.Image, q int) []image.Image {
	t.Helper()
	var out []image.Image
	for _, im := range imgs {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, im, &jpeg.Options{Quality: q}); err != nil {
			t.Fatal(err)
		}
		back, err := jpeg.Decode(&buf)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, back)
	}
	return out
}

// 各自重新压缩过的竖条仍认得出（JPEG 质量 70～92）。
func TestPanoramaSurvivesJPEG(t *testing.T) {
	banner := texturedBanner(1368, 568, 5)
	for _, q := range []int{70, 85, 92} {
		v := analyzePanorama(jpegTrip(t, slice(banner, 560, 269, 269, 270), q))
		t.Logf("质量 %d：%s", q, v)
		if !v.OK {
			t.Errorf("质量 %d 的竖条应当认出：%s", q, v)
		}
	}
	for _, q := range []int{70, 92} {
		if v := analyzePanorama(jpegTrip(t, variantSet(texturedBanner(340, 568, 3), 340, 568, 3), q)); v.OK {
			t.Errorf("质量 %d 的变体集不该拼：%s", q, v)
		}
	}
}

// 接缝落在大片留白里：两边都是白的，接得“完美”也不算证据；各自的整体并不像（dHash 远），所以挡下的是信息量这一条。
func TestPanoramaFlatSeamIsNotEvidence(t *testing.T) {
	banner := texturedBanner(1368, 568, 9)
	// 中间 120 列刷成白色，从白色正中切开
	draw.Draw(banner, image.Rect(624, 0, 744, 568), image.NewUniform(color.White), image.Point{}, draw.Src)
	v := analyzePanorama(slice(banner, 684, 684))
	t.Log(v)
	if v.OK || v.Reason != "flat_edge" {
		t.Fatalf("留白里的接缝应当 flat_edge: %s", v)
	}
}
