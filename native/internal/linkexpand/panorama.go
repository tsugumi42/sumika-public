package linkexpand

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"math/bits"
	"time"

	xdraw "golang.org/x/image/draw"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// X 拆图拼回（规格-链接解析模板“X 拆图拼回”）：有人把一张横幅拆成几张竖条发，识别出来就在本地拼回一张。
// 判断顺序，任一步不过都按普通多图发（拿不准不拼）：高度 → 先认变体（dHash）→ 接缝要有信息量 → 接缝对得上。
// 宽度不参与判断。阈值依据是 panorama_test.go 的正反例和真实样本（规格里写了数）。

const (
	// panoHeightTol 是高度允许的差（像素）。
	panoHeightTol = 1
	// panoVariantMaxDist：相邻两张 dHash（64 位）汉明距离不超过它，就当同一画面的几个版本（变体集），不拼。
	panoVariantMaxDist = 10
	// panoBand 是接缝两侧各取几列；panoRows 是把这几列沿高度缩成多少行再比。
	panoBand = 3
	panoRows = 96
	// panoMinEdgeStd：边缘列（缩成 panoRows 行后）亮度的标准差下限（0～255 刻度）。纯色、留白的边缘上接得上不算证据。
	panoMinEdgeStd = 8.0
	// panoSeamAbs、panoSeamRel：接缝两侧平均绝对差（0～255 刻度）不超过 abs + rel × 图内同样距离的平均绝对差。
	panoSeamAbs = 6.0
	panoSeamRel = 2.0
	// panoramaTimeout：解码、判断、拼接、编码一起的限时；超时按普通多图发。
	panoramaTimeout = 5 * time.Second
	// panoJPEGQuality 是拼好的图的 JPEG 质量；超体积时每次缩到 panoShrinkStep，最多 panoShrinkTries 次。
	panoJPEGQuality = 92
	panoShrinkStep  = 0.8
	panoShrinkTries = 6
)

// seamStats 是一道接缝的各项数（测试和探针看；日志也写）。
type seamStats struct {
	HashDist           int
	EdgeStdL, EdgeStdR float64
	Seam, Inner        float64
}

type panoVerdict struct {
	OK     bool
	Reason string // 不拼的原因：height、variant、flat_edge、seam；OK 时为空
	Seams  []seamStats
}

// panoramaCandidate 是 Build 阶段（还没有像素）的初筛：X 帖子自己有 ≥2 张图、没有视频、每张图宽高都有、高度相差 ≤panoHeightTol。
// 返回这组图（按原顺序）；不是候选返回 nil。
func panoramaCandidate(r *linkreader.Result) []linkreader.Media {
	if r.Platform != "x" || r.Media.State != "available" {
		return nil
	}
	imgs := xOwnImages(r)
	if len(imgs) < 2 {
		return nil
	}
	for _, m := range r.Media.Value {
		if m.Type == "video" || m.Type == "gif" {
			return nil
		}
	}
	lo, hi := math.MaxInt, 0
	for _, m := range imgs {
		if m.Width.State != "available" || m.Height.State != "available" || m.Height.Value <= 0 || m.Width.Value <= 0 {
			return nil
		}
		lo, hi = min(lo, m.Height.Value), max(hi, m.Height.Value)
	}
	if hi-lo > panoHeightTol {
		return nil
	}
	return imgs
}

