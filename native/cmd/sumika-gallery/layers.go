package main

import (
	"fmt"
	"reflect"

	"github.com/egoist/mygo/ui"
)

// “显示层”：给舞台里每一块有底色或边框的容器描一圈细线，深一层换一种颜色，并写出最深有几层。
//
// mygo 没有公开的元素树接口（只有 F12 检查器，不能按层着色），所以这里和 internal/uitest 一样用反射读
// 固定版本 mygo（go.mod 里是 v0.9.0）的内部字段：从 *ui.Context 走 rt → c → root 拿到已排版的树。
// 升级 mygo 后字段名变了会读不到：读不到时 layerScan 返回错误，画廊只关掉这一条并在舞台角上写原因，不崩。
//
// 数得到的：有底色（background）或四边都有边框（border）的元素，包括 winshell 控件的底、气泡、卡片、审批块。
// 数不到的：用 Draw 回调自己画出来的东西（开关的轨道和圆钮、滑块、分段控件滑动的选中块）不是元素，没有框。

// layerBox 是一块被数到的容器：窗口坐标里的框、圆角、第几层（舞台的直接内容是第 1 层）。
type layerBox struct {
	Rect   ui.Rect
	Radius float32
	Level  int
}

// layerResult 是一次扫描的结果。
type layerResult struct {
	Boxes []layerBox
	Depth int // 最深几层
}

// layerScan 在已排版的树里找 key 为 stageKey 的元素，数它里面的容器。
func layerScan(c *ui.Context, stageKey string) (res layerResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = layerResult{}, fmt.Errorf("读不到 mygo 的元素树（版本变了？）：%v", r)
		}
	}()
	root := reflect.ValueOf(c).Elem().FieldByName("rt").Elem().FieldByName("c").FieldByName("root")
	stage := findKey(root, stageKey)
	if !stage.IsValid() {
		return res, fmt.Errorf("树里没有键为 %q 的舞台", stageKey)
	}
	for ch := stage.Elem().FieldByName("first"); !ch.IsNil(); ch = ch.Elem().FieldByName("next") {
		walkLayers(ch, 0, &res)
	}
	return res, nil
}

func nodeKey(v reflect.Value) string {
	key := v.Elem().FieldByName("key")
	if key.IsNil() {
		return ""
	}
	if k := key.Elem(); k.Kind() == reflect.String {
		return k.String()
	}
	return ""
}

// findKey 深度优先找第一个键等于 want 的节点；没有返回零值。
func findKey(n reflect.Value, want string) reflect.Value {
	if n.IsNil() {
		return reflect.Value{}
	}
	if nodeKey(n) == want {
		return n
	}
	for ch := n.Elem().FieldByName("first"); !ch.IsNil(); ch = ch.Elem().FieldByName("next") {
		if f := findKey(ch, want); f.IsValid() {
			return f
		}
	}
	return reflect.Value{}
}

func f32(v reflect.Value, name string) float32 { return float32(v.FieldByName(name).Float()) }

// 元素标志位里“不可见”“不参与”两位（和 internal/uitest 读的是同一份）。
const (
	flagInvisible = 1 << 18
	flagInert     = 1 << 27
)

func walkLayers(n reflect.Value, parentLevel int, res *layerResult) {
	v := n.Elem()
	level := parentLevel
	flags := v.FieldByName("flags").Uint()
	live := !v.FieldByName("hidden").Bool() && flags&flagInvisible == 0 && flags&flagInert == 0
	rect := ui.Rect{X: f32(v, "x"), Y: f32(v, "y"), W: f32(v, "w"), H: f32(v, "h")}
	if live && rect.W > 0 && rect.H > 0 && (v.FieldByName("kind").Uint() == 0 || v.FieldByName("kind").Uint() == 3) { // 只数盒子和输入框：文字、图片、图标不算容器
		border := v.FieldByName("border")
		bordered := true
		for i := 0; i < 4; i++ {
			if border.Index(i).Float() <= 0 {
				bordered = false
			}
		}
		if v.FieldByName("bg").FieldByName("A").Uint() != 0 || bordered {
			level++
			radius := float32(v.FieldByName("radius").Index(0).Float())
			res.Boxes = append(res.Boxes, layerBox{Rect: rect, Radius: radius, Level: level})
			res.Depth = max(res.Depth, level)
		}
	}
	for ch := v.FieldByName("first"); !ch.IsNil(); ch = ch.Elem().FieldByName("next") {
		walkLayers(ch, level, res)
	}
}
