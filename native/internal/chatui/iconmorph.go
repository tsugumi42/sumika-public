package chatui

import (
	"sync"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/icons"
)

// Glyph 是能变形的图标：画静止状态用的 SVG，和变形用的轮廓。轮廓取不到（描边写法的图标、数据读不懂）时 Shape 为 nil，
// 这时 IconMorph 退回 mygo 自带的“旧图标缩走、新图标长出来”。
type Glyph struct {
	Key   string // 图标名加样式，区分“是不是同一个图标”
	SVG   *ui.SVG
	Shape *Shape
}

var (
	glyphMu    sync.Mutex
	glyphCache = map[string]Glyph{}
)

// MorphGlyph 按 Icon(name, fill) 一样的名字取图标和它的轮廓（解析一次，之后从缓存取）。
func MorphGlyph(name string, fill bool) Glyph {
	key := name
	if fill {
		key += "#fill"
	}
	glyphMu.Lock()
	if g, ok := glyphCache[key]; ok {
		glyphMu.Unlock()
		return g
	}
	glyphMu.Unlock()
	g := Glyph{Key: key, SVG: Icon(name, fill)}
	if body, ok := iconBody(name, fill); ok {
		g.Shape, _ = ParseShape(body)
	}
	glyphMu.Lock()
	glyphCache[key] = g
	glyphMu.Unlock()
	return g
}

// iconBody 是 Icon 取图标的同一套规则（先自己画的、再别名、再 MingCute 名）下的路径文本。
func iconBody(name string, fill bool) (string, bool) {
	if paths, ok := ownSVG[name]; ok {
		body := paths[0]
		if fill && paths[1] != "" {
			body = paths[1]
		}
		return body, body != ""
	}
	mc := name
	if a, ok := iconAlias[name]; ok {
		mc = a
	}
	body, err := icons.Body(mc, fill)
	return body, err == nil
}

// IconMorph 是一个会变形的图标位置的状态：这个位置换了图标，就从旧图标的轮廓流到新图标的轮廓。
// 一个位置用一个 IconMorph（比如代码块的复制钮一个），图标换了就自动变；换到一半又换，从当时的形状接着流，不跳。
// 画法和弹簧由调用方给（winshell 的 k.IconMorph 传 k.snappy()，按画廊慢放换算；chatui 里传 t.Spring.Snappy）。
type IconMorph struct {
	cur   Glyph   // 现在显示的（或正在变去的）图标
	from  *Shape  // 这一次变形的起点：上一个图标的轮廓，被打断时是当时的中间形状
	morph *Morph  // from → cur.Shape 的配对
	seq   int     // 变过几次；弹簧的目标就是它
	v0    float32 // 这一次变形开始时弹簧的值
	last  float32 // 上一帧弹簧的值和进度（被打断时用来取当时的形状）
	lastP float32
	moved bool // 上一帧在变形中
}

// Show 画这个位置的图标 g：和上次一样就是普通的图标元素；换了就变形，到位后又是普通的图标元素（和直接画 g 逐像素一样）。
// 系统要求减少动态效果时弹簧直接到终点，等于直接换图标。返回外层的盒子（size×size），调用方可以继续设它的外边距等。
func (m *IconMorph) Show(c *ui.Context, key string, g Glyph, size float32, color ui.Color, spring ui.SpringOptions) ui.Element {
	box := ui.Box(c.Key(key)).Size(size, size).Shrink(0).Center()
	if m.cur.SVG == nil {
		m.cur = g
	}
	canMorph := g.Shape != nil && m.cur.Shape != nil
	if g.Key != m.cur.Key {
		// 起点：没在变形就是上一个图标的轮廓；在变形中就是那一帧的中间形状
		var from *Shape
		if m.moved && m.morph != nil {
			from = m.morph.Frame(m.lastP)
		} else {
			from = m.cur.Shape
		}
		m.v0 = m.last
		m.seq++
		m.cur = g
		m.from = from
		m.morph = nil
		if canMorph && from != nil {
			m.morph = NewMorph(from, g.Shape)
		}
	}
	anim := ui.Animation{Spring: spring}
	v := box.AnimateTo("morph", float32(m.seq), anim)
	m.last = v
	moving := v != float32(m.seq)
	m.moved = moving && m.morph != nil
	if !moving {
		m.lastP = 1
	}
	switch {
	case !moving:
		box.Children(func() { ui.Icon(c.Key(key+"-icon"), g.SVG).Size(size, size).TextColor(color) })
	case m.morph == nil:
		// 没有轮廓可流：mygo 自己的替换（旧的缩走、新的长出来），弹簧还是预设
		box.Children(func() {
			ui.Icon(c.Key(key+"-icon"), g.SVG).Size(size, size).TextColor(color).ContentTransition(ui.ContentTransition{Animation: &ui.Animation{Spring: spring}})
		})
	default:
		p := float32(1)
		if den := float32(m.seq) - m.v0; den != 0 {
			p = (v - m.v0) / den
		}
		m.lastP = p
		shape := m.morph.Frame(p)
		scale := size / iconGrid
		box.Draw(func(pt *ui.Painter, r ui.Rect) {
			var path ui.Path
			for _, ct := range shape.cs {
				for i, q := range ct.p {
					if i == 0 {
						path.MoveTo(r.X+q.x*scale, r.Y+q.y*scale)
					} else {
						path.LineTo(r.X+q.x*scale, r.Y+q.y*scale)
					}
				}
				path.Close()
			}
			path.FillRule(ui.EvenOdd) // 配对的轮廓绕向已经对齐；不分洞和实，用奇偶规则，中间帧不会因为绕向翻转而把洞填上
			pt.FillPath(&path, color) // 点已经换成界面坐标：不用 Painter.Transform（挂在带缩放的按钮里时它画不出来，实测）
		})
	}
	return box
}

// iconGrid 是图标数据的格子：MingCute 和自己画的都是 24×24。
const iconGrid = 24
