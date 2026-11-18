package main

// 动图的测试样本（第八～十步共用）：全部在测试里现造，不读文件、不连 7720。
//   - GIF：标准库 gif.EncodeAll；
//   - APNG：手写块（acTL、fcTL、fdAT），每帧的像素数据借 png.Encode 出的 IDAT；
//   - 动态 WebP：vpx 的 EncodeAll（无损，颜色逐像素还原）。

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/egoist/mygo/ui"
	vpxwebp "github.com/gen2brain/vpx/webp"
)

type colorN = color.NRGBA

// solid 一块纯色（不透明）
func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

var (
	cRed    = color.NRGBA{255, 0, 0, 255}
	cGreen  = color.NRGBA{0, 255, 0, 255}
	cBlue   = color.NRGBA{0, 0, 255, 255}
	cYellow = color.NRGBA{R: 255, G: 255, A: 255}
)

// gifSample 每帧一个纯色整屏；delays 是百分之一秒；loop 是 gif.GIF.LoopCount（-1 没有 NETSCAPE 块，0 无限，n 重复 n 次）
func gifSample(t testing.TB, w, h int, cols []color.NRGBA, delays []int, loop int) []byte {
	t.Helper()
	g := &gif.GIF{LoopCount: loop}
	for i, c := range cols {
		fr := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.NRGBA{0, 0, 0, 0}, c})
		for j := range fr.Pix {
			fr.Pix[j] = 1
		}
		g.Image = append(g.Image, fr)
		g.Delay = append(g.Delay, delays[i])
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngChunk(typ string, data []byte) []byte {
	b := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(b, uint32(len(data)))
	copy(b[4:], typ)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

// pngParts 把 png.Encode 出的文件拆成 IHDR 的内容和（所有 IDAT 的内容拼起来）
func pngParts(t testing.TB, img image.Image) (ihdr, idat []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()[8:]
	for len(b) >= 12 {
		n := int(binary.BigEndian.Uint32(b))
		switch string(b[4:8]) {
		case "IHDR":
			ihdr = b[8 : 8+n]
		case "IDAT":
			idat = append(idat, b[8:8+n]...)
		}
		b = b[12+n:]
	}
	return
}

// apngSample 手写 APNG。def 不为 nil：默认图（IDAT）不属于动画，动画是 frames 各帧（fdAT）；
// def 为 nil：默认图就是 frames[0]（fcTL 在 IDAT 前面），后面的帧用 fdAT。delaysMs 毫秒；plays 是 num_plays（0 无限）
func apngSample(t testing.TB, def image.Image, frames []image.Image, delaysMs []int, plays int) []byte {
	t.Helper()
	first := frames[0]
	if def != nil {
		first = def
	}
	ihdr, _ := pngParts(t, first)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, pngChunk("IHDR", ihdr)...)
	actl := binary.BigEndian.AppendUint32(nil, uint32(len(frames)))
	out = append(out, pngChunk("acTL", binary.BigEndian.AppendUint32(actl, uint32(plays)))...)
	seq := uint32(0)
	fctl := func(i int) []byte {
		b := binary.BigEndian.AppendUint32(nil, seq)
		seq++
		bnd := frames[i].Bounds()
		b = binary.BigEndian.AppendUint32(b, uint32(bnd.Dx()))
		b = binary.BigEndian.AppendUint32(b, uint32(bnd.Dy()))
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint16(b, uint16(delaysMs[i]))
		b = binary.BigEndian.AppendUint16(b, 1000)
		return append(b, 0, 0) // dispose none, blend source
	}
	for i, fr := range frames {
		_, idat := pngParts(t, fr)
		switch {
		case i == 0 && def == nil:
			out = append(out, pngChunk("fcTL", fctl(0))...)
			out = append(out, pngChunk("IDAT", idat)...)
		case i == 0:
			_, defIDAT := pngParts(t, def)
			out = append(out, pngChunk("IDAT", defIDAT)...)
			fallthrough
		default:
			out = append(out, pngChunk("fcTL", fctl(i))...)
			fd := binary.BigEndian.AppendUint32(nil, seq)
			seq++
			out = append(out, pngChunk("fdAT", append(fd, idat...))...)
		}
	}
	return append(out, pngChunk("IEND", nil)...)
}

// webpSample 动态 WebP，无损；delaysMs 毫秒；loop 是 ANIM 块的循环数（0 无限）
func webpSample(t testing.TB, frames []image.Image, delaysMs []int, loop int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := vpxwebp.EncodeAll(&buf, &vpxwebp.WEBP{Image: frames, Delay: delaysMs, LoopCount: loop}, vpxwebp.EncodeOptions{Lossless: true}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func toRGBA(c color.NRGBA) color.RGBA {
	r, g, b, a := c.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

func imagePixel(img image.Image, x, y int) color.RGBA {
	return color.RGBAModel.Convert(img.At(img.Bounds().Min.X+x, img.Bounds().Min.Y+y)).(color.RGBA)
}

func near(a, b color.RGBA, tol int) bool {
	d := func(x, y uint8) int { return max(int(x)-int(y), int(y)-int(x)) }
	return d(a.R, b.R) <= tol && d(a.G, b.G) <= tol && d(a.B, b.B) <= tol && d(a.A, b.A) <= tol
}

func typeName(v any) string { return fmt.Sprintf("%T", v) }

// bitmapPixel 位图在 (x,y) 的颜色：位图没有读像素的接口，放进 Tester 按原大小画在左上角，从画出来的帧里取
func bitmapPixel(b *ui.Bitmap, x, y int) color.RGBA {
	w, h := b.Size()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { ui.Image(c, b).Size(float32(w), float32(h)) })
	}, w, h)
	tt.Frame()
	return tt.Image().RGBAAt(x, y)
}

func rgbaAt(img *image.RGBA, x, y int) color.RGBA { return img.RGBAAt(x, y) }
