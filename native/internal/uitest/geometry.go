// Package uitest 是界面测试共用的工具：读 Tester 已提交的元素树，查几何规矩。
// 只给测试用；读的是固定版本 mygo 的内部字段，升级后读不到会 panic，要跟着改。
// 调研见 docs/notes/调研-界面测试分层-2026-11-08.md 第九节。
package uitest

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 唯一布局例外名单（主人 2026-11-06 批准，ADR-285 第 11 条；没用到的"居中列"已删）。
// 每条写明仍然执行的检查，代码里照这句真的查，记录（Pair.Checks）也照实写。
const Exceptions = `已批|virtual-scroll|虚拟列表内容可越过滚动视口：上下边距和行间距不按内边距查、不和滚动容器外面的圆角查同心，仍查左右边距
已批|floating-control|浮栏、遮罩、选择浮层脱离文档流：不查和兄弟的间距、不查父的内边距，仍查不越出所属容器`

const (
	ExceptionScroll   = 0
	ExceptionFloating = 1
)

// v0.7.4的Tester没有公开节点遍历；只在测试读固定版本的已提交树。
// 不使用unsafe，不改mygo，不依赖产品的surface登记来决定检查覆盖。
type Node struct {
	Key, Widget, Text, Label               string
	Box                                    ui.Rect
	Pad, Radius, Margin                    [4]float32
	GapX, GapY                             float32
	Row, Absolute, Painted, Hidden, Scroll bool
	// 下面几项给 boxes.go 的检查读：裁剪轴、不可见、透明、可点、文字自然宽等。
	ClipX, ClipY, Invisible, Inert, AccessHidden, IgnoreInput, PassThrough  bool
	Clickable, Focusable, Hover, Selectable, Editable, HasHit, HasClipInset bool
	Image, Moved, OpacitySet, WidthFill, Disabled                           bool
	Kind                                                                    uint8 // 0 盒子 1 文字 2 图片 3 输入 4 图标
	Opacity                                                                 float32
	TextW                                                                   float32 // 文字最长一行的宽（排版结果）
	TextLines                                                               int
	MaxLines                                                                int
	scrollX, scrollY, Modal                                                 bool
	Parent                                                                  *Node   `json:"-"`
	Children                                                                []*Node `json:"-"`
}
type Pair struct {
	Scene, Child, Parent string
	Distances            [4]float32
	Outer, Inner         [4]float32
	Padding              [4]float32
	Checks               []string
	LayoutException      string
}

