package tokenspec

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/design/colorx"
)

// 尺寸的缩放轴（规格 3.3 第 2 条）。
const (
	AxisFixed = iota
	AxisText
	AxisSpace
	AxisRadius
)

var axisNames = map[string]int{"fixed": AxisFixed, "text": AxisText, "space": AxisSpace, "radius": AxisRadius}

// 尺寸的计算方式（规格 3.3 第 3 条）。
const (
	OpBase  = iota // 基础值 × 轴系数，取整
	OpAlias        // 等于另一个尺寸
	OpWrap         // A + B
	OpInner        // max(min(radius.inner-min, A), A − B)
	OpHalf         // A / 2，取整
)

// innerMinPath 是同心圆角的下限令牌。
const innerMinPath = "radius.inner-min"

// 中性色配方要读的两个原料令牌。
const neutralHuePath, neutralChromaPath = "neutral.hue", "neutral.chroma"

type value struct {
	typ   string
	color ui.Color
	ok    colorx.OKLCH
	hasOK bool
	num   float64
	dim   float64 // 默认设置下的值
	ms    float64
	bez   [4]float64
}

type evaluator struct {
	toks  map[string]*Token
	vals  map[string]*value
	state map[string]int // 1 正在算、2 算完
	stack []string
}

func (s *Spec) evaluate(in Input) (map[string]*Token, map[string]*value, error) {
	toks, err := s.resolve(in)
	if err != nil {
		return nil, nil, err
	}
	e := &evaluator{toks: toks, vals: map[string]*value{}, state: map[string]int{}}
	for _, p := range sortedPaths(toks) {
		if _, err := e.get(p, ""); err != nil {
			return nil, nil, err
		}
	}
	return toks, e.vals, nil
}

func refPath(n *node) (string, bool) {
	if n == nil || n.kind != 's' || !strings.HasPrefix(n.str, "{") || !strings.HasSuffix(n.str, "}") {
		return "", false
	}
	return n.str[1 : len(n.str)-1], true
}

// get 算 path 的值；from 是引用它的令牌（检查分层用）。
func (e *evaluator) get(path, from string) (*value, error) {
	tk, ok := e.toks[path]
	if !ok {
		if from == "" {
			return nil, fmt.Errorf("引用了不存在的令牌 {%s}", path)
		}
		return nil, fmt.Errorf("令牌 %s 引用了不存在的令牌 {%s}", from, path)
	}
	if from != "" {
		if err := e.checkRef(e.toks[from], tk); err != nil {
			return nil, err
		}
	}
	switch e.state[path] {
	case 2:
		return e.vals[path], nil
	case 1:
		return nil, fmt.Errorf("循环引用：%s → %s", strings.Join(e.stack, " → "), path)
	}
	e.state[path] = 1
	e.stack = append(e.stack, path)
	v, err := e.compute(tk)
	e.stack = e.stack[:len(e.stack)-1]
	if err != nil {
		return nil, err
	}
	e.state[path] = 2
	e.vals[path] = v
	return v, nil
}

func (e *evaluator) checkRef(from, to *Token) error {
	switch from.Layer {
	case LayerPrimitive:
		if to.Layer != LayerPrimitive {
			return fmt.Errorf("原料令牌 %s 不能引用 %s 层的 %s", from.Path, to.Layer, to.Path)
		}
	case LayerSemantic:
		if to.Layer == LayerComponent {
			return fmt.Errorf("语义令牌 %s 不能引用组件令牌 %s", from.Path, to.Path)
		}
	case LayerComponent:
		if to.Layer == LayerPrimitive {
			return fmt.Errorf("组件令牌 %s 只能引用语义层，不能直接用原料 %s", from.Path, to.Path)
		}
		if to.Layer == LayerComponent && to.Owner != from.Owner {
			return fmt.Errorf("组件令牌 %s（%s）不能引用别的组件 %s（%s）的令牌", from.Path, from.Owner, to.Path, to.Owner)
		}
	}
	return nil
}

func (e *evaluator) ref(n *node, from *Token, want string) (*value, error) {
	p, ok := refPath(n)
	if !ok {
		return nil, fmt.Errorf("令牌 %s：这里要写 {引用}", from.Path)
	}
	v, err := e.get(p, from.Path)
	if err != nil {
		return nil, err
	}
	if want != "" && v.typ != want && !(want == "number" && v.typ == "fontWeight") {
		return nil, fmt.Errorf("令牌 %s：引用的 {%s} 是 %s，这里要 %s", from.Path, p, v.typ, want)
	}
	return v, nil
}

