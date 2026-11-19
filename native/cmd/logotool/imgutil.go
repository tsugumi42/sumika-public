package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// 奶油底色。半透明的像素（比如 SVG 圆角外面）一律按"画在奶油底上"合成，
// 参考图和候选图用同一个底色，所以对比不受透明度影响。
var creamBG = [3]float64{253, 246, 234}

// RGB 是不透明的 8 位 RGB 图，Pix 按行排，每像素 3 字节。
type RGB struct {
	W, H int
	Pix  []uint8
}

func newRGB(w, h int) *RGB { return &RGB{W: w, H: h, Pix: make([]uint8, w*h*3)} }

func (m *RGB) at(x, y int) (uint8, uint8, uint8) {
	i := (y*m.W + x) * 3
	return m.Pix[i], m.Pix[i+1], m.Pix[i+2]
}

func (m *RGB) set(x, y int, r, g, b uint8) {
	i := (y*m.W + x) * 3
	m.Pix[i], m.Pix[i+1], m.Pix[i+2] = r, g, b
}

func (m *RGB) fill(r, g, b uint8) {
	for i := 0; i < len(m.Pix); i += 3 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2] = r, g, b
	}
}

func clamp8(v float64) uint8 {
	v = math.Round(v)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// loadPNG 读 PNG，半透明部分合成到奶油底上。
func loadPNG(path string) (*RGB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s 不是能读的 PNG：%w", path, err)
	}
	b := im.Bounds()
	out := newRGB(b.Dx(), b.Dy())
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			r, g, bl, a := im.At(b.Min.X+x, b.Min.Y+y).RGBA() // 预乘的 16 位
			inv := 1 - float64(a)/65535
			out.set(x, y,
				clamp8(float64(r)/257+inv*creamBG[0]),
				clamp8(float64(g)/257+inv*creamBG[1]),
				clamp8(float64(bl)/257+inv*creamBG[2]))
		}
	}
	return out, nil
}

// savePNG 写 PNG（先写临时文件再改名，别的进程不会读到写了一半的图）。
func savePNG(path string, m *RGB) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	dst := image.NewNRGBA(image.Rect(0, 0, m.W, m.H))
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			r, g, b := m.at(x, y)
			dst.SetNRGBA(x, y, color.NRGBA{r, g, b, 255})
		}
	}
	return writePNGAtomic(path, dst)
}

func writePNGAtomic(path string, im image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := png.Encode(f, im); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// hconcat 把几张图横着拼起来，中间留 gap 像素的灰缝，高度取最高的，矮的顶对齐。
func hconcat(imgs []*RGB, gap int) *RGB {
	w, h := 0, 0
	for i, m := range imgs {
		w += m.W
		if i > 0 {
			w += gap
		}
		if m.H > h {
			h = m.H
		}
	}
	out := newRGB(w, h)
	out.fill(128, 128, 128)
	x0 := 0
	for _, m := range imgs {
		for y := 0; y < m.H; y++ {
			copy(out.Pix[(y*out.W+x0)*3:], m.Pix[y*m.W*3:(y+1)*m.W*3])
		}
		x0 += m.W + gap
	}
	return out
}