func number(v reflect.Value, name string) float32 { return float32(v.FieldByName(name).Float()) }
func vector(v reflect.Value, name string) (out [4]float32) {
	f := v.FieldByName(name)
	for i := range out {
		out[i] = float32(f.Index(i).Float())
	}
	return
}
func Tree(tt *ui.Tester) *Node {
	root := reflect.ValueOf(tt).Elem().FieldByName("rt").Elem().FieldByName("c").FieldByName("root")
	var walk func(reflect.Value, *Node) *Node
	walk = func(p reflect.Value, parent *Node) *Node {
		v := p.Elem()
		n := &Node{Parent: parent, Widget: v.FieldByName("widget").String(), Text: v.FieldByName("text").String(), Label: v.FieldByName("label").String(), Box: ui.Rect{X: number(v, "x"), Y: number(v, "y"), W: number(v, "w"), H: number(v, "h")}, Pad: vector(v, "pad"), Radius: vector(v, "radius"), GapX: number(v, "gapX"), GapY: number(v, "gapY"), Row: v.FieldByName("row").Bool(), Hidden: v.FieldByName("hidden").Bool()}
		n.Margin = vector(v, "margin")
		n.scrollX, n.scrollY = v.FieldByName("flags").Uint()&(1<<4) != 0, v.FieldByName("flags").Uint()&(1<<5) != 0
		n.Scroll = v.FieldByName("flags").Uint()&((1<<4)|(1<<5)) != 0
		key := v.FieldByName("key")
		if !key.IsNil() {
			k := key.Elem()
			if k.Kind() == reflect.String {
				n.Key = k.String()
			}
		}
		n.Absolute = v.FieldByName("flags").Uint()&(1<<8) != 0
		flags := v.FieldByName("flags").Uint()
		n.ClipX, n.ClipY = flags&(1<<6) != 0, flags&(1<<7) != 0
		n.Clickable, n.Focusable, n.Editable = flags&(1<<0) != 0, flags&(1<<1) != 0, flags&(1<<2) != 0
		n.Disabled, n.PassThrough, n.Hover, n.Selectable = flags&(1<<9) != 0, flags&(1<<11) != 0, flags&(1<<13) != 0, flags&(1<<17) != 0
		n.Invisible, n.Inert, n.Modal = flags&(1<<18) != 0, flags&(1<<27) != 0, flags&(1<<22) != 0
		n.AccessHidden, n.IgnoreInput = v.FieldByName("accessHidden").Bool(), v.FieldByName("ignoreInput").Bool()
		n.HasHit, n.HasClipInset = !v.FieldByName("hitFn").IsNil(), !v.FieldByName("clipping").IsNil()
		n.Image = !v.FieldByName("image").IsNil()
		n.Kind = uint8(v.FieldByName("kind").Uint())
		n.Opacity, n.OpacitySet = float32(v.FieldByName("opacity").Float()), v.FieldByName("opacitySet").Bool()
		if w := v.FieldByName("world"); w.FieldByName("Set").Bool() { // 变换：只有真的位移、缩放、旋转才算；一两像素的字形校正（textNudge）框不变，不算
			f := func(name string) float64 { return w.FieldByName(name).Float() }
			n.Moved = math.Abs(f("A")-1) > 0.01 || math.Abs(f("D")-1) > 0.01 || math.Abs(f("B")) > 0.01 || math.Abs(f("C")) > 0.01 || math.Abs(f("X")) > 2 || math.Abs(f("Y")) > 2
		}
		if tl := v.FieldByName("tl"); !tl.IsNil() { // 文字排好版的结果：最长一行多宽、几行
			n.TextW, n.TextLines = float32(tl.Elem().FieldByName("Width").Float()), tl.Elem().FieldByName("Lines").Len()
		}
		n.MaxLines = int(v.FieldByName("maxLines").Int())
		w := v.FieldByName("width")
		n.WidthFill = w.FieldByName("u").Uint() == 2 && w.FieldByName("v").Float() >= 99.9 // 宽度写的是 100%（FillWidth）
		n.Painted = v.FieldByName("bg").FieldByName("A").Uint() != 0
		border := vector(v, "border")
		n.Painted = n.Painted || (border[0] > 0 && border[1] > 0 && border[2] > 0 && border[3] > 0)
		for ch := v.FieldByName("first"); !ch.IsNil(); ch = ch.Elem().FieldByName("next") {
			n.Children = append(n.Children, walk(ch, n))
		}
		return n
	}
	return walk(root, nil)
}
func (n *Node) Name() string {
	if n.Key != "" {
		return n.Key
	}
	if n.Label != "" {
		return n.Label
	}
	if n.Text != "" {
		return n.Text
	}
	return n.Widget
}
func (n *Node) Find(key string) *Node {
	if n.Key == key {
		return n
	}
	for _, ch := range n.Children {
		if got := ch.Find(key); got != nil {
			return got
		}
	}
	return nil
}
func Near(a, b float32) bool { return math.Abs(float64(a-b)) <= 1 }
func Distances(p, b ui.Rect) [4]float32 {
	return [4]float32{b.Y - p.Y, p.X + p.W - b.X - b.W, p.Y + p.H - b.Y - b.H, b.X - p.X}
}
func Contains(a, b ui.Rect) bool {
	d := Distances(a, b)
	return d[0] >= -1 && d[1] >= -1 && d[2] >= -1 && d[3] >= -1
}
func Equal(t *testing.T, scene, what string, got, want float32) {
	t.Helper()
	if !Near(got, want) {
		t.Errorf("%s %s = %.2f，期望 %.2f", scene, what, got, want)
	}
}

