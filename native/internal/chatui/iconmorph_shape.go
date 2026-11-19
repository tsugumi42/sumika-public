package chatui

import (
	"math"
	"regexp"
	"sort"
	"strconv"
)

// 图标变形的纯算法（Fluid 的 IconMorph / Metamorph，笔记在 data/dev/design-language-20261114/17-动画参考/Fluid-笔记.md）：
// 把图标的填充轮廓沿边均匀撒点，两个图标的轮廓按大小配对，每个点从一个形状的位置流到另一个形状的位置。
//
// 为什么在这里而不是别处：mygo 的 ui.Path 能画任意路径（Painter.FillPath），ui.ParsePath 能解析 d，但 Path 里的点是私有的、
// 没法取出来撒点，所以路径数据自己解析（下面的 flattenPath，和 mygo 的 ParsePath 画出来对照过，见 iconmorph_test.go）。
// 图标数据来自 assets/icons 的 MingCute：线性版也都是填充的轮廓（fill="currentColor"），正好是 Fluid 要求的“只变形填充形状”；
// 描边写法的（ownSVG 里的 add、minus、checks 等少数）不变形，退回 mygo 自带的“旧的缩走、新的长出来”。

const (
	// morphPoints 是每条轮廓撒多少个点。Fluid 整个图标撒 96 个；我们每条轮廓都撒这么多，配对的两条点数一样，才能一对一流。
	morphPoints = 96
	// morphCurveSteps 是把一段曲线先切成几小段再量弧长（量得越细，撒的点越均匀）；图标的曲线都很短，16 足够。
	morphCurveSteps = 16
	// morphStagger 是各点“出发时间”的最大差，占整个行程的比例。Fluid 让每个点的弹簧频率略有差异，所以轮廓像液体流过去而不是整块平移；
	// 我们只有一根进度弹簧，用错开出发时间做同样的效果，差异取小：太大图形会拧成一团。0 就是整块一起动。
	morphStagger = 0.12
	// morphWaves 是出发时间沿轮廓起伏几个来回：起伏平滑，相邻的点出发时间接近。
	morphWaves = 2
)

type mpt struct{ x, y float32 }

// contour 是一条闭合轮廓：morphPoints 个点，按弧长均匀。
type contour struct {
	p [morphPoints]mpt
}

// Shape 是一个图标的全部轮廓（一个 d 里的每条子路径是一条），坐标在 24×24 的格子里。
type Shape struct {
	cs []contour
}

var (
	rePathD  = regexp.MustCompile(`\sd="([^"]*)"`)
	reStroke = regexp.MustCompile(`\sstroke="[^n"]`) // stroke="currentColor" 之类；stroke="none" 不算
)

// ParseShape 从 <svg> 里面的路径文本（assets/icons.Body 的样子）取出轮廓。
// 不是纯填充的（有描边）、没有路径、路径解不出来，返回 false：调用方退回别的换法。
func ParseShape(body string) (*Shape, bool) {
	if reStroke.MatchString(body) {
		return nil, false
	}
	var sh Shape
	for _, m := range rePathD.FindAllStringSubmatch(body, -1) {
		subs, err := flattenPath(m[1])
		if err != nil {
			return nil, false
		}
		for _, sp := range subs {
			if len(sp) < 3 {
				continue
			}
			sh.cs = append(sh.cs, resample(sp))
		}
	}
	if len(sh.cs) == 0 {
		return nil, false
	}
	return &sh, true
}

// 路径数据的解析 ----------------------------------------------------------------------------------------

type pathReader struct {
	s   string
	i   int
	err error
}

func (r *pathReader) skipSep() {
	for r.i < len(r.s) && (r.s[r.i] == ' ' || r.s[r.i] == ',' || r.s[r.i] == '\t' || r.s[r.i] == '\n' || r.s[r.i] == '\r') {
		r.i++
	}
}

// more 说后面还有没有参数（下一个字符不是命令字母）。
func (r *pathReader) more() bool {
	r.skipSep()
	if r.i >= len(r.s) {
		return false
	}
	c := r.s[r.i]
	return c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9')
}

