package main

// 第九步的测试：T-gif、T-apng、T-webp、T-play（帧号）、T-budget。样本全是现造的，见 anim_samples_test.go。
// 帧的像素直接从 animation.frames 里取（RGBA，预乘），不经过 GPU。

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"testing"
	"time"

	vpxwebp "github.com/gen2brain/vpx/webp"
)

var (
	opaqueRed    = color.RGBA{255, 0, 0, 255}
	opaqueGreen  = color.RGBA{0, 255, 0, 255}
	opaqueBlue   = color.RGBA{0, 0, 255, 255}
	opaquePurple = color.RGBA{128, 0, 128, 255}
	clear0       = color.RGBA{}
)

// 所有帧同尺寸、原点 (0,0)、步长 4×宽：Bitmap.Update 才不拷贝、能复用纹理
func checkFrameShape(t *testing.T, an *animation, w, h int) {
	t.Helper()
	for i, f := range an.frames {
		if f.Rect != image.Rect(0, 0, w, h) || f.Stride != 4*w {
			t.Fatalf("第 %d 帧 Rect=%v Stride=%d，要 %dx%d、步长 %d", i, f.Rect, f.Stride, w, h, 4*w)
		}
	}
}

func mustAnim(t *testing.T, data []byte, tw int) *animation {
	t.Helper()
	an, still, err := decodeAnimated(data, tw)
	if err != nil {
		t.Fatalf("解不开：%v", err)
	}
	if an == nil {
		t.Fatalf("没解成动图（退到了静态，位图 %v）", still)
	}
	if still == nil {
		t.Fatal("动图应当同时给首帧位图")
	}
	return an
}

// ───────────── T-gif ─────────────

func TestGIFThreeFrames(t *testing.T) {
	t.Parallel()
	an := mustAnim(t, gifSample(t, 8, 6, []colorN{cRed, cGreen, cBlue}, []int{5, 10, 0}, 0), 768)
	checkFrameShape(t, an, 8, 6)
	for i, want := range []color.RGBA{opaqueRed, opaqueGreen, opaqueBlue} {
		if got := rgbaAt(an.frames[i], 3, 3); got != want {
			t.Fatalf("第 %d 帧中心 %v，要 %v", i, got, want)
		}
	}
	// 百分之一秒乘 10；0 按 100 毫秒；LoopCount 0 无限
	if want := []int{50, 100, 100}; !equalInts(an.delays, want) {
		t.Fatalf("帧时长 %v，要 %v", an.delays, want)
	}
	if an.plays != 0 {
		t.Fatalf("播 %d 遍，要无限（0）", an.plays)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 没有 NETSCAPE 块（LoopCount -1）只播一次，停在末帧；LoopCount n 播 n+1 遍
func TestGIFLoopCounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		loop  int
		plays int
	}{{"没有 NETSCAPE 块", -1, 1}, {"无限", 0, 0}, {"重复 2 次", 2, 3}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			an := mustAnim(t, gifSample(t, 4, 4, []colorN{cRed, cGreen}, []int{10, 10}, c.loop), 768)
			if an.plays != c.plays {
				t.Fatalf("播 %d 遍，要 %d", an.plays, c.plays)
			}
		})
	}
	an := mustAnim(t, gifSample(t, 4, 4, []colorN{cRed, cGreen}, []int{10, 10}, -1), 768)
	if i, wait := an.frameAt(10 * time.Second); i != 1 || wait != 0 {
		t.Fatalf("播完后第 %d 帧、还等 %v，要停在末帧（1）不再等", i, wait)
	}
}

