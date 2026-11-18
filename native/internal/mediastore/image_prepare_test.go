package mediastore

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"strings"
	"testing"

	vpxwebp "github.com/gen2brain/vpx/webp"
)

func prepPNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func prepDecode(t *testing.T, input []byte) image.Image {
	t.Helper()
	out, mime, err := PrepareImage(bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" {
		t.Fatalf("MIME = %q", mime)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestPrepareImageDimensions(t *testing.T) {
	for _, tc := range []struct{ w, h, wantW, wantH int }{
		{2048, 1024, 1024, 512}, {300, 1200, 256, 1024}, {20, 10, 20, 10}, {1, 2048, 1, 1024},
	} {
		img := prepDecode(t, prepPNG(t, tc.w, tc.h, color.NRGBA{R: 200, A: 255}))
		if img.Bounds().Dx() != tc.wantW || img.Bounds().Dy() != tc.wantH {
			t.Errorf("%dx%d: got %v, want %dx%d", tc.w, tc.h, img.Bounds(), tc.wantW, tc.wantH)
		}
	}
}

func TestPrepareImageGIFFirstFrame(t *testing.T) {
	palette := color.Palette{color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	first := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	second := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	for i := range second.Pix {
		second.Pix[i] = 1
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	img := prepDecode(t, b.Bytes())
	r, _, blue, _ := img.At(0, 0).RGBA()
	if r != 0xffff || blue != 0 {
		t.Fatalf("first frame pixel = %v", img.At(0, 0))
	}
}

func TestPrepareImageAPNGFirstFrameAfterDefault(t *testing.T) {
	defaultPNG := prepPNG(t, 2, 2, color.NRGBA{B: 255, A: 255})
	firstPNG := prepPNG(t, 1, 1, color.NRGBA{R: 255, A: 255})
	secondPNG := prepPNG(t, 1, 1, color.NRGBA{G: 255, A: 255})
	var apng bytes.Buffer
	apng.Write(defaultPNG[:8])
	var control [26]byte
	binary.BigEndian.PutUint32(control[4:8], 1)
	binary.BigEndian.PutUint32(control[8:12], 1)
	binary.BigEndian.PutUint32(control[12:16], 1)
	var animation [8]byte
	binary.BigEndian.PutUint32(animation[:4], 2)
	for pos := 8; pos+12 <= len(defaultPNG); {
		n := int(binary.BigEndian.Uint32(defaultPNG[pos:]))
		kind := string(defaultPNG[pos+4 : pos+8])
		if kind == "IDAT" && animation != [8]byte{} {
			writePNGChunk(&apng, "acTL", animation[:])
			animation = [8]byte{}
		}
		if kind == "IEND" {
			writePNGChunk(&apng, "fcTL", control[:])
			for p := 8; p+12 <= len(firstPNG); {
				size := int(binary.BigEndian.Uint32(firstPNG[p:]))
				if string(firstPNG[p+4:p+8]) == "IDAT" {
					payload := append([]byte{0, 0, 0, 1}, firstPNG[p+8:p+8+size]...)
					writePNGChunk(&apng, "fdAT", payload)
				}
				p += 12 + size
			}
			binary.BigEndian.PutUint32(control[:4], 2)
			writePNGChunk(&apng, "fcTL", control[:])
			for p := 8; p+12 <= len(secondPNG); {
				size := int(binary.BigEndian.Uint32(secondPNG[p:]))
				if string(secondPNG[p+4:p+8]) == "IDAT" {
					payload := append([]byte{0, 0, 0, 3}, secondPNG[p+8:p+8+size]...)
					writePNGChunk(&apng, "fdAT", payload)
				}
				p += 12 + size
			}
		}
		apng.Write(defaultPNG[pos : pos+12+n])
		pos += 12 + n
	}
	img := prepDecode(t, apng.Bytes())
	r, _, b, a := img.At(1, 0).RGBA()
	if r != 0xffff || b != 0 || a != 0xffff {
		t.Fatalf("first frame pixel = %v", img.At(1, 0))
	}
	_, _, _, a = img.At(0, 0).RGBA()
	if a != 0 {
		t.Fatalf("outside first frame should be transparent, got %v", img.At(0, 0))
	}
}

func TestPrepareImageWebPFirstFrame(t *testing.T) {
	first := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	second := image.NewNRGBA(first.Bounds())
	for y := range 2 {
		for x := range 2 {
			first.Set(x, y, color.NRGBA{R: 255, A: 255})
			second.Set(x, y, color.NRGBA{B: 255, A: 255})
		}
	}
	var b bytes.Buffer
	if err := vpxwebp.EncodeAll(&b, &vpxwebp.WEBP{Image: []image.Image{first, second}, Delay: []int{50, 50}}, vpxwebp.EncodeOptions{Lossless: true}); err != nil {
		t.Fatal(err)
	}
	img := prepDecode(t, b.Bytes())
	r, _, blue, _ := img.At(0, 0).RGBA()
	if r != 0xffff || blue != 0 {
		t.Fatalf("first frame pixel = %v", img.At(0, 0))
	}
}

func TestPrepareImageRejectsInvalidAndOversize(t *testing.T) {
	for _, tc := range []struct {
		name     string
		r        io.Reader
		contains string
	}{
		{"invalid", strings.NewReader("not an image"), "unsupported"},
		{"truncated", bytes.NewReader([]byte("\x89PNG\r\n\x1a\n")), "decode"},
		{"oversize bytes", io.LimitReader(zeroReader{}, thumbMaxBytes+1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := PrepareImage(tc.r)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[:4], 10001)
	binary.BigEndian.PutUint32(ihdr[4:8], 10000)
	ihdr[8], ihdr[9] = 8, 6
	writePNGChunk(&b, "IHDR", ihdr[:])
	writePNGChunk(&b, "IDAT", nil)
	_, _, err := PrepareImage(&b)
	if err == nil || !strings.Contains(err.Error(), "pixel limit") {
		t.Fatalf("oversize pixels error = %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
