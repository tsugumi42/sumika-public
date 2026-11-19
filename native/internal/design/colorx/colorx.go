// Package colorx 是设计令牌的颜色计算（规格-设计令牌 3.3 的 palette）：oklch 换 sRGB、对比度、
// 按对比度推明度、叠色。生成器和运行时共用这一份，算法照 chatui/color.go、tokens.go 原样搬来。
package colorx

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// OKLCH 是 oklch 颜色；L 0～1，C 彩度，H 色相（度），A 不透明度。
type OKLCH struct{ L, C, H, A float64 }

// New 是不透明的 oklch 颜色。
func New(l, c, h float64) OKLCH { return OKLCH{l, c, h, 1} }

type oklab struct{ L, a, b float64 }

func (o OKLCH) lab() oklab {
	r := o.H * math.Pi / 180
	return oklab{o.L, o.C * math.Cos(r), o.C * math.Sin(r)}
}

// linear 把 oklab 换成线性 sRGB（Björn Ottosson 的矩阵）。
func (p oklab) linear() (float64, float64, float64) {
	l_ := p.L + 0.3963377774*p.a + 0.2158037573*p.b
	m_ := p.L - 0.1055613458*p.a - 0.0638541728*p.b
	s_ := p.L - 0.0894841775*p.a - 1.2914855480*p.b
	l, m, s := l_*l_*l_, m_*m_*m_, s_*s_*s_
	return 4.0767416621*l - 3.3077115913*m + 0.2309699292*s,
		-1.2684380046*l + 2.6097574011*m - 0.3413193965*s,
		-0.0041960863*l - 0.7034186147*m + 1.7076147010*s
}

func inGamut(r, g, b float64) bool {
	const e = 1e-4
	return r >= -e && r <= 1+e && g >= -e && g <= 1+e && b >= -e && b <= 1+e
}

func encode(x float64) uint8 {
	x = math.Min(1, math.Max(0, x))
	if x <= 0.0031308 {
		x *= 12.92
	} else {
		x = 1.055*math.Pow(x, 1/2.4) - 0.055
	}
	return uint8(math.Round(x * 255))
}

func decode(v uint8) float64 {
	x := float64(v) / 255
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

// Color 换成 sRGB；超出色域时保持明度和色相、降彩度（二分），和 CSS 的色域映射同一思路。
func (o OKLCH) Color() ui.Color {
	p := o.lab()
	r, g, b := p.linear()
	if !inGamut(r, g, b) {
		lo, hi := 0.0, o.C
		for range 24 {
			mid := (lo + hi) / 2
			if rr, gg, bb := (OKLCH{o.L, mid, o.H, o.A}).lab().linear(); inGamut(rr, gg, bb) {
				lo = mid
			} else {
				hi = mid
			}
		}
		r, g, b = OKLCH{o.L, lo, o.H, o.A}.lab().linear()
	}
	return ui.RGBA(encode(r), encode(g), encode(b), float32(o.A))
}

// FromColor 把 sRGB 颜色换回 oklch（Björn Ottosson 的矩阵），透明度照搬；Color 的反方向。
func FromColor(c ui.Color) OKLCH {
	r, g, b := decode(c.R), decode(c.G), decode(c.B)
	l := math.Cbrt(0.4122214708*r + 0.5363434216*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	L := 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
	A := 1.9779984951*l - 2.4285922050*m + 0.4505937099*s
	B := 0.0259040371*l + 0.7827717662*m - 0.8086757660*s
	return OKLCH{L, math.Hypot(A, B), math.Atan2(B, A) * 180 / math.Pi, float64(c.A) / 255}
}

// WithAlpha 返回同色、不透明度为 a 的颜色。
func (o OKLCH) WithAlpha(a float64) OKLCH { o.A = a; return o }

func luminance(c ui.Color) float64 {
	return 0.2126*decode(c.R) + 0.7152*decode(c.G) + 0.0722*decode(c.B)
}

// Contrast 是前景压在不透明背景上的 WCAG 2 对比度；前景带透明度时先叠到背景上。
func Contrast(fg, bg ui.Color) float64 {
	if fg.A < 255 {
		fg = fg.Over(bg)
	}
	a, b := luminance(fg), luminance(bg)
	if a < b {
		a, b = b, a
	}
	return (a + 0.05) / (b + 0.05)
}

// Over 是 fg 以不透明度 k 盖在 bg 上的颜色（sRGB 里混合，和绘制时一样）。
func Over(fg ui.Color, k float64, bg ui.Color) ui.Color {
	m := func(x, y uint8) uint8 { return uint8(float64(x)*k + float64(y)*(1-k) + .5) }
	return ui.Color{R: m(fg.R, bg.R), G: m(fg.G, bg.G), B: m(fg.B, bg.B), A: 255}
}

// Reach 让颜色对所有底色都达到 min 对比度：已经达到就原样返回，否则只沿 dir（+1 变亮、-1 变暗）调明度，
// 取刚好过线的那一档；推到头也不达标时 ok 为 false。
func Reach(o OKLCH, dir, min float64, bgs ...ui.Color) (OKLCH, bool) {
	pass := func(l float64) bool {
		c := OKLCH{l, o.C, o.H, 1}.Color()
		for _, bg := range bgs {
			if Contrast(c, bg) < min {
				return false
			}
		}
		return true
	}
	if pass(o.L) {
		return o, true
	}
	near, far := o.L, 0.0
	if dir > 0 {
		far = 1
	}
	if !pass(far) {
		return o, false
	}
	for range 40 {
		mid := (near + far) / 2
		if pass(mid) {
			far = mid
		} else {
			near = mid
		}
	}
	return OKLCH{far, o.C, o.H, 1}, true
}

// Strong 解“整块填强调色、上面放白字”的底：色相、彩度跟 char，只调明度，取白字和 muted 透明度的白色次要字
// 都达到 min 的最亮一档；char 本身够深就用它。
func Strong(char OKLCH, min, muted float64) (bg, on, onMuted ui.Color) {
	white := ui.Color{R: 255, G: 255, B: 255, A: 255}
	pass := func(l float64) bool {
		c := OKLCH{l, char.C, char.H, 1}.Color()
		return Contrast(white, c) >= min && Contrast(Over(white, muted, c), c) >= min
	}
	lo, hi := 0.0, char.L
	if !pass(hi) {
		for range 40 {
			mid := (lo + hi) / 2
			if pass(mid) {
				lo = mid
			} else {
				hi = mid
			}
		}
		hi = lo
	}
	bg = OKLCH{hi, char.C, char.H, 1}.Color()
	return bg, white, Over(white, muted, bg)
}