// 处置方式 1（不动）、2（清成透明）、3（恢复画之前的），帧只占画布的一块。与手算的像素逐一比较
func TestGIFDisposalAndPartialFrames(t *testing.T) {
	t.Parallel()
	pal := func(c color.Color) color.Palette { return color.Palette{color.NRGBA{}, c} }
	filled := func(r image.Rectangle, c color.Color) *image.Paletted {
		p := image.NewPaletted(r, pal(c))
		for i := range p.Pix {
			p.Pix[i] = 1
		}
		return p
	}
	g := &gif.GIF{
		Config:    image.Config{ColorModel: color.Palette{color.NRGBA{}, cRed}, Width: 4, Height: 4},
		LoopCount: 0,
		Image: []*image.Paletted{
			filled(image.Rect(0, 0, 4, 4), cRed),                          // 0：整屏红，处置 1
			filled(image.Rect(1, 1, 3, 3), cBlue),                         // 1：中间 2×2 蓝，处置 2（画完清成透明）
			filled(image.Rect(0, 0, 1, 1), cGreen),                        // 2：左上角绿，处置 3（画完恢复）
			filled(image.Rect(3, 3, 4, 4), color.NRGBA{128, 0, 128, 255}), // 3：右下角紫，处置 0
		},
		Delay:    []int{5, 5, 5, 5},
		Disposal: []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious, gif.DisposalNone},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	an := mustAnim(t, buf.Bytes(), 768)
	checkFrameShape(t, an, 4, 4)
	type px struct {
		x, y int
		want color.RGBA
	}
	cases := [][]px{
		{{0, 0, opaqueRed}, {3, 3, opaqueRed}},                                         // 帧 0
		{{0, 0, opaqueRed}, {1, 1, opaqueBlue}, {2, 2, opaqueBlue}, {3, 3, opaqueRed}}, // 帧 1：蓝压在红上
		{{0, 0, opaqueGreen}, {1, 1, clear0}, {2, 2, clear0}, {3, 3, opaqueRed}},       // 帧 2：上一帧的蓝已清成透明，左上角绿
		{{0, 0, opaqueRed}, {1, 1, clear0}, {3, 3, opaquePurple}, {2, 3, opaqueRed}},   // 帧 3：绿已恢复成红，右下角紫
	}
	for i, ps := range cases {
		for _, p := range ps {
			if got := rgbaAt(an.frames[i], p.x, p.y); got != p.want {
				t.Errorf("第 %d 帧 (%d,%d) 是 %v，要 %v", i, p.x, p.y, got, p.want)
			}
		}
	}
}

// 逻辑屏幕比帧小（帧超出画布）：标准库的 gif.DecodeAll 直接报"frame bounds larger than image bounds"，
// 我们跟着打不开（浏览器会容忍，这种文件很少见；以后遇到真的再放宽）。要紧的是不 panic、不给半截动图
func TestGIFFrameBeyondScreen(t *testing.T) {
	t.Parallel()
	data := gifSample(t, 8, 8, []colorN{cRed, cGreen}, []int{5, 5}, 0)
	binary.LittleEndian.PutUint16(data[6:], 4) // 逻辑屏幕改成 4×4
	binary.LittleEndian.PutUint16(data[8:], 4)
	an, _, err := decodeAnimated(data, 768)
	if an != nil || err == nil {
		t.Fatalf("动图 %v、错误 %v，要打不开", an, err)
	}
}

// 截断（没有 trailer、少一截）：解不成动图，也不 panic；能显示的首帧才退到静态，GIF 的静态解码同样要整个读完，所以这里是打不开
func TestGIFTruncated(t *testing.T) {
	t.Parallel()
	data := gifSample(t, 8, 8, []colorN{cRed, cGreen, cBlue}, []int{5, 5, 5}, 0)
	for _, cut := range []int{len(data) - 1, len(data) - 20, len(data) / 2} {
		an, _, err := decodeAnimated(data[:cut], 768)
		if an != nil {
			t.Fatalf("截到 %d 字节还解成了动图", cut)
		}
		_ = err // 打不开或退到首帧都行，要紧的是不 panic、不给半截动图
	}
}

// 一帧的 GIF 是静态图：不当动图
func TestGIFSingleFrameIsStatic(t *testing.T) {
	t.Parallel()
	an, b, err := decodeAnimated(gifSample(t, 8, 8, []colorN{cRed}, []int{5}, 0), 768)
	if err != nil || an != nil || b == nil {
		t.Fatalf("单帧：动图 %v、位图 %v、错误 %v，要无动图、有位图", an, b, err)
	}
}