// numOrRef 读一个数，或者一个指向数的引用。
func (e *evaluator) numOrRef(n *node, from *Token) (float64, error) {
	if f, ok := n.float(); ok {
		return f, nil
	}
	v, err := e.ref(n, from, "number")
	if err != nil {
		return 0, err
	}
	return v.num, nil
}

func (e *evaluator) compute(tk *Token) (*value, error) {
	if p, ok := refPath(tk.value); ok {
		if tk.ext != nil {
			for _, k := range []string{"neutral", "alpha", "reach", "shift", "over", "concentric"} {
				if tk.ext.get(k) != nil {
					return nil, fmt.Errorf("令牌 %s：别名不能再带计算 %q", tk.Path, k)
				}
			}
		}
		v, err := e.get(p, tk.Path)
		if err != nil {
			return nil, err
		}
		if v.typ != tk.Type {
			return nil, fmt.Errorf("令牌 %s 是 %s，别名指向的 {%s} 是 %s", tk.Path, tk.Type, p, v.typ)
		}
		return v, nil
	}
	if tk.Layer == LayerComponent && !e.isCalc(tk) {
		return nil, fmt.Errorf("组件令牌 %s 不能写字面量，要引用语义层或写计算", tk.Path)
	}
	switch tk.Type {
	case "color":
		return e.color(tk)
	case "dimension":
		return e.dimension(tk)
	case "number", "fontWeight":
		f, ok := tk.value.float()
		if !ok {
			return nil, fmt.Errorf("令牌 %s：$value 要是数", tk.Path)
		}
		return &value{typ: tk.Type, num: f}, nil
	case "duration":
		u, num := tk.value.get("unit"), tk.value.get("value")
		f, ok := num.float()
		if !ok || u == nil || u.str != "ms" {
			return nil, fmt.Errorf("令牌 %s：duration 要写 {\"value\": 数, \"unit\": \"ms\"}", tk.Path)
		}
		return &value{typ: "duration", ms: f}, nil
	case "cubicBezier":
		if tk.value.kind != 'a' || len(tk.value.items) != 4 {
			return nil, fmt.Errorf("令牌 %s：cubicBezier 要是四个数", tk.Path)
		}
		v := &value{typ: "cubicBezier"}
		for i, it := range tk.value.items {
			f, ok := it.float()
			if !ok {
				return nil, fmt.Errorf("令牌 %s：cubicBezier 要是四个数", tk.Path)
			}
			v.bez[i] = f
		}
		return v, nil
	}
	return nil, fmt.Errorf("令牌 %s：不支持的类型 %s", tk.Path, tk.Type)
}

func (e *evaluator) isCalc(tk *Token) bool {
	if tk.ext == nil {
		return false
	}
	for _, k := range []string{"neutral", "alpha", "reach", "shift", "over", "concentric"} {
		if tk.ext.get(k) != nil {
			return true
		}
	}
	return false
}

// 颜色的 $value：{"colorSpace": "srgb" | "oklch", "components": […], "alpha": a, "hex": "#…"}。
func parseColor(n *node) (ui.Color, colorx.OKLCH, bool, error) {
	cs, comps := n.get("colorSpace"), n.get("components")
	if cs == nil || comps == nil || comps.kind != 'a' || len(comps.items) != 3 {
		return ui.Color{}, colorx.OKLCH{}, false, fmt.Errorf("颜色要写 colorSpace 和三个 components")
	}
	var c [3]float64
	for i, it := range comps.items {
		f, ok := it.float()
		if !ok {
			return ui.Color{}, colorx.OKLCH{}, false, fmt.Errorf("components 要是数")
		}
		c[i] = f
	}
	a := 1.0
	if al := n.get("alpha"); al != nil {
		f, ok := al.float()
		if !ok {
			return ui.Color{}, colorx.OKLCH{}, false, fmt.Errorf("alpha 要是数")
		}
		a = f
	}
	switch cs.str {
	case "srgb":
		to := func(x float64) uint8 { return uint8(math.Round(math.Max(0, math.Min(1, x)) * 255)) }
		return ui.RGBA(to(c[0]), to(c[1]), to(c[2]), float32(a)), colorx.OKLCH{}, false, nil
	case "oklch":
		o := colorx.OKLCH{L: c[0], C: c[1], H: c[2], A: a}
		return o.Color(), o, true, nil
	}
	return ui.Color{}, colorx.OKLCH{}, false, fmt.Errorf("colorSpace 只支持 srgb、oklch，写的是 %q", cs.str)
}