// analyzePanorama 判断这一组（按顺序）图是不是一张横幅拆开的。
func analyzePanorama(imgs []image.Image) panoVerdict {
	if len(imgs) < 2 {
		return panoVerdict{Reason: "too_few"}
	}
	lo, hi := math.MaxInt, 0
	for _, im := range imgs {
		h := im.Bounds().Dy()
		lo, hi = min(lo, h), max(hi, h)
	}
	if lo <= 0 || hi-lo > panoHeightTol {
		return panoVerdict{Reason: "height"}
	}
	hashes := make([]uint64, len(imgs))
	for i, im := range imgs {
		hashes[i] = dHash(im)
	}
	v := panoVerdict{}
	for i := 0; i+1 < len(imgs); i++ {
		l, r := imgs[i], imgs[i+1]
		s := seamStats{HashDist: bits.OnesCount64(hashes[i] ^ hashes[i+1])}
		lw, rw := l.Bounds().Dx(), r.Bounds().Dx()
		if lw < 2*panoBand || rw < 2*panoBand {
			v.Seams = append(v.Seams, s)
			v.Reason = "narrow"
			return v
		}
		lb := bandRows(l, lw-panoBand, lw)
		rb := bandRows(r, 0, panoBand)
		s.EdgeStdL, s.EdgeStdR = lumStd(lb), lumStd(rb)
		s.Seam = bandMAD(lb, rb)
		s.Inner = (bandMAD(bandRows(l, lw-2*panoBand, lw-panoBand), lb) + bandMAD(rb, bandRows(r, panoBand, 2*panoBand))) / 2
		v.Seams = append(v.Seams, s)
		switch {
		case s.HashDist <= panoVariantMaxDist:
			v.Reason = "variant"
		case s.EdgeStdL < panoMinEdgeStd || s.EdgeStdR < panoMinEdgeStd:
			v.Reason = "flat_edge"
		case s.Seam > panoSeamAbs+panoSeamRel*s.Inner:
			v.Reason = "seam"
		}
		if v.Reason != "" {
			return v
		}
	}
	v.OK = true
	return v
}

type rgb [3]float64

// bandRows 取 [x0,x1) 这几列，沿高度分成 panoRows 段，每段求平均颜色。
func bandRows(img image.Image, x0, x1 int) []rgb {
	b := img.Bounds()
	h := b.Dy()
	out := make([]rgb, panoRows)
	for i := range panoRows {
		y0, y1 := h*i/panoRows, h*(i+1)/panoRows
		if y1 <= y0 {
			y1 = y0 + 1
		}
		var sum rgb
		n := 0
		for y := y0; y < y1 && y < h; y++ {
			for x := x0; x < x1; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				sum[0] += float64(r >> 8)
				sum[1] += float64(g >> 8)
				sum[2] += float64(bl >> 8)
				n++
			}
		}
		if n > 0 {
			for c := range sum {
				sum[c] /= float64(n)
			}
		}
		out[i] = sum
	}
	return out
}

func lum(c rgb) float64 { return 0.299*c[0] + 0.587*c[1] + 0.114*c[2] }

func lumStd(rows []rgb) float64 {
	var mean float64
	for _, c := range rows {
		mean += lum(c)
	}
	mean /= float64(len(rows))
	var v float64
	for _, c := range rows {
		d := lum(c) - mean
		v += d * d
	}
	return math.Sqrt(v / float64(len(rows)))
}

func bandMAD(a, b []rgb) float64 {
	var sum float64
	for i := range a {
		for c := range 3 {
			sum += math.Abs(a[i][c] - b[i][c])
		}
	}
	return sum / float64(len(a)*3)
}

// dHash 是 9×8 灰度的差分哈希：每格取一小片区域的平均亮度，每行 8 个“左格是否比右格暗”。
func dHash(img image.Image) uint64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	const cols, rows, grid = 9, 8, 6
	var cell [rows][cols]float64
	for cy := range rows {
		for cx := range cols {
			var sum float64
			for sy := range grid {
				for sx := range grid {
					x := (cx*grid + sx) * w / (cols * grid)
					y := (cy*grid + sy) * h / (rows * grid)
					r, g, bl, _ := img.At(b.Min.X+min(x, w-1), b.Min.Y+min(y, h-1)).RGBA()
					sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
				}
			}
			cell[cy][cx] = sum / (grid * grid)
		}
	}
	var hash uint64
	for cy := range rows {
		for cx := range cols - 1 {
			hash <<= 1
			if cell[cy][cx] < cell[cy][cx+1] {
				hash |= 1
			}
		}
	}
	return hash
}