// 帧缩到宽 tw：所有帧同尺寸，比例不变；不比 tw 宽的不放大
func TestAnimScaledToTarget(t *testing.T) {
	t.Parallel()
	an := mustAnim(t, gifSample(t, 400, 200, []colorN{cRed, cGreen}, []int{5, 5}, 0), 100)
	checkFrameShape(t, an, 100, 50)
	an = mustAnim(t, gifSample(t, 40, 20, []colorN{cRed, cGreen}, []int{5, 5}, 0), 100)
	checkFrameShape(t, an, 40, 20)
}

// ───────────── T-apng ─────────────

func TestAPNG(t *testing.T) {
	three := []image.Image{solid(8, 6, cRed), solid(8, 6, cGreen), solid(8, 6, cBlue)}
	t.Run("默认图就是动画第一帧", func(t *testing.T) {
		an := mustAnim(t, apngSample(t, nil, three, []int{50, 20, 0}, 0), 768)
		checkFrameShape(t, an, 8, 6)
		for i, want := range []color.RGBA{opaqueRed, opaqueGreen, opaqueBlue} {
			if got := rgbaAt(an.frames[i], 4, 3); got != want {
				t.Fatalf("第 %d 帧 %v，要 %v", i, got, want)
			}
		}
		// 毫秒；0 按 100；num_plays 0 无限
		if !equalInts(an.delays, []int{50, 20, 100}) || an.plays != 0 {
			t.Fatalf("帧时长 %v、播 %d 遍", an.delays, an.plays)
		}
	})
	t.Run("默认图不属于动画：动画只有 fdAT 那几帧", func(t *testing.T) {
		an := mustAnim(t, apngSample(t, solid(8, 6, color.NRGBA{255, 255, 0, 255}), three[:2], []int{50, 50}, 3), 768)
		if len(an.frames) != 2 || an.plays != 3 {
			t.Fatalf("%d 帧、播 %d 遍，要 2 帧、3 遍", len(an.frames), an.plays)
		}
		if got := rgbaAt(an.frames[0], 1, 1); got != opaqueRed {
			t.Fatalf("第一帧 %v，要红（默认图的黄不该进动画）", got)
		}
	})
	t.Run("16 位", func(t *testing.T) {
		f16 := func(c color.NRGBA64) image.Image {
			img := image.NewNRGBA64(image.Rect(0, 0, 8, 6))
			for y := 0; y < 6; y++ {
				for x := 0; x < 8; x++ {
					img.SetNRGBA64(x, y, c)
				}
			}
			return img
		}
		an := mustAnim(t, apngSample(t, nil, []image.Image{f16(color.NRGBA64{65535, 0, 0, 65535}), f16(color.NRGBA64{0, 65535, 0, 65535})}, []int{50, 50}, 0), 768)
		if got := rgbaAt(an.frames[1], 2, 2); got != opaqueGreen {
			t.Fatalf("16 位第二帧 %v，要绿", got)
		}
	})
	t.Run("acTL 声明的帧数和实际不符：整体报错，退到默认图", func(t *testing.T) {
		data := apngSample(t, nil, three[:2], []int{50, 50}, 0)
		i := bytes.Index(data, []byte("acTL"))
		binary.BigEndian.PutUint32(data[i+4:], 3) // 声明 3 帧，实际 2 帧
		binary.BigEndian.PutUint32(data[i+12:], crc32.ChecksumIEEE(data[i:i+12]))
		an, b, err := decodeAnimated(data, 768)
		if err != nil || an != nil || b == nil {
			t.Fatalf("动图 %v、位图 %v、错误 %v，要无动图、有默认图的位图", an, b, err)
		}
		if got := bitmapPixel(b, 4, 3); !near(got, opaqueRed, 2) {
			t.Fatalf("默认图中心 %v，要红", got)
		}
	})
}

// ───────────── T-webp ─────────────