// num 读一个数：符号、整数部分、小数部分、指数；“0-2”“.883.5”这种紧挨着的写法在第二个符号或第二个小数点处断开。
func (r *pathReader) num() float64 {
	r.skipSep()
	st := r.i
	if r.i < len(r.s) && (r.s[r.i] == '-' || r.s[r.i] == '+') {
		r.i++
	}
	digits := 0
	for r.i < len(r.s) && r.s[r.i] >= '0' && r.s[r.i] <= '9' {
		r.i++
		digits++
	}
	if r.i < len(r.s) && r.s[r.i] == '.' {
		r.i++
		for r.i < len(r.s) && r.s[r.i] >= '0' && r.s[r.i] <= '9' {
			r.i++
			digits++
		}
	}
	if digits > 0 && r.i < len(r.s) && (r.s[r.i] == 'e' || r.s[r.i] == 'E') {
		j := r.i + 1
		if j < len(r.s) && (r.s[j] == '-' || r.s[j] == '+') {
			j++
		}
		if j < len(r.s) && r.s[j] >= '0' && r.s[j] <= '9' {
			for j < len(r.s) && r.s[j] >= '0' && r.s[j] <= '9' {
				j++
			}
			r.i = j
		}
	}
	if digits == 0 {
		r.err = errPath
		return 0
	}
	v, err := strconv.ParseFloat(r.s[st:r.i], 64)
	if err != nil {
		r.err = err
	}
	return v
}

// flag 读圆弧的标志位：只有 0 或 1 一个字符，后面可以不加分隔（“a1 1 0 011 1”）。
func (r *pathReader) flag() float64 {
	r.skipSep()
	if r.i < len(r.s) && (r.s[r.i] == '0' || r.s[r.i] == '1') {
		v := float64(r.s[r.i] - '0')
		r.i++
		return v
	}
	r.err = errPath
	return 0
}

type pathErr string

func (e pathErr) Error() string { return string(e) }

const errPath = pathErr("iconmorph: 路径数据读不懂")

// flattenPath 把 d 切成一条条子路径的折线（曲线切成 morphCurveSteps 小段）。支持 M L H V C S Q T A Z 及小写（相对）。
func flattenPath(d string) (subs [][]mpt, err error) {
	r := &pathReader{s: d}
	var cur []mpt
	var cx, cy, sx, sy float64 // 当前点、子路径起点
	var lcx, lcy float64       // 上一条三次、二次曲线的控制点（S、T 用来反射）
	var prev byte
	flush := func() {
		if len(cur) > 0 {
			subs = append(subs, cur)
		}
		cur = nil
	}
	add := func(x, y float64) {
		cur = append(cur, mpt{float32(x), float32(y)})
		cx, cy = x, y
	}
	cubic := func(x1, y1, x2, y2, x, y float64) {
		x0, y0 := cx, cy
		for i := 1; i <= morphCurveSteps; i++ {
			t := float64(i) / morphCurveSteps
			u := 1 - t
			add(u*u*u*x0+3*u*u*t*x1+3*u*t*t*x2+t*t*t*x, u*u*u*y0+3*u*u*t*y1+3*u*t*t*y2+t*t*t*y)
		}
		lcx, lcy = x2, y2
	}
	var cmd byte
	for {
		r.skipSep()
		if r.i >= len(r.s) {
			break
		}
		if c := r.s[r.i]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			cmd = c
			r.i++
			if cmd == 'z' || cmd == 'Z' {
				if len(cur) > 0 {
					cx, cy = sx, sy
				}
				flush()
				prev = 'z'
				// z 之后接的不是 m 时，新子路径从起点继续
				continue
			}
		} else if !r.more() || cmd == 0 {
			return nil, errPath
		} else if cmd == 'M' {
			cmd = 'L' // 命令字母后多出来的坐标对，是隐含的 L（m 是 l）
		} else if cmd == 'm' {
			cmd = 'l'
		}
		rel := cmd >= 'a'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		switch cmd | 0x20 {
		case 'm':
			x, y := r.num()+ox, r.num()+oy
			flush()
			add(x, y)
			sx, sy = x, y
		case 'l':
			x, y := r.num()+ox, r.num()+oy
			if len(cur) == 0 {
				add(sx, sy)
			}
			add(x, y)
		case 'h':
			x := r.num() + ox
			if len(cur) == 0 {
				add(sx, sy)
			}
			add(x, cy)
		case 'v':
			y := r.num() + oy
			if len(cur) == 0 {
				add(sx, sy)
			}
			add(cx, y)
		case 'c':
			x1, y1, x2, y2, x, y := r.num()+ox, r.num()+oy, r.num()+ox, r.num()+oy, r.num()+ox, r.num()+oy
			if len(cur) == 0 {
				add(sx, sy)
			}
			cubic(x1, y1, x2, y2, x, y)
		case 's':
			x2, y2, x, y := r.num()+ox, r.num()+oy, r.num()+ox, r.num()+oy
			x1, y1 := cx, cy
			if p := prev | 0x20; p == 'c' || p == 's' {
				x1, y1 = 2*cx-lcx, 2*cy-lcy
			}
			if len(cur) == 0 {
				add(sx, sy)
			}
			cubic(x1, y1, x2, y2, x, y)
		case 'q', 't':
			var qx, qy, x, y float64
			if cmd|0x20 == 'q' {
				qx, qy, x, y = r.num()+ox, r.num()+oy, r.num()+ox, r.num()+oy
			} else {
				qx, qy = cx, cy
				if p := prev | 0x20; p == 'q' || p == 't' {
					qx, qy = 2*cx-lcx, 2*cy-lcy
				}
				x, y = r.num()+ox, r.num()+oy
			}
			if len(cur) == 0 {
				add(sx, sy)
			}
			x0, y0 := cx, cy
			cubic(x0+2.0/3*(qx-x0), y0+2.0/3*(qy-y0), x+2.0/3*(qx-x), y+2.0/3*(qy-y), x, y)
			lcx, lcy = qx, qy
		case 'a':
			rx, ry, rot, large, sweep := r.num(), r.num(), r.num(), r.flag(), r.flag()
			x, y := r.num()+ox, r.num()+oy
			if len(cur) == 0 {
				add(sx, sy)
			}
			arcToCubics(cx, cy, rx, ry, rot, large != 0, sweep != 0, x, y, cubic)
		default:
			return nil, errPath
		}
		if r.err != nil {
			return nil, r.err
		}
		prev = cmd
	}
	flush()
	return subs, nil
}