// stitchPanorama 把图横向拼成一张；高度取最小的（差 1 像素时裁掉多的），带透明度的先垫白底。
func stitchPanorama(imgs []image.Image) *image.NRGBA {
	h, w := math.MaxInt, 0
	for _, im := range imgs {
		h = min(h, im.Bounds().Dy())
		w += im.Bounds().Dx()
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	x := 0
	for _, im := range imgs {
		b := im.Bounds()
		draw.Draw(dst, image.Rect(x, 0, x+b.Dx(), h), im, b.Min, draw.Over)
		x += b.Dx()
	}
	return dst
}

// encodePanorama 编码成 JPEG（pngOnly 为真用 PNG）；超过 maxBytes 就每次缩到 80% 重编，最多 panoShrinkTries 次，仍超限报 ErrMediaLimit。
func encodePanorama(img image.Image, pngOnly bool, maxBytes int64) ([]byte, string, int, int, error) {
	cur := img
	for try := 0; ; try++ {
		var out bytes.Buffer
		mime := "image/jpeg"
		var err error
		if pngOnly {
			mime = "image/png"
			err = png.Encode(&out, cur)
		} else {
			err = jpeg.Encode(&out, cur, &jpeg.Options{Quality: panoJPEGQuality})
		}
		if err != nil {
			return nil, "", 0, 0, err
		}
		if int64(out.Len()) <= maxBytes {
			b := cur.Bounds()
			return out.Bytes(), mime, b.Dx(), b.Dy(), nil
		}
		if try >= panoShrinkTries {
			return nil, "", 0, 0, linkreader.ErrMediaLimit
		}
		b := cur.Bounds()
		w, h := max(1, int(float64(b.Dx())*panoShrinkStep)), max(1, int(float64(b.Dy())*panoShrinkStep))
		small := image.NewNRGBA(image.Rect(0, 0, w, h))
		xdraw.ApproxBiLinear.Scale(small, small.Bounds(), cur, b, xdraw.Src, nil)
		cur = small
	}
}

type panoramaOutcome struct {
	res     *types.MediaResource
	verdict panoVerdict
	err     error
}

// stitchLoaded 解码这组已下载的图、判断、拼接、编码，整个放在一个协程里，限时 timeout；
// 判断不通过或失败返回 res == nil，超时也是（协程晚到的结果会被释放）。调用方不释放 loaded。
func (p *Preparer) stitchLoaded(ctx context.Context, loaded []Loaded) panoramaOutcome {
	timeout := p.PanoramaTimeout
	if timeout <= 0 {
		timeout = panoramaTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan panoramaOutcome, 1)
	maxBytes := p.imageMax()
	go func() {
		imgs := make([]image.Image, 0, len(loaded))
		allPNG := true
		for _, l := range loaded {
			var im image.Image
			var format string
			err := l.Resource.Read(func(r io.Reader) error {
				var e error
				im, format, e = image.Decode(io.LimitReader(r, 64<<20))
				return e
			})
			if err != nil || l.Kind == types.BodyGIF || format == "gif" {
				done <- panoramaOutcome{verdict: panoVerdict{Reason: "decode"}}
				return
			}
			allPNG = allPNG && format == "png"
			imgs = append(imgs, im)
		}
		v := analyzePanorama(imgs)
		if !v.OK {
			done <- panoramaOutcome{verdict: v}
			return
		}
		data, mime, w, h, err := encodePanorama(stitchPanorama(imgs), allPNG, maxBytes)
		if err != nil {
			done <- panoramaOutcome{verdict: panoVerdict{Reason: "encode", Seams: v.Seams}, err: err}
			return
		}
		done <- panoramaOutcome{res: types.MemoryMedia(rand.Text(), mime, w, h, data), verdict: v}
	}()
	select {
	case out := <-done:
		return out
	case <-ctx.Done():
		go func() { // 晚到的结果没人要，释放
			if out := <-done; out.res != nil {
				_ = out.res.Release()
			}
		}()
		return panoramaOutcome{verdict: panoVerdict{Reason: "timeout"}, err: ctx.Err()}
	}
}

func (v panoVerdict) String() string {
	s := "拼接"
	if v.OK {
		s += "通过"
	} else {
		s += "不拼（" + v.Reason + "）"
	}
	for i, x := range v.Seams {
		s += fmt.Sprintf("；缝%d dHash距离%d 边缘标准差%.1f/%.1f 缝%.1f 内%.1f", i+1, x.HashDist, x.EdgeStdL, x.EdgeStdR, x.Seam, x.Inner)
	}
	return s
}

// markPanorama 在候选组的第一张图上记 PanoramaN（组必须是连续的几个图片项，否则不标，走普通多图）。
func (b *builder) markPanorama(items []Item) {
	cand := panoramaCandidate(b.r)
	if cand == nil {
		return
	}
	in := map[string]bool{}
	for _, m := range cand {
		in[m.URL.Value] = true
	}
	first, n := -1, 0
	for i, it := range items {
		if (it.Kind == types.BodyImage || it.Kind == "photo") && in[it.Media.URL.Value] {
			if first < 0 {
				first = i
			} else if i != first+n {
				return
			}
			n++
		}
	}
	if n != len(cand) {
		return
	}
	items[first].PanoramaN = n
	items[first].PanoramaKeep = b.o.PanoramaOriginals
}

// appendPanoramaOriginals：开了“拼接后另附原图”时，把候选组的原图放进末尾一个合并转发（一个节点）。
// 这些项标 PanoramaOrig，Prepare 只在拼成功时才发它们；没拼成原图已经按普通多图发过了。
func appendPanoramaOriginals(d *Draft) {
	var orig []Item
	collect := func(items []Item) {
		for i, it := range items {
			if it.PanoramaN > 1 && it.PanoramaKeep && i+it.PanoramaN <= len(items) && len(orig) == 0 {
				for _, o := range items[i : i+it.PanoramaN] {
					o.PanoramaN, o.PanoramaKeep, o.PanoramaOrig = 0, false, true
					orig = append(orig, o)
				}
			}
		}
	}
	for _, p := range d.Parts {
		collect(p.Body)
		for _, n := range p.Nodes {
			collect(n.Body)
		}
	}
	if len(orig) > 0 {
		d.Parts = append(d.Parts, Part{Kind: types.PartForward, Nodes: []Node{{Name: NodeName, Body: orig}}})
	}
}

// DiagnosePanorama 给 link-probe 用：解码这组图的字节、按顺序判断，返回说明；通过时同时返回拼好的 JPEG。不联网、不碰配置。
func DiagnosePanorama(images [][]byte) (report string, stitched []byte, err error) {
	imgs := make([]image.Image, 0, len(images))
	allPNG := true
	for i, raw := range images {
		im, format, e := image.Decode(bytes.NewReader(raw))
		if e != nil {
			return "", nil, fmt.Errorf("第 %d 张解码失败：%w", i+1, e)
		}
		allPNG = allPNG && format == "png"
		b := im.Bounds()
		report += fmt.Sprintf("第%d张 %s %d×%d %d 字节\n", i+1, format, b.Dx(), b.Dy(), len(raw))
		imgs = append(imgs, im)
	}
	v := analyzePanorama(imgs)
	report += v.String() + "\n"
	if !v.OK {
		return report, nil, nil
	}
	data, _, w, h, e := encodePanorama(stitchPanorama(imgs), allPNG, defaultImageMaxBytes)
	if e != nil {
		return report, nil, e
	}
	report += fmt.Sprintf("拼好 %d×%d，%d 字节\n", w, h, len(data))
	return report, data, nil
}
