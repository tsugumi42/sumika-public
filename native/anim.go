package main

// 动图的解码和帧号（规格-原生界面第二版 3.5、4.1、4.3）：
//   - decodeAnimated 是统一入口：先按字节头认（sniff.go），不看 mime 也不看 seg.Animated；认出多帧的、预算放得下的，
//     解成一串同尺寸的 *image.RGBA（已经缩到显示像素、预乘 alpha、原点在 (0,0)、步长 4×宽，所以 Bitmap.Update 不拷贝）；
//     单帧、超预算、解到一半坏了的，退回 decodeStatic 的首帧，动图的标记（角标）由调用方照 seg.Animated 画，不受影响；
//   - 预算：解码瞬时内存（帧数 × 画布 × 每像素字节）不超过 64 MB，存下来的（帧数 × 缩放后的宽 × 高 × 4）不超过 24 MB，
//     都在 DecodeAll 之前用数出来的帧数算，超了不去解；
//   - 帧时长统一成毫秒，0 或小于 20 毫秒的按 100 毫秒播（暂定，浏览器对很短的延迟会抬高，具体的数没核对）；
//   - animation.frameAt 按"离开始播过了多久"算该显示哪一帧、还要等多久换下一帧；存下来的帧不可变。

import (
	"bytes"
	"image"
	"image/gif"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/gen2brain/pngn"
	vpxwebp "github.com/gen2brain/vpx/webp"
	"golang.org/x/image/draw"
)

const (
	animMaxTransient = 64 << 20 // 解码瞬时内存上限
	animMaxStored    = 24 << 20 // 存下来的帧的总字节上限
	animMinDelay     = 20       // 毫秒；小于这个的帧时长按 animDefaultDelay 播
	animDefaultDelay = 100      // 毫秒
)

// animation 一个解好的动图。frames 解好以后不再改；startedAt、cur 只在界面线程读写
type animation struct {
	frames []*image.RGBA
	delays []int // 毫秒，已经抬过
	plays  int   // 播几遍，0 是无限
	total  int   // 一遍的总毫秒

	startedAt time.Time // 第一次播的时刻；零值是还没播过
	cur       int       // bmp 里现在是第几帧
}

func newAnimation(frames []*image.RGBA, delaysMs []int, plays int) *animation {
	an := &animation{frames: frames, delays: make([]int, len(frames)), plays: plays}
	for i := range frames {
		d := animDefaultDelay
		if i < len(delaysMs) && delaysMs[i] >= animMinDelay {
			d = delaysMs[i]
		}
		an.delays[i] = d
		an.total += d
	}
	return an
}

// bytes 存下来的帧一共多少字节
func (an *animation) bytes() (n int) {
	for _, f := range an.frames {
		n += len(f.Pix)
	}
	return
}

// frameAt 播了 elapsed 以后该显示第几帧；wait 是还要等多久才换下一帧，0 是不会再换了（播完停在末帧）。
// 循环用完以后一直是末帧，所以切回会话时按当前时刻算，播完的不会重播
func (an *animation) frameAt(elapsed time.Duration) (idx int, wait time.Duration) {
	last := len(an.frames) - 1
	if last <= 0 || an.total <= 0 {
		return 0, 0
	}
	ms := int(elapsed / time.Millisecond)
	if ms < 0 {
		ms = 0
	}
	if an.plays > 0 && ms >= an.total*an.plays {
		return last, 0
	}
	r := ms % an.total
	end := 0
	for i, d := range an.delays {
		end += d
		if r < end {
			return i, time.Duration(end-r) * time.Millisecond
		}
	}
	return last, 0
}

// animFits 预算检查（纯函数）：n 帧、画布 w×h、每像素 perPx 字节（GIF 的调色板帧是 1，其余 4）、缩到宽 tw。
// 瞬时内存不超过 64 MB，且存下来的不超过 24 MB 才放得下
func animFits(n, w, h, perPx, tw int) bool {
	if n <= 0 || w <= 0 || h <= 0 {
		return false
	}
	if int64(n)*int64(w)*int64(h)*int64(perPx) > animMaxTransient {
		return false
	}
	sw, sh := fitSize(w, h, tw)
	return int64(n)*int64(sw)*int64(sh)*4 <= animMaxStored
}

