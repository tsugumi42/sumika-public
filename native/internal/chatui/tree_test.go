package chatui

import (
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"
)

// node 是从 Tester 已提交的元素树里读出来的一个元素。
// mygo v0.7.4 的 Tester 没有公开遍历节点，测试按固定版本的字段读（和工作台 geometry_test 同一做法）；
// 升级 mygo 后这里读不到字段会直接 panic，要跟着改。
type node struct {
	Key, Label, Text string
	Features         string // 自己设的 OpenType 特性（ts.features）
	Box              ui.Rect
	Pad, Radius      [4]float32
	Opacity          float32
	Ink              [4]uint8 // 自己设的字色 RGBA（ts.color；没设是 0）
	Parent           *node
	Children         []*node
}

func num(v reflect.Value, name string) float32 { return float32(v.FieldByName(name).Float()) }
func vec(v reflect.Value, name string) (out [4]float32) {
	f := v.FieldByName(name)
	for i := range out {
		out[i] = float32(f.Index(i).Float())
	}
	return
}

func tree(tt *ui.Tester) *node {
	root := reflect.ValueOf(tt).Elem().FieldByName("rt").Elem().FieldByName("c").FieldByName("root")
	var walk func(reflect.Value, *node) *node
	walk = func(p reflect.Value, parent *node) *node {
		v := p.Elem()
		n := &node{Parent: parent, Text: v.FieldByName("text").String(), Label: v.FieldByName("label").String(),
			Box: ui.Rect{X: num(v, "x"), Y: num(v, "y"), W: num(v, "w"), H: num(v, "h")}, Pad: vec(v, "pad"), Radius: vec(v, "radius")}
		n.Features = v.FieldByName("ts").FieldByName("features").String()
		if col := v.FieldByName("ts").FieldByName("color"); col.IsValid() {
			for i, f := range []string{"R", "G", "B", "A"} {
				n.Ink[i] = uint8(col.FieldByName(f).Uint())
			}
		}
		if k := v.FieldByName("key"); !k.IsNil() && k.Elem().Kind() == reflect.String {
			n.Key = k.Elem().String()
		}
		for ch := v.FieldByName("first"); !ch.IsNil(); ch = ch.Elem().FieldByName("next") {
			n.Children = append(n.Children, walk(ch, n))
		}
		return n
	}
	return walk(root, nil)
}

// find 找 Key 等于 key 的第一个元素。
func (n *node) find(key string) *node {
	if n == nil {
		return nil
	}
	if n.Key == key {
		return n
	}
	for _, ch := range n.Children {
		if got := ch.find(key); got != nil {
			return got
		}
	}
	return nil
}

// path 按 Key 一层层往下找，比如 path("m1", "bubble")。
func (n *node) path(keys ...string) *node {
	cur := n
	for _, k := range keys {
		cur = cur.find(k)
		if cur == nil {
			return nil
		}
	}
	return cur
}

func (n *node) String() string {
	if n == nil {
		return "<nil>"
	}
	return strings.TrimSpace(n.Key + " " + n.Label)
}

// edges 是子块到父块上、右、下、左四边的距离。
func edges(p, c ui.Rect) [4]float32 {
	return [4]float32{c.Y - p.Y, p.X + p.W - c.X - c.W, p.Y + p.H - c.Y - c.H, c.X - p.X}
}

func near(a, b float32) bool { d := a - b; return d <= 0.75 && d >= -0.75 }