// calcKeys 是每种计算认识的参数键；多写的键报错，免得拼错了悄悄不生效。
var calcKeys = map[string]map[string]bool{
	"neutral": {"l": true, "k": true},
	"reach":   {"base": true, "min": true, "away": true, "against": true},
	"over":    {"fg": true, "bg": true, "k": true},
}

func (e *evaluator) color(tk *Token) (*value, error) {
	ex := tk.ext
	for calc, keys := range calcKeys {
		if n := ex.get(calc); n != nil {
			if n.kind != 'o' {
				return nil, fmt.Errorf("令牌 %s：%s 要是对象", tk.Path, calc)
			}
			for _, m := range n.members {
				if !keys[m.key] {
					return nil, fmt.Errorf("令牌 %s：%s 里不认识的键 %q", tk.Path, calc, m.key)
				}
			}
		}
	}
	v := &value{typ: "color"}
	switch {
	case ex != nil && ex.get("neutral") != nil:
		r := ex.get("neutral")
		l, err1 := e.numOrRef(r.get("l"), tk)
		k, err2 := e.numOrRef(r.get("k"), tk)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("令牌 %s：neutral 要写 l 和 k", tk.Path)
		}
		h, err := e.ref(strNode("{"+neutralHuePath+"}"), tk, "number")
		if err != nil {
			return nil, err
		}
		c, err := e.ref(strNode("{"+neutralChromaPath+"}"), tk, "number")
		if err != nil {
			return nil, err
		}
		v.ok, v.hasOK = colorx.New(l, c.num*k, h.num), true
		v.color = v.ok.Color()
	case ex != nil && ex.get("alpha") != nil:
		r := ex.get("alpha")
		if r.kind != 'a' || len(r.items) != 2 {
			return nil, fmt.Errorf("令牌 %s：alpha 要写 [{颜色}, 不透明度]", tk.Path)
		}
		base, err := e.ref(r.items[0], tk, "color")
		if err != nil {
			return nil, err
		}
		a, err := e.numOrRef(r.items[1], tk)
		if err != nil {
			return nil, err
		}
		if base.hasOK {
			v.ok, v.hasOK = base.ok.WithAlpha(a), true
			v.color = v.ok.Color()
		} else {
			v.color = base.color
			v.color.A = uint8(math.Round(a * 255))
		}
	case ex != nil && ex.get("shift") != nil:
		r := ex.get("shift")
		if r.kind != 'a' || len(r.items) != 2 {
			return nil, fmt.Errorf("令牌 %s：shift 要写 [{颜色}, 明度差]", tk.Path)
		}
		base, err := e.ref(r.items[0], tk, "color")
		if err != nil {
			return nil, err
		}
		if !base.hasOK {
			return nil, fmt.Errorf("令牌 %s：shift 的底色要是 oklch 算出来的", tk.Path)
		}
		dl, err := e.numOrRef(r.items[1], tk)
		if err != nil {
			return nil, err
		}
		o := base.ok
		o.L += dl
		v.ok, v.hasOK = o, true
		v.color = o.Color()
	case ex != nil && ex.get("reach") != nil:
		r := ex.get("reach")
		base, err := e.ref(r.get("base"), tk, "color")
		if err != nil {
			return nil, err
		}
		if !base.hasOK {
			return nil, fmt.Errorf("令牌 %s：reach 的底色要是 oklch 算出来的", tk.Path)
		}
		min, err := e.numOrRef(r.get("min"), tk)
		if err != nil {
			return nil, err
		}
		away, err := e.numOrRef(r.get("away"), tk)
		if err != nil {
			return nil, err
		}
		ag := r.get("against")
		if ag == nil || ag.kind != 'a' || len(ag.items) == 0 {
			return nil, fmt.Errorf("令牌 %s：reach 要写 against（对哪些底色）", tk.Path)
		}
		var bgs []ui.Color
		for _, it := range ag.items {
			b, err := e.ref(it, tk, "color")
			if err != nil {
				return nil, err
			}
			bgs = append(bgs, b.color)
		}
		o, ok := colorx.Reach(base.ok, away, min, bgs...)
		if !ok {
			return nil, fmt.Errorf("令牌 %s：对比度 %.1f 解不出来（沿明度推到头也不够）", tk.Path, min)
		}
		v.ok, v.hasOK = o, true
		v.color = o.Color()
	case ex != nil && ex.get("over") != nil:
		r := ex.get("over")
		fg, err := e.ref(r.get("fg"), tk, "color")
		if err != nil {
			return nil, err
		}
		bg, err := e.ref(r.get("bg"), tk, "color")
		if err != nil {
			return nil, err
		}
		k := float64(fg.color.A) / 255
		if kn := r.get("k"); kn != nil {
			if k, err = e.numOrRef(kn, tk); err != nil {
				return nil, err
			}
		}
		v.color = colorx.Over(fg.color, k, bg.color)
	default:
		c, o, has, err := parseColor(tk.value)
		if err != nil {
			return nil, fmt.Errorf("令牌 %s：%w", tk.Path, err)
		}
		v.color, v.ok, v.hasOK = c, o, has
		return v, nil
	}
	// 算出来的颜色：$value 要和结果一致（只在这份定义生效的模式下核对，见 checkValues）。
	return v, nil
}

