package chatui

import (
	"image"
	"math"
	"runtime"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Wallpaper 是聊天区的"柔和"背景（Bun chat.css:46-51）：20 DIP 格、1 DIP 波点（正文色 9%，羽化到 1.6），
// 下面三团角色色椭圆光晕。整张图在 CPU 上画一次，尺寸、主题、角色色不变就一直复用；每帧只贴图。
// 一个 Wallpaper 只给一个元素用（它记着自己上次画的尺寸）。
type Wallpaper struct {
	key  wallKey
	img  *image.RGBA
	bm   *ui.Bitmap
	Hits int // 重画次数，测试和帧统计看它
}

type wallKey struct {
	w, h      int
	base, dot ui.Color
	glow      OKLCH
}

// glowEnd 是光晕淡到透明的位置占椭圆半径的比例（Bun 的 transparent 72%；审查 B9）。
const glowEnd = 0.72

// 光晕：中心（占宽高的比例）、半径（占宽高的比例）、浓度（Bun 的百分比）。
var glows = []struct{ cx, cy, rx, ry, a float64 }{
	{0.70, 0.18, 0.60, 0.55, 0.26},
	{0.95, 0.85, 0.55, 0.60, 0.18},
	{0.05, 0.90, 0.45, 0.50, 0.14},
}

// Paint 把壁纸铺满 r；在 Element.Draw 里调用。
func (w *Wallpaper) Paint(p *ui.Painter, r ui.Rect, t Tokens, glow OKLCH) {
	scale := p.Scale()
	pw, ph := int(math.Ceil(float64(r.W*scale))), int(math.Ceil(float64(r.H*scale)))
	if pw <= 0 || ph <= 0 {
		return
	}
	k := wallKey{pw, ph, t.C.ChatBg, t.C.DotInk, glow}
	if k != w.key || w.bm == nil {
		w.key = k
		w.Hits++
		w.img = renderWallpaper(pw, ph, float64(scale), t.C.ChatBg, t.C.DotInk, t.AccentOf(glow).Base)
		if w.bm == nil {
			w.bm = ui.NewBitmap(w.img)
		} else {
			w.bm.Update(w.img)
		}
	}
	p.Image(w.bm, r, ui.FillBox)
}

// renderWallpaper 画整张壁纸：底色 → 三团光晕（从中心的浓度线性淡到椭圆半径的 72% 处为零，照 Bun chat.css:48-50 的 transparent 72%）→ 波点，都是 source-over。
// 光晕是平滑渐变，先在 1/4 分辨率上算再双线性放大；波点只盖每个点附近的几个像素；按行分给各核。
func renderWallpaper(w, h int, scale float64, base, dot, accent ui.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	br, bg, bb := float64(base.R), float64(base.G), float64(base.B)
	ar, ag, ab := float64(accent.R), float64(accent.G), float64(accent.B)
	// 1. 低分辨率光晕：每个格点存叠完三团以后"往角色色走了多少"（0～1）。
	const q = 4
	gw, gh := w/q+2, h/q+2
	amt := make([]float64, gw*gh)
	fw, fh := float64(w), float64(h)
	parallel(gh, func(y0, y1 int) {
		for gy := y0; gy < y1; gy++ {
			fy := float64(gy*q) + 0.5
			for gx := 0; gx < gw; gx++ {
				fx := float64(gx*q) + 0.5
				keep := 1.0 // 剩下多少底色
				for _, gl := range glows {
					dx := (fx - gl.cx*fw) / (gl.rx * fw)
					dy := (fy - gl.cy*fh) / (gl.ry * fh)
					if d := math.Sqrt(dx*dx+dy*dy) / glowEnd; d < 1 {
						keep *= 1 - gl.a*(1-d)
					}
				}
				amt[gy*gw+gx] = 1 - keep
			}
		}
	})
	// 2. 放大写进整张图。
	parallel(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			fy := float64(y) / q
			iy := int(fy)
			ty := fy - float64(iy)
			for x := 0; x < w; x++ {
				fx := float64(x) / q
				ix := int(fx)
				tx := fx - float64(ix)
				a00, a10 := amt[iy*gw+ix], amt[iy*gw+ix+1]
				a01, a11 := amt[(iy+1)*gw+ix], amt[(iy+1)*gw+ix+1]
				a := (a00*(1-tx)+a10*tx)*(1-ty) + (a01*(1-tx)+a11*tx)*ty
				i := img.PixOffset(x, y)
				img.Pix[i] = uint8(br + (ar-br)*a + 0.5)
				img.Pix[i+1] = uint8(bg + (ag-bg)*a + 0.5)
				img.Pix[i+2] = uint8(bb + (ab-bb)*a + 0.5)
				img.Pix[i+3] = 255
			}
		}
	})
	// 3. 波点：每格左上角 (1,1) DIP 处一个，实心半径 1、羽化到 1.6。
	dr, dg, db, da := float64(dot.R), float64(dot.G), float64(dot.B), float64(dot.A)/255
	cell := 20 * scale
	inner, outer := 1*scale, 1.6*scale
	reach := int(math.Ceil(outer)) + 1
	rows := int(fh/cell) + 1
	parallel(rows, func(r0, r1 int) {
		for row := r0; row < r1; row++ {
			cy := float64(row)*cell + scale
			for cx := scale; cx < fw+cell; cx += cell {
				for y := int(cy) - reach; y <= int(cy)+reach; y++ {
					if y < 0 || y >= h {
						continue
					}
					for x := int(cx) - reach; x <= int(cx)+reach; x++ {
						if x < 0 || x >= w {
							continue
						}
						dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
						d := math.Sqrt(dx*dx + dy*dy)
						if d >= outer {
							continue
						}
						a := da
						if d > inner {
							a *= (outer - d) / (outer - inner)
						}
						i := img.PixOffset(x, y)
						img.Pix[i] = uint8(float64(img.Pix[i]) + (dr-float64(img.Pix[i]))*a + 0.5)
						img.Pix[i+1] = uint8(float64(img.Pix[i+1]) + (dg-float64(img.Pix[i+1]))*a + 0.5)
						img.Pix[i+2] = uint8(float64(img.Pix[i+2]) + (db-float64(img.Pix[i+2]))*a + 0.5)
					}
				}
			}
		}
	})
	return img
}

// parallel 把 [0,n) 切成几段分给各核，等全部做完再返回。
func parallel(n int, fn func(lo, hi int)) {
	k := min(runtime.NumCPU(), n)
	if k <= 1 {
		fn(0, n)
		return
	}
	var wg sync.WaitGroup
	step := (n + k - 1) / k
	for lo := 0; lo < n; lo += step {
		wg.Add(1)
		go func(lo, hi int) { defer wg.Done(); fn(lo, hi) }(lo, min(n, lo+step))
	}
	wg.Wait()
}