// CheckTree 查一棵树：每层四边 padding 同档、子不越过父的内边距、兄弟实际间距等于声明的 gap、圆角不过半高、贴边的角同心。
// innerMin 是同心时内圆角的下限（令牌 RInnerMin）。
func CheckTree(t *testing.T, root *Node, scene string, innerMin float32, pairs *[]Pair) {
	var walk func(*Node, *Node)
	walk = func(n, parent *Node) {
		if n.Hidden || n.Box.W <= 0 || n.Box.H <= 0 {
			return
		}
		// 每层自身四边都必须同档；直角父层和单行控件不豁免。
		// 文字行例外地按"字"而不是"盒子"算四边等距：行框上下自带半个行距的空白，所以文字只在左右补同样的
		// 内边距（上下 0、左右相等），画出来字到四边才等距——这是同一条规矩换成按字量，不是给哪个组件开口子；
		// 左右这个值是否正好等于半个行距，由 internal/chatui 的按像素测试（TestAuditSpacing 等）检查。
		textLine := n.Text != "" && Near(n.Pad[0], 0) && Near(n.Pad[2], 0) && Near(n.Pad[1], n.Pad[3])
		if !strings.HasPrefix(n.Key, "messages-") && !textLine {
			for side := 1; side < len(n.Pad); side++ {
				Equal(t, scene, n.Name()+" 四边padding", n.Pad[side], n.Pad[0])
			}
		}
		// 透明包装层也逐对保留原始四边，并用真实相邻兄弟核块间距。
		var previous *Node
		for _, child := range n.Children {
			if child.Hidden || child.Box.W <= 0 || child.Box.H <= 0 {
				continue
			}
			pair := Pair{Scene: scene, Child: child.Name(), Parent: n.Name(), Distances: Distances(n.Box, child.Box), Outer: n.Radius, Inner: child.Radius, Padding: n.Pad, Checks: []string{"实际父子四边", "自身padding", "兄弟间距"}}
			if child.Absolute {
				pair.LayoutException = strings.Split(Exceptions, "\n")[ExceptionFloating]
				pair.Checks = []string{"不越出所属容器"}
			} else if n.Scroll {
				pair.LayoutException = strings.Split(Exceptions, "\n")[ExceptionScroll]
				pair.Checks = []string{"左右父子边距", "自身padding"}
			}
			*pairs = append(*pairs, pair)
			if child.Absolute {
				// 浮层：仍查不越出所属容器（四边距离不为负）
				for side, d := range pair.Distances {
					if d < -1 {
						t.Errorf("%s %s → %s 浮层第%d边越出所属容器 %.2f", scene, n.Name(), child.Name(), side, -d)
					}
				}
				continue
			}
			for side, d := range pair.Distances {
				if n.Scroll && (side == 0 || side == 2) {
					continue
				}
				if d < n.Pad[side]-1 {
					t.Errorf("%s %s → %s 第%d边距离%.2f小于骨架内边距%.2f", scene, n.Name(), child.Name(), side, d, n.Pad[side])
				}
			}
			if previous != nil && !n.Scroll {
				if n.Row {
					if Near(child.Box.Y, previous.Box.Y) {
						Equal(t, scene, n.Name()+" 横向实际间距", child.Box.X-previous.Box.X-previous.Box.W-child.Margin[3]-previous.Margin[1], n.GapX)
					}
				} else {
					Equal(t, scene, n.Name()+" 纵向实际间距", child.Box.Y-previous.Box.Y-previous.Box.H-child.Margin[0]-previous.Margin[2], n.GapY)
				}
			}
			previous = child
		}
		for _, r := range n.Radius {
			if r > min(n.Box.W, n.Box.H)/2+1 {
				t.Errorf("%s %s 圆角%.2f超过半高 %+v", scene, n.Name(), r, n.Box)
			}
		}
		if n.Painted {
			p := Pair{Scene: scene, Child: n.Name(), Inner: n.Radius, Padding: n.Pad, Checks: []string{"四边padding", "半高限制"}}
			if parent != nil {
				p.Parent, p.Distances, p.Outer = parent.Name(), Distances(parent.Box, n.Box), parent.Radius
				p.Checks = append(p.Checks, "四边原始距离", "贴边对应角")
				// 逐对检查与父轮廓相邻的角。远离角的独立内容不伪称共心。
				// 正圆（宽高相等、四角都是半径，如头像、圆点）是图形不是容器，没有"角"可以同心：
				// 圆角行里放圆头像不做同心检查（Telegram、iOS 列表都这样；主人 11-06 要会话行用圆角不用胶囊）。
				circle := Near(n.Box.W, n.Box.H) && Near(n.Radius[0], n.Box.W/2) && Near(n.Radius[1], n.Box.W/2) && Near(n.Radius[2], n.Box.W/2) && Near(n.Radius[3], n.Box.W/2)
				for corner, sides := range [4][2]int{{0, 3}, {0, 1}, {2, 1}, {2, 3}} {
					if circle {
						break
					}
					x, y := p.Distances[sides[0]], p.Distances[sides[1]]
					if parent.Radius[corner] > 0 && x >= 0 && y >= 0 && x < parent.Radius[corner]-0.5 && y < parent.Radius[corner]-0.5 { // 离边正好一个圆角半径算在角外（留半像素给浮点误差）
						// 零距满铺层由外壳Clip给出实际外角，按裁切后的轮廓计算。
						if x <= 1 && y <= 1 {
							continue
						}
						Equal(t, scene, n.Name()+" 对应角四边距离", x, y)
						Equal(t, scene, n.Name()+" 对应角同心", n.Radius[corner], max(innerMin, parent.Radius[corner]-min(x, y))) // 内层圆角下限固定 4（规格"内层圆角 max(4, 外层 − 距离)"），不随疏密缩放
					}
				}
			}
			*pairs = append(*pairs, p)
			parent = n
		}
		for _, ch := range n.Children {
			if n.Scroll {
				walk(ch, nil) // 滚动内容随滚动移动，和外面那层的圆角没有固定关系，不查同心（例外 virtual-scroll）
				continue
			}
			walk(ch, parent)
		}
	}
	walk(root, nil)
}