func TestWebPAnim(t *testing.T) {
	t.Parallel()
	three := []image.Image{solid(8, 6, cRed), solid(8, 6, cGreen), solid(8, 6, cBlue)}
	t.Run("无损：帧数、毫秒、循环数、颜色", func(t *testing.T) {
		an := mustAnim(t, webpSample(t, three, []int{40, 60, 10}, 2), 768)
		checkFrameShape(t, an, 8, 6)
		if !equalInts(an.delays, []int{40, 60, 100}) || an.plays != 2 { // 10 毫秒小于 20，抬到 100
			t.Fatalf("帧时长 %v、播 %d 遍", an.delays, an.plays)
		}
		for i, want := range []color.RGBA{opaqueRed, opaqueGreen, opaqueBlue} {
			if got := rgbaAt(an.frames[i], 4, 3); got != want {
				t.Fatalf("第 %d 帧 %v，要 %v", i, got, want)
			}
		}
	})
	t.Run("有损、带透明（ALPH 块）：透明的地方还是透明", func(t *testing.T) {
		withHole := func(c color.NRGBA) image.Image {
			img := solid(16, 16, c)
			img.SetNRGBA(0, 0, color.NRGBA{})
			return img
		}
		var buf bytes.Buffer
		err := vpxwebp.EncodeAll(&buf, &vpxwebp.WEBP{Image: []image.Image{withHole(cRed), withHole(cGreen)}, Delay: []int{50, 50}}, vpxwebp.EncodeOptions{Quality: 90})
		if err != nil {
			t.Fatal(err)
		}
		an := mustAnim(t, buf.Bytes(), 768)
		if got := rgbaAt(an.frames[0], 0, 0); got.A != 0 {
			t.Fatalf("(0,0) 是 %v，要透明", got)
		}
		if got := rgbaAt(an.frames[1], 8, 8); !near(got, opaqueGreen, 12) {
			t.Fatalf("有损第二帧中心 %v，要接近绿", got)
		}
	})
	t.Run("有损、不透明（没有 ALPH 块）", func(t *testing.T) {
		var buf bytes.Buffer
		err := vpxwebp.EncodeAll(&buf, &vpxwebp.WEBP{Image: []image.Image{solid(16, 16, cRed), solid(16, 16, cBlue)}, Delay: []int{50, 50}}, vpxwebp.EncodeOptions{Quality: 90})
		if err != nil {
			t.Fatal(err)
		}
		an := mustAnim(t, buf.Bytes(), 768)
		if got := rgbaAt(an.frames[0], 8, 8); !near(got, opaqueRed, 12) || got.A != 255 {
			t.Fatalf("有损第一帧中心 %v，要接近红且不透明", got)
		}
	})
}

// ───────────── T-play（帧号）─────────────

func TestFrameAt(t *testing.T) {
	t.Parallel()
	mk := func(plays int, delays ...int) *animation {
		return newAnimation(make([]*image.RGBA, len(delays)), delays, plays)
	}
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	cases := []struct {
		name   string
		an     *animation
		at     int // 毫秒
		idx    int
		waitMs int
	}{
		{"开头", mk(0, 100, 100, 100), 0, 0, 100},
		{"第一帧中间", mk(0, 100, 100, 100), 30, 0, 70},
		{"刚好到第二帧", mk(0, 100, 100, 100), 100, 1, 100},
		{"无限循环：转回开头", mk(0, 100, 100, 100), 310, 0, 90},
		{"无限循环：很久以后", mk(0, 100, 100, 100), 3_000_050, 0, 50},
		{"播 1 遍：播完停在末帧", mk(1, 100, 100, 100), 300, 2, 0},
		{"播 1 遍：还没播完", mk(1, 100, 100, 100), 299, 2, 1},
		{"播 3 遍：第二遍里", mk(3, 100, 100, 100), 650, 0, 50},
		{"播 3 遍：播完（切回会话时已经超过总时长），停在末帧，不重播", mk(3, 100, 100, 100), 900, 2, 0},
		{"延迟 0 按 100", mk(0, 0, 0), 150, 1, 50},
		{"延迟 19 按 100，20 就是 20", mk(0, 19, 20), 100, 1, 20},
		{"延迟不等", mk(0, 40, 100), 45, 1, 95},
		{"只有一帧：不动", mk(0, 100), 5000, 0, 0},
		{"时间倒退当 0", mk(0, 100, 100), -5, 0, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, wait := c.an.frameAt(ms(c.at))
			if idx != c.idx || wait != ms(c.waitMs) {
				t.Fatalf("第 %d 帧、还等 %v，要第 %d 帧、等 %v", idx, wait, c.idx, ms(c.waitMs))
			}
		})
	}
}