// arcToCubics 把圆弧（SVG 的端点写法）换成若干段三次曲线，交给 cubic（W3C SVG 实现说明 F.6.5 和常见的 90° 分段做法）。
func arcToCubics(x0, y0, rx, ry, rotDeg float64, large, sweep bool, x, y float64, cubic func(x1, y1, x2, y2, x, y float64)) {
	if x0 == x && y0 == y {
		return
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	if rx == 0 || ry == 0 {
		cubic(x0, y0, x, y, x, y) // 半径为零：直线
		return
	}
	phi := rotDeg * math.Pi / 180
	cp, sp := math.Cos(phi), math.Sin(phi)
	dx, dy := (x0-x)/2, (y0-y)/2
	x1p, y1p := cp*dx+sp*dy, -sp*dx+cp*dy
	if l := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry); l > 1 {
		s := math.Sqrt(l)
		rx, ry = rx*s, ry*s
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	co := 0.0
	if den != 0 && num > 0 {
		co = math.Sqrt(num / den)
	}
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1p/ry, -co*ry*x1p/rx
	cx, cy := cp*cxp-sp*cyp+(x0+x)/2, sp*cxp+cp*cyp+(y0+y)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
	}
	th1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dth := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dth > 0 {
		dth -= 2 * math.Pi
	} else if sweep && dth < 0 {
		dth += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(dth) / (math.Pi / 2)))
	if n < 1 {
		n = 1
	}
	step := dth / float64(n)
	t := 4.0 / 3 * math.Tan(step/4)
	pt := func(a float64) (float64, float64) {
		ex, ey := rx*math.Cos(a), ry*math.Sin(a)
		return cp*ex - sp*ey + cx, sp*ex + cp*ey + cy
	}
	dv := func(a float64) (float64, float64) {
		ex, ey := -rx*math.Sin(a), ry*math.Cos(a)
		return cp*ex - sp*ey, sp*ex + cp*ey
	}
	a := th1
	for i := 0; i < n; i++ {
		b := a + step
		ax, ay := pt(a)
		bx, by := pt(b)
		dax, day := dv(a)
		dbx, dby := dv(b)
		if i == n-1 {
			bx, by = x, y // 最后一段落在精确的终点上，不累积误差
		}
		cubic(ax+t*dax, ay+t*day, bx-t*dbx, by-t*dby, bx, by)
		a = b
	}
}

// 撒点和配对 ----------------------------------------------------------------------------------------------

// polyLen 是闭合折线的周长。
func polyLen(p []mpt) float64 {
	var l float64
	for i := range p {
		j := (i + 1) % len(p)
		l += math.Hypot(float64(p[j].x-p[i].x), float64(p[j].y-p[i].y))
	}
	return l
}

// resample 沿闭合折线按弧长均匀撒 morphPoints 个点，第一个点是折线的起点。
func resample(p []mpt) contour {
	var c contour
	total := polyLen(p)
	if total == 0 {
		for i := range c.p {
			c.p[i] = p[0]
		}
		return c
	}
	seg, acc := 0, 0.0 // 现在在第 seg 段，这一段之前累积的弧长
	segLen := func(i int) float64 {
		j := (i + 1) % len(p)
		return math.Hypot(float64(p[j].x-p[i].x), float64(p[j].y-p[i].y))
	}
	sl := segLen(0)
	for k := 0; k < morphPoints; k++ {
		target := total * float64(k) / morphPoints
		for acc+sl < target && seg < len(p)-1 {
			acc += sl
			seg++
			sl = segLen(seg)
		}
		f := 0.0
		if sl > 0 {
			f = (target - acc) / sl
		}
		j := (seg + 1) % len(p)
		c.p[k] = mpt{p[seg].x + float32(f)*(p[j].x-p[seg].x), p[seg].y + float32(f)*(p[j].y-p[seg].y)}
	}
	return c
}