// ContentLeft 是一块里真正画出来的内容（字、图标）的最左边，不是外层盒子的边：叶子是盒子左边加左内距。
// 跨容器比“同一级内容对齐”用它，比如输入框的字和底行药丸里的图标。
func (n *Node) ContentLeft() float32 {
	best, found := float32(0), false
	for _, ch := range n.Children {
		if ch.Hidden || ch.Absolute || ch.Box.W <= 0 || ch.Box.H <= 0 {
			continue
		}
		if x := ch.ContentLeft(); !found || x < best {
			best, found = x, true
		}
	}
	if found {
		return best
	}
	return n.Box.X + n.Pad[3]
}

// ContentTop 和 ContentLeft 同理，是内容的最上边。
func (n *Node) ContentTop() float32 {
	best, found := float32(0), false
	for _, ch := range n.Children {
		if ch.Hidden || ch.Absolute || ch.Box.W <= 0 || ch.Box.H <= 0 {
			continue
		}
		if y := ch.ContentTop(); !found || y < best {
			best, found = y, true
		}
	}
	if found {
		return best
	}
	return n.Box.Y + n.Pad[0]
}

// Walk 从这个节点起依次走每个节点。
func (n *Node) Walk(fn func(*Node)) {
	fn(n)
	for _, ch := range n.Children {
		ch.Walk(fn)
	}
}
