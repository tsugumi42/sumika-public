package main

import (
	"math"
	"sort"
)

// ---------- CIELAB（sRGB，D65） ----------

var srgbToLin [256]float64

func init() {
	for i := range srgbToLin {
		c := float64(i) / 255
		if c <= 0.04045 {
			srgbToLin[i] = c / 12.92
		} else {
			srgbToLin[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
}

func labF(t float64) float64 {
	if t > 216.0/24389.0 {
		return math.Cbrt(t)
	}
	return (24389.0/27.0*t + 16) / 116
}

func rgbToLab(r, g, b uint8) [3]float64 {
	R, G, B := srgbToLin[r], srgbToLin[g], srgbToLin[b]
	x := (0.4124564*R + 0.3575761*G + 0.1804375*B) / 0.95047
	y := 0.2126729*R + 0.7151522*G + 0.0721750*B
	z := (0.0193339*R + 0.1191920*G + 0.9503041*B) / 1.08883
	fx, fy, fz := labF(x), labF(y), labF(z)
	return [3]float64{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}

// labImage 把整张图转成 Lab，三个通道交错存放。
func labImage(m *RGB) []float32 {
	out := make([]float32, m.W*m.H*3)
	for i := 0; i < m.W*m.H; i++ {
		l := rgbToLab(m.Pix[i*3], m.Pix[i*3+1], m.Pix[i*3+2])
		out[i*3], out[i*3+1], out[i*3+2] = float32(l[0]), float32(l[1]), float32(l[2])
	}
	return out
}

// ---------- 四个类和 k-means ----------

const (
	clsBG = iota
	clsBody
	clsEye
	clsBeak
	numCls
)

var clsNames = [numCls]string{"bg", "body", "eye", "beak"}

// 初值：奶油底、藏青、近黑、橙。
var initRGB = [numCls][3]uint8{
	{253, 246, 234},
	{54, 65, 100},
	{25, 25, 30},
	{245, 160, 60},
}

func nearest(lab []float32, i int, centers *[numCls][3]float64) int {
	l, a, b := float64(lab[i*3]), float64(lab[i*3+1]), float64(lab[i*3+2])
	best, bd := 0, math.MaxFloat64
	for c := 0; c < numCls; c++ {
		dl, da, db := l-centers[c][0], a-centers[c][1], b-centers[c][2]
		if d := dl*dl + da*da + db*db; d < bd {
			best, bd = c, d
		}
	}
	return best
}

func classify(lab []float32, centers *[numCls][3]float64) []uint8 {
	n := len(lab) / 3
	out := make([]uint8, n)
	for i := 0; i < n; i++ {
		out[i] = uint8(nearest(lab, i, centers))
	}
	return out
}

// kmeans 以固定初值在 Lab 里做几轮 k-means，返回类中心和实际迭代轮数。
// 初值固定、数据固定，所以结果是确定的。
func kmeans(lab []float32) (centers [numCls][3]float64, iters int) {
	for c := 0; c < numCls; c++ {
		centers[c] = rgbToLab(initRGB[c][0], initRGB[c][1], initRGB[c][2])
	}
	n := len(lab) / 3
	for iters = 1; iters <= 30; iters++ {
		var sum [numCls][3]float64
		var cnt [numCls]float64
		for i := 0; i < n; i++ {
			c := nearest(lab, i, &centers)
			sum[c][0] += float64(lab[i*3])
			sum[c][1] += float64(lab[i*3+1])
			sum[c][2] += float64(lab[i*3+2])
			cnt[c]++
		}
		shift := 0.0
		for c := 0; c < numCls; c++ {
			if cnt[c] == 0 {
				continue // 这一类一个像素都没有：中心不动
			}
			for k := 0; k < 3; k++ {
				v := sum[c][k] / cnt[c]
				shift = math.Max(shift, math.Abs(v-centers[c][k]))
				centers[c][k] = v
			}
		}
		if shift < 1e-3 {
			break
		}
	}
	return
}

// specksMinArea：小于这个面积的连通块算斑点。取整图面积的 0.01%（1254×1254 时约 157 像素），
// 最少 4 像素。眼睛、喙都在 2 万像素上下，离这个门槛很远。
func specksMinArea(w, h int) int {
	return max(4, int(math.Round(float64(w*h)*1e-4)))
}

// removeSpecks：把 4 连通、面积小于 minArea 的块改成它周围出现最多的另一类，返回改了多少像素。
// 为什么要有这一步：藏青的暗部、喙下的软阴影里，个别像素离"近黑"比离"藏青"更近，
// 会分出几块孤立的 eye 斑点；参考图有而候选图没有时，它们到最近眼睛边界的距离是几百像素，
// 会把边界平均值和最大值拉偏，而这不是描得好不好的差别。先全部找出再统一改，结果和扫描顺序无关。
func removeSpecks(labels []uint8, w, h, minArea int) int {
	n := w * h
	seen := make([]bool, n)
	type fix struct {
		px []int32
		to uint8
	}
	var fixes []fix
	var queue []int32
	for s := 0; s < n; s++ {
		if seen[s] {
			continue
		}
		c := labels[s]
		queue = append(queue[:0], int32(s))
		seen[s] = true
		for qi := 0; qi < len(queue); qi++ {
			p := int(queue[qi])
			x := p % w
			for _, q := range [4]int{p - 1, p + 1, p - w, p + w} {
				if (q == p-1 && x == 0) || (q == p+1 && x == w-1) || q < 0 || q >= n {
					continue
				}
				if !seen[q] && labels[q] == c {
					seen[q] = true
					queue = append(queue, int32(q))
				}
			}
		}
		if len(queue) >= minArea {
			continue
		}
		var cnt [numCls]int
		for _, p32 := range queue {
			p := int(p32)
			x := p % w
			for _, q := range [4]int{p - 1, p + 1, p - w, p + w} {
				if (q == p-1 && x == 0) || (q == p+1 && x == w-1) || q < 0 || q >= n {
					continue
				}
				if labels[q] != c {
					cnt[labels[q]]++
				}
			}
		}
		best, bc := -1, 0
		for k := 0; k < numCls; k++ {
			if cnt[k] > bc {
				best, bc = k, cnt[k]
			}
		}
		if best >= 0 {
			fixes = append(fixes, fix{px: append([]int32(nil), queue...), to: uint8(best)})
		}
	}
	changed := 0
	for _, f := range fixes {
		for _, p := range f.px {
			labels[p] = f.to
		}
		changed += len(f.px)
	}
	return changed
}

// ---------- 精确欧氏距离变换（Felzenszwalb & Huttenlocher 两遍法） ----------

const edtInf = 1e10

// dt1d 对长度 n 的一行做一维平方距离变换。v、z 是调用方给的工作区（长度 n、n+1）。
func dt1d(f, d []float64, v []int, z []float64, n int) {
	k := 0
	v[0] = 0
	z[0] = -math.MaxFloat64
	z[1] = math.MaxFloat64
	for q := 1; q < n; q++ {
		var s float64
		for {
			p := v[k]
			s = ((f[q] + float64(q*q)) - (f[p] + float64(p*p))) / float64(2*q-2*p)
			if s <= z[k] && k > 0 {
				k--
				continue
			}
			break
		}
		k++
		v[k] = q
		z[k] = s
		z[k+1] = math.MaxFloat64
	}
	k = 0
	for q := 0; q < n; q++ {
		for z[k+1] < float64(q) {
			k++
		}
		dq := float64(q - v[k])
		d[q] = dq*dq + f[v[k]]
	}
}

// edtSquared 返回每个像素到最近的"源点"（src 为 true）的欧氏距离的平方；
// 没有任何源点时全是 edtInf 量级的大数。
func edtSquared(src []bool, w, h int) []float64 {
	g := make([]float64, w*h)
	for i, s := range src {
		if s {
			g[i] = 0
		} else {
			g[i] = edtInf
		}
	}
	m := w
	if h > m {
		m = h
	}
	f := make([]float64, m)
	d := make([]float64, m)
	v := make([]int, m)
	z := make([]float64, m+1)
	// 先按列
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			f[y] = g[y*w+x]
		}
		dt1d(f, d, v, z, h)
		for y := 0; y < h; y++ {
			g[y*w+x] = d[y]
		}
	}
	// 再按行
	for y := 0; y < h; y++ {
		copy(f[:w], g[y*w:(y+1)*w])
		dt1d(f, d, v, z, w)
		copy(g[y*w:(y+1)*w], d[:w])
	}
	return g
}

// ---------- 边界和统计 ----------

// boundaryMask：属于类 c 且 4 邻域里有（图内的）非 c 像素的像素。
// 图的外框不算边界，所以被画面裁掉的那一侧没有边界线。
func boundaryMask(labels []uint8, w, h int, c uint8) []bool {
	out := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if labels[i] != c {
				continue
			}
			if (x > 0 && labels[i-1] != c) || (x < w-1 && labels[i+1] != c) ||
				(y > 0 && labels[i-w] != c) || (y < h-1 && labels[i+w] != c) {
				out[i] = true
			}
		}
	}
	return out
}