// ───────────── T-budget ─────────────

func TestAnimFitsBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		n, w, h, per, tw int
		want             bool
	}{
		// 存下来的：帧数 × 缩放后宽 × 高 × 4 ≤ 24 MiB。1024×1024，6 帧刚好 24 MiB
		{"存下来的刚好在上限", 6, 1024, 1024, 4, 1024, true},
		{"存下来的多一帧", 7, 1024, 1024, 4, 1024, false},
		{"存下来的多一个像素（高 1025）", 6, 1024, 1025, 4, 1024, false},
		// 解码瞬时：帧数 × 画布 × 每像素字节 ≤ 64 MiB。GIF 调色板帧每像素 1 字节，缩到宽 100 所以存下来的很小
		{"瞬时刚好在上限", 16, 2048, 2048, 1, 100, true},
		{"瞬时多一帧", 17, 2048, 2048, 1, 100, false},
		{"瞬时多一个像素（高 2049）", 16, 2048, 2049, 1, 100, false},
		{"瞬时按 4 字节算：同样的画布 4 帧刚好", 4, 2048, 2048, 4, 100, true},
		{"瞬时按 4 字节算：5 帧超了", 5, 2048, 2048, 4, 100, false},
		{"没有帧", 0, 10, 10, 4, 100, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := animFits(c.n, c.w, c.h, c.per, c.tw); got != c.want {
				t.Fatalf("animFits(%d 帧 %d×%d ×%d 字节 缩到 %d) = %v，要 %v", c.n, c.w, c.h, c.per, c.tw, got, c.want)
			}
		})
	}
}

// 超预算的真动图：只留首帧（动图为 nil、位图有，不报错）
func TestAnimOverBudgetKeepsFirstFrame(t *testing.T) {
	// 8 帧 1024×1024，缩到宽 1024：存下来 32 MiB > 24 MiB
	data := gifSample(t, 1024, 1024, []colorN{cRed, cGreen, cBlue, cRed, cGreen, cBlue, cRed, cGreen}, []int{5, 5, 5, 5, 5, 5, 5, 5}, 0)
	an, b, err := decodeAnimated(data, 1024)
	if err != nil || an != nil || b == nil {
		t.Fatalf("动图 %v、位图 %v、错误 %v，要无动图、有首帧位图", an, b, err)
	}
	if got := bitmapPixel(b, 10, 10); !near(got, opaqueRed, 2) {
		t.Fatalf("首帧 %v，要红", got)
	}
	// 同样的图缩小一点就放得下（6 帧 × 1024 × 1024 × 4 = 24 MiB 的边上，缩到 512 宽后只有 8 MiB）
	if an, _, err := decodeAnimated(data, 512); err != nil || an == nil || len(an.frames) != 8 {
		t.Fatalf("缩到 512 宽应该放得下：%v、%v", an, err)
	}
}

// 照片尺寸的图头声称很大：不去解
func TestAnimHeaderClaimingHuge(t *testing.T) {
	t.Parallel()
	data := gifSample(t, 8, 8, []colorN{cRed, cGreen}, []int{5, 5}, 0)
	binary.LittleEndian.PutUint16(data[6:], 65535)
	binary.LittleEndian.PutUint16(data[8:], 65535)
	if an, _, err := decodeAnimated(data, 768); an != nil || err == nil {
		t.Fatalf("65535×65535 的头：动图 %v、错误 %v，要打不开", an, err)
	}
}

// 静态 PNG 走 decodeAnimated：退到静态路径
func TestDecodeAnimatedOnStaticBytes(t *testing.T) {
	t.Parallel()
	an, b, err := decodeAnimated(facePNG(20, 10, red), 768)
	if err != nil || an != nil || b == nil {
		t.Fatalf("动图 %v、位图 %v、错误 %v", an, b, err)
	}
	if _, _, err := decodeAnimated([]byte("not an image"), 768); err == nil {
		t.Fatal("乱字节应当打不开")
	}
}
