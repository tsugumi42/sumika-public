// Package chatui 是 Sumika 聊天界面的令牌和组件（规格-界面令牌和消息气泡）。
// 颜色、尺寸都从四个外观设置推出来，组件里不写死数字。
package chatui

import (
	"math"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/design/colorx"
)

// OKLCH 是 oklch 颜色；L 0～1，C 彩度，H 色相（度），A 不透明度。
// mygo 的 Color.Mix 按 sRGB 插值，会把中性色和角色色混出偏色，所以混色都在这里按 oklab 做。
type OKLCH struct{ L, C, H, A float64 }

func oklch(l, c, h float64) OKLCH { return OKLCH{l, c, h, 1} }

// Design 换成 design 包的 OKLCH。
func (o OKLCH) Design() colorx.OKLCH { return colorx.OKLCH{L: o.L, C: o.C, H: o.H, A: o.A} }

type oklab struct{ L, a, b float64 }

func (o OKLCH) lab() oklab {
	r := o.H * math.Pi / 180
	return oklab{o.L, o.C * math.Cos(r), o.C * math.Sin(r)}
}

func (p oklab) lch(alpha float64) OKLCH {
	c := math.Hypot(p.a, p.b)
	h := math.Atan2(p.b, p.a) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	return OKLCH{p.L, c, h, alpha}
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
		for i := 0; i < 24; i++ {
			mid := (lo + hi) / 2
			q := OKLCH{o.L, mid, o.H, o.A}.lab()
			if rr, gg, bb := q.linear(); inGamut(rr, gg, bb) {
				lo = mid
			} else {
				hi = mid
			}
		}
		r, g, b = OKLCH{o.L, lo, o.H, o.A}.lab().linear()
	}
	return ui.RGBA(encode(r), encode(g), encode(b), float32(o.A))
}

// Mix 按 oklab 直线插值，t=0 是 o，t=1 是 x（对应 CSS 的 color-mix(in oklab, …)）。
func (o OKLCH) Mix(x OKLCH, t float64) OKLCH {
	a, b := o.lab(), x.lab()
	p := oklab{a.L + (b.L-a.L)*t, a.a + (b.a-a.a)*t, a.b + (b.b-a.b)*t}
	return p.lch(o.A + (x.A-o.A)*t)
}

// WithAlpha 返回同色、不透明度为 a 的颜色。
func (o OKLCH) WithAlpha(a float64) OKLCH { o.A = a; return o }

// luminance 是 sRGB 颜色的相对亮度（WCAG 2.x）。
func luminance(c ui.Color) float64 {
	return 0.2126*decode(c.R) + 0.7152*decode(c.G) + 0.0722*decode(c.B)
}

// Contrast 是前景压在不透明背景上的对比度（WCAG 2.x）；前景带透明度时先叠到背景上。
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