// distancesTo：from 里每个边界像素到 to 的边界的最近距离（像素）。
// to 一个边界像素都没有时取整张图的对角线长度当"够不着"的距离，不返回 0 也不丢掉。
func distancesTo(from, to []bool, w, h int) []float64 {
	d2 := edtSquared(to, w, h)
	capD := math.Hypot(float64(w), float64(h))
	var out []float64
	for i, b := range from {
		if !b {
			continue
		}
		if d2[i] >= edtInf/2 {
			out = append(out, capD)
		} else {
			out = append(out, math.Sqrt(d2[i]))
		}
	}
	return out
}

type DistStats struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean"`
	Max  float64 `json:"max"`
	P99  float64 `json:"p99"`
}

func summarize(v []float64) DistStats {
	if len(v) == 0 {
		return DistStats{}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	sum := 0.0
	for _, x := range s {
		sum += x
	}
	return DistStats{N: len(s), Mean: sum / float64(len(s)), Max: s[len(s)-1], P99: percentileSorted(s, 0.99)}
}

// percentileSorted：线性插值的分位数（和 numpy 默认的一样）。
func percentileSorted(s []float64, q float64) float64 {
	if len(s) == 0 {
		return 0
	}
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if hi >= len(s) {
		hi = len(s) - 1
	}
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}