func (c *contour) length() float64 {
	var l float64
	for i := range c.p {
		j := (i + 1) % morphPoints
		l += math.Hypot(float64(c.p[j].x-c.p[i].x), float64(c.p[j].y-c.p[i].y))
	}
	return l
}

func (c *contour) area() float64 { // 带符号面积：正负说明绕向
	var a float64
	for i := range c.p {
		j := (i + 1) % morphPoints
		a += float64(c.p[i].x)*float64(c.p[j].y) - float64(c.p[j].x)*float64(c.p[i].y)
	}
	return a / 2
}

func (c *contour) centroid() mpt {
	var x, y float64
	for _, p := range c.p {
		x += float64(p.x)
		y += float64(p.y)
	}
	return mpt{float32(x / morphPoints), float32(y / morphPoints)}
}

func (c *contour) reversed() contour {
	var r contour
	for i := range c.p {
		r.p[i] = c.p[(morphPoints-i)%morphPoints]
	}
	return r
}

func (c *contour) rotated(s int) contour {
	var r contour
	for i := range c.p {
		r.p[i] = c.p[(i+s)%morphPoints]
	}
	return r
}

// atPoint 是缩成（或从）一个点的轮廓：多出来的部件从一个点长出来、缩成一个点消失（Fluid 规则 3）。
func atPoint(m mpt) contour {
	var c contour
	for i := range c.p {
		c.p[i] = m
	}
	return c
}

// pair 是一对要互相流过去的轮廓：from 的第 i 个点流到 to 的第 i 个点。
type pair struct{ from, to contour }

// Morph 是从一个形状到另一个形状的配对结果，Frame 按进度给出中间的形状。
type Morph struct{ pairs []pair }

// NewMorph 配对两个形状：各自的轮廓按周长从大到小排，第 i 大对第 i 大；多出来的从目标（或起点）轮廓的中心一个点长出来（缩成一个点）。
// 配对后让两条轮廓绕向一致，并转动目标的起点使对应的点总距离最小，免得图形流到一半拧成麻花。
func NewMorph(a, b *Shape) *Morph {
	order := func(s *Shape) []contour {
		cs := append([]contour(nil), s.cs...)
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].length() > cs[j].length() })
		return cs
	}
	ca, cb := order(a), order(b)
	m := &Morph{}
	for i := 0; i < max(len(ca), len(cb)); i++ {
		var p pair
		switch {
		case i >= len(ca):
			p.to = cb[i]
			p.from = atPoint(p.to.centroid())
		case i >= len(cb):
			p.from = ca[i]
			p.to = atPoint(p.from.centroid())
		default:
			p.from, p.to = ca[i], cb[i]
			if (p.from.area() < 0) != (p.to.area() < 0) {
				p.to = p.to.reversed()
			}
			best, bestD := 0, math.MaxFloat64
			for s := 0; s < morphPoints; s++ {
				var d float64
				for k := 0; k < morphPoints; k++ {
					q := p.to.p[(k+s)%morphPoints]
					dx, dy := float64(q.x-p.from.p[k].x), float64(q.y-p.from.p[k].y)
					d += dx*dx + dy*dy
				}
				if d < bestD {
					best, bestD = s, d
				}
			}
			p.to = p.to.rotated(best)
		}
		m.pairs = append(m.pairs, p)
	}
	return m
}

// Frame 是进度 t（0 起点、1 终点，弹簧过冲时可以略大于 1）的形状。
func (m *Morph) Frame(t float32) *Shape {
	var sh Shape
	for _, p := range m.pairs {
		var c contour
		for i := range c.p {
			u := pointProgress(t, i)
			c.p[i] = mpt{p.from.p[i].x + u*(p.to.p[i].x-p.from.p[i].x), p.from.p[i].y + u*(p.to.p[i].y-p.from.p[i].y)}
		}
		sh.cs = append(sh.cs, c)
	}
	return &sh
}

// pointProgress 是第 i 个点的进度：整体进度 t 减去这个点的出发延迟，再按剩下的行程拉回 0～1；t 为 0 时都在起点，t 为 1 时都到终点。
func pointProgress(t float32, i int) float32 {
	d := float32(morphStagger * 0.5 * (1 + math.Sin(2*math.Pi*morphWaves*float64(i)/morphPoints)))
	if t <= d {
		return 0
	}
	return (t - d) / (1 - d)
}

// Points 是形状每条轮廓的点（给测试和画法用）。
func (s *Shape) Points() [][]mpt {
	out := make([][]mpt, len(s.cs))
	for i := range s.cs {
		out[i] = append([]mpt(nil), s.cs[i].p[:]...)
	}
	return out
}