// decodeAnimated 动图的统一入口。返回 (动图, 首帧位图)：动图不为 nil 时位图是它的第一帧（播放时拿它 Update）；
// 为 nil 时只有静态位图（单帧、超预算、动图本身解坏了都退到这里）。tw 是缩放后的宽（像素）
func decodeAnimated(data []byte, tw int) (*animation, *ui.Bitmap, error) {
	still := func() (*animation, *ui.Bitmap, error) {
		b, err := decodeStatic(data, tw)
		return nil, b, err
	}
	kind, n := sniffAnim(data)
	if kind == animNone || n <= 1 {
		return still()
	}
	var cfg image.Config
	var err error
	perPx := 4
	switch kind {
	case animGIF:
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
		perPx = 1
	case animAPNG:
		cfg, err = pngn.DecodeConfig(bytes.NewReader(data))
	case animWebP:
		cfg, err = vpxwebp.DecodeConfig(bytes.NewReader(data))
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || float64(cfg.Width)*float64(cfg.Height) > imgMaxPixels {
		return nil, nil, errBad
	}
	if !animFits(n, cfg.Width, cfg.Height, perPx, tw) {
		return still()
	}
	var an *animation
	switch kind {
	case animGIF:
		an, err = decodeGIF(data, tw)
	case animAPNG:
		an, err = decodeAPNG(data, tw)
	case animWebP:
		an, err = decodeWebPAnim(data, tw)
	}
	if err != nil || an == nil || len(an.frames) == 0 {
		return still() // 声明的帧数和实际对不上、截断：能显示首帧就显示首帧
	}
	if len(an.frames) == 1 {
		return nil, ui.NewBitmap(an.frames[0]), nil
	}
	return an, ui.NewBitmap(an.frames[0]), nil
}

// frameOf 把一帧缩到宽 tw 以内（不放大）做成独立的 RGBA：原点 (0,0)、步长 4×宽，预乘 alpha
func frameOf(src image.Image, tw int) *image.RGBA {
	b := src.Bounds()
	nw, nh := fitSize(b.Dx(), b.Dy(), tw)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	if nw == b.Dx() && nh == b.Dy() {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	} else {
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	}
	return dst
}

// decodeGIF 合成：画布取逻辑屏幕的大小，起步全透明；每帧 Over 画上去，再按处置方式收拾（2 清成透明，3 恢复画之前的，0 和 1 不动）
func decodeGIF(data []byte, tw int) (*animation, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(g.Image) == 0 {
		return nil, errBad
	}
	w, h := g.Config.Width, g.Config.Height
	if w <= 0 || h <= 0 {
		return nil, errBad
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	var frames []*image.RGBA
	var delays []int
	for i, fr := range g.Image {
		disp := byte(gif.DisposalNone)
		if i < len(g.Disposal) {
			disp = g.Disposal[i]
		}
		region := fr.Bounds().Intersect(canvas.Bounds())
		var saved *image.RGBA
		if disp == gif.DisposalPrevious {
			saved = image.NewRGBA(image.Rect(0, 0, region.Dx(), region.Dy()))
			draw.Draw(saved, saved.Bounds(), canvas, region.Min, draw.Src)
		}
		draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		frames = append(frames, frameOf(canvas, tw))
		d := 0
		if i < len(g.Delay) {
			d = g.Delay[i] * 10
		}
		delays = append(delays, d)
		switch disp {
		case gif.DisposalBackground:
			draw.Draw(canvas, region, image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			draw.Draw(canvas, region, saved, image.Point{}, draw.Src)
		}
	}
	// LoopCount：-1 没有 NETSCAPE 块，只播一次；0 无限；n 播 n+1 遍
	plays := 0
	switch {
	case g.LoopCount < 0:
		plays = 1
	case g.LoopCount > 0:
		plays = g.LoopCount + 1
	}
	return newAnimation(frames, delays, plays), nil
}

// decodeAPNG pngn 给的每帧已经是整张画布（直通 alpha），缩放时转成预乘的 RGBA。LoopCount 为 0 无限，n 是播 n 遍
func decodeAPNG(data []byte, tw int) (*animation, error) {
	a, err := pngn.DecodeAll(bytes.NewReader(data))
	if err != nil || len(a.Image) == 0 {
		return nil, errBad
	}
	frames := make([]*image.RGBA, len(a.Image))
	delays := make([]int, len(a.Image))
	for i, img := range a.Image {
		frames[i] = frameOf(img, tw)
		if i < len(a.Delay) {
			delays[i] = int(a.Delay[i]*1000 + 0.5)
		}
	}
	return newAnimation(frames, delays, a.LoopCount), nil
}

// decodeWebPAnim vpx 给的每帧已经是预乘的整画布 RGBA，延迟是毫秒，循环数 0 无限
func decodeWebPAnim(data []byte, tw int) (*animation, error) {
	a, err := vpxwebp.DecodeAll(bytes.NewReader(data), vpxwebp.Options{FrameSizeLimit: animMaxTransient / 4})
	if err != nil || len(a.Image) == 0 {
		return nil, errBad
	}
	frames := make([]*image.RGBA, len(a.Image))
	for i, img := range a.Image {
		frames[i] = frameOf(img, tw)
	}
	return newAnimation(frames, a.Delay, a.LoopCount), nil
}