func (e *evaluator) dimension(tk *Token) (*value, error) {
	if c := tk.ext.get("concentric"); c != nil {
		if c.kind != 'o' || len(c.members) != 1 {
			return nil, fmt.Errorf("令牌 %s：concentric 只写 wrap、inner、half 其中一个", tk.Path)
		}
		m := c.members[0]
		args := m.val
		need := map[string]int{"wrap": 2, "inner": 2, "half": 1}[m.key]
		if need == 0 || args.kind != 'a' || len(args.items) != need {
			return nil, fmt.Errorf("令牌 %s：concentric.%s 的参数不对", tk.Path, m.key)
		}
		var xs []float64
		for _, it := range args.items {
			d, err := e.ref(it, tk, "dimension")
			if err != nil {
				return nil, err
			}
			xs = append(xs, d.dim)
		}
		var out float64
		switch m.key {
		case "wrap":
			out = xs[0] + xs[1]
		case "inner":
			lo, err := e.ref(strNode("{"+innerMinPath+"}"), tk, "dimension")
			if err != nil {
				return nil, err
			}
			out = math.Max(math.Min(lo.dim, xs[0]), xs[0]-xs[1]) // 下限不超过外层：外层本身比下限还小时，里层不能比外层圆
		case "half":
			out = math.Round(xs[0] / 2)
		}
		return &value{typ: "dimension", dim: out}, nil
	}
	f, ok := tk.value.get("value").float()
	if u := tk.value.get("unit"); !ok || u == nil || u.str != "px" {
		return nil, fmt.Errorf("令牌 %s：dimension 要写 {\"value\": 数, \"unit\": \"px\"}", tk.Path)
	}
	ax := tk.ext.get("axis")
	if ax == nil {
		return nil, fmt.Errorf("令牌 %s：尺寸要写 $extensions.sumika.axis（text、space、radius、fixed）", tk.Path)
	}
	if _, ok := axisNames[ax.str]; !ok {
		return nil, fmt.Errorf("令牌 %s：axis 只能是 text、space、radius、fixed", tk.Path)
	}
	return &value{typ: "dimension", dim: f}, nil
}

// expected 是一个令牌在自己生效的模式下应该写在 $value 里的值；不是计算出来的返回 nil。
func expected(tk *Token, v *value) *node {
	if tk.ext == nil {
		return nil
	}
	switch tk.Type {
	case "color":
		for _, k := range []string{"neutral", "alpha", "reach", "shift", "over"} {
			if tk.ext.get(k) != nil {
				return colorNode(v.color)
			}
		}
	case "dimension":
		if tk.ext.get("concentric") != nil {
			return &node{kind: 'o', members: []member{{"value", numNode(v.dim)}, {"unit", strNode("px")}}}
		}
	}
	return nil
}

func hex(c ui.Color) string {
	s := fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	if c.A != 255 {
		s += fmt.Sprintf("%02x", c.A)
	}
	return s
}

func colorNode(c ui.Color) *node {
	r := func(x uint8) *node { return numNode(math.Round(float64(x)/255*10000) / 10000) }
	n := &node{kind: 'o', members: []member{
		{"colorSpace", strNode("srgb")},
		{"components", &node{kind: 'a', items: []*node{r(c.R), r(c.G), r(c.B)}}},
	}}
	if c.A != 255 {
		n.members = append(n.members, member{"alpha", numNode(math.Round(float64(c.A)/255*1000) / 1000)})
	}
	n.members = append(n.members, member{"hex", strNode(hex(c))})
	return n
}

// sameValue 比较 $value 和应有的值：颜色比 hex，尺寸比数。
func sameValue(got, want *node) bool {
	if got == nil {
		return false
	}
	if h := want.get("hex"); h != nil {
		g := got.get("hex")
		return g != nil && strings.EqualFold(g.str, h.str)
	}
	a, ok1 := got.get("value").float()
	b, _ := want.get("value").float()
	return ok1 && a == b
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
