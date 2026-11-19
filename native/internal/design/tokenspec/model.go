package tokenspec

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// DimSpec 是运行时算一个尺寸的公式（规格 3.3）：按 Order 的顺序算，引用都指向前面的项。
type DimSpec struct {
	Path    string
	Base    float64
	Axis    int
	Op      int
	A, B, C int // 引用的下标；C 只给 OpInner 用（radius.inner-min）
}

// Field 是生成出来的一个结构体字段。
type Field struct {
	Path, Name string
	Index      int // 尺寸：在 Dims 公式表里的下标
}

// Model 是生成 Go 代码要的全部数据。
type Model struct {
	Modifiers []string
	Inputs    []Input
	Dims      []DimSpec
	DimFields []Field
	Colors    []Field
	Nums      []Field
	Durs      []Field
	Curves    []Field
	colorTab  [][]ui.Color // [模式][字段]
	numTab    [][]float64
	durVals   []float64
	curveVals [][4]float64
}

// Build 校验整套令牌，算出每个模式的颜色和数，排好尺寸公式。
func (s *Spec) Build() (*Model, error) {
	if err := s.checkValues(false); err != nil {
		return nil, err
	}
	m := &Model{Inputs: s.Inputs()}
	for _, md := range s.mods {
		m.Modifiers = append(m.Modifiers, md.name)
	}
	type evald struct {
		toks map[string]*Token
		vals map[string]*value
	}
	var all []evald
	for _, in := range m.Inputs {
		toks, vals, err := s.evaluate(in)
		if err != nil {
			return nil, fmt.Errorf("模式 %s：%w", in.key(s.mods), err)
		}
		all = append(all, evald{toks, vals})
	}
	base := all[0]
	for _, p := range sortedPaths(base.toks) {
		tk := base.toks[p]
		if tk.Type == "dimension" || tk.Type == "duration" || tk.Type == "cubicBezier" {
			for i, ev := range all[1:] {
				if ev.toks[p] != tk {
					return nil, fmt.Errorf("%s 在模式 %s 下被改了：尺寸、时长、曲线不分模式", p, m.Inputs[i+1].key(s.mods))
				}
			}
		}
	}
	// 尺寸公式：拓扑序。
	index := map[string]int{}
	var visit func(p string) (int, error)
	visit = func(p string) (int, error) {
		if i, ok := index[p]; ok {
			return i, nil
		}
		tk := base.toks[p]
		d := DimSpec{Path: p, A: -1, B: -1, C: -1}
		if rp, ok := refPath(tk.value); ok {
			i, err := visit(rp)
			if err != nil {
				return 0, err
			}
			d.Op, d.A = OpAlias, i
		} else if c := tk.ext.get("concentric"); c != nil {
			mm := c.members[0]
			var idx []int
			for _, it := range mm.val.items {
				rp, _ := refPath(it)
				i, err := visit(rp)
				if err != nil {
					return 0, err
				}
				idx = append(idx, i)
			}
			switch mm.key {
			case "wrap":
				d.Op, d.A, d.B = OpWrap, idx[0], idx[1]
			case "inner":
				lo, err := visit(innerMinPath)
				if err != nil {
					return 0, err
				}
				d.Op, d.A, d.B, d.C = OpInner, idx[0], idx[1], lo
			case "half":
				d.Op, d.A = OpHalf, idx[0]
			}
		} else {
			f, _ := tk.value.get("value").float()
			d.Op, d.Base, d.Axis = OpBase, f, axisNames[tk.ext.get("axis").str]
		}
		index[p] = len(m.Dims)
		m.Dims = append(m.Dims, d)
		return index[p], nil
	}
	// 每种类型一个结构体，名字在结构体内不许撞；颜色都在 color 组下，生成时去掉这一段（Colors.Bg 而不是 Colors.ColorBg）。
	names := map[string]map[string]string{}
	field := func(tk *Token) (Field, error) {
		n := tk.GoName
		if n == "" {
			n = goName(strings.TrimPrefix(tk.Path, "color."))
		}
		kind := tk.Type
		if kind == "fontWeight" {
			kind = "number"
		}
		if names[kind] == nil {
			names[kind] = map[string]string{}
		}
		if prev, dup := names[kind][n]; dup {
			return Field{}, fmt.Errorf("生成的 Go 名字 %s 撞车：%s 和 %s", n, prev, tk.Path)
		}
		names[kind][n] = tk.Path
		return Field{Path: tk.Path, Name: n}, nil
	}
	for _, p := range sortedPaths(base.toks) {
		tk := base.toks[p]
		if tk.Type == "dimension" {
			if _, err := visit(p); err != nil {
				return nil, err
			}
		}
		if tk.Layer == LayerPrimitive || tk.Private {
			continue
		}
		f, err := field(tk)
		if err != nil {
			return nil, err
		}
		v := base.vals[p]
		switch tk.Type {
		case "dimension":
			f.Index = index[p]
			m.DimFields = append(m.DimFields, f)
		case "color":
			m.Colors = append(m.Colors, f)
		case "number", "fontWeight":
			m.Nums = append(m.Nums, f)
		case "duration":
			m.Durs = append(m.Durs, f)
			m.durVals = append(m.durVals, v.ms)
		case "cubicBezier":
			m.Curves = append(m.Curves, f)
			m.curveVals = append(m.curveVals, v.bez)
		}
	}
	for _, ev := range all {
		var cs []ui.Color
		for _, f := range m.Colors {
			cs = append(cs, ev.vals[f.Path].color)
		}
		var ns []float64
		for _, f := range m.Nums {
			ns = append(ns, ev.vals[f.Path].num)
		}
		m.colorTab = append(m.colorTab, cs)
		m.numTab = append(m.numTab, ns)
	}
	return m, nil
}

// checkValues 核对算出来的令牌的 $value；fix 为真时改成算出来的值并写回文件。
func (s *Spec) checkValues(fix bool) error {
	cache := map[string]map[string]*value{}
	var errs []string
	for _, src := range s.files {
		for _, tk := range src.tokens {
			if tk.ext == nil {
				continue
			}
			k := Input(tk.home).key(s.mods)
			vals, ok := cache[k]
			if !ok {
				_, v, err := s.evaluate(tk.home)
				if err != nil {
					return fmt.Errorf("模式 %s：%w", k, err)
				}
				vals, cache[k] = v, v
			}
			want := expected(tk, vals[tk.Path])
			if want == nil || sameValue(tk.value, want) {
				continue
			}
			if fix {
				tk.def.set("$value", want)
				tk.value = want
				src.dirty = true
				continue
			}
			errs = append(errs, fmt.Sprintf("%s：%s 的 $value 和算出来的不一致，应为 %s（跑 go generate 时加 -fix 改写）", src.file, tk.Path, string(bytes.TrimSpace(want.encode()))))
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// Fix 把算出来的令牌的 $value 改成算出来的值，写回改过的文件。
func (s *Spec) Fix() ([]string, error) {
	if err := s.checkValues(true); err != nil {
		return nil, err
	}
	var changed []string
	for _, src := range s.files {
		if !src.dirty {
			continue
		}
		if err := os.WriteFile(filepath.Join(s.dir, filepath.FromSlash(src.file)), src.root.encode(), 0o644); err != nil {
			return nil, err
		}
		changed = append(changed, src.file)
	}
	return changed, nil
}

// goName 把 space.s1 这样的路径换成 SpaceS1。
func goName(p string) string {
	var b strings.Builder
	up := true
	for _, r := range p {
		if r == '.' || r == '-' || r == '_' {
			up = true
			continue
		}
		if up {
			r = unicode.ToUpper(r)
			up = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Go 生成 design 包的 tokens_gen.go。
func (m *Model) Go() ([]byte, error) {
	var b bytes.Buffer
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("// Code generated by internal/design/gen from tokens/*.tokens.json. DO NOT EDIT.\n\n")
	w("package design\n\nimport (\n\t\"time\"\n\n\t\"github.com/egoist/mygo/ui\"\n)\n\n")
	w("// Dims 是语义层和组件层的尺寸（逻辑像素，已按外观设置缩放、取整）。\ntype Dims struct {\n")
	for _, f := range m.DimFields {
		w("\t%s float32 // %s\n", f.Name, f.Path)
	}
	w("}\n\nconst dimCount = %d\n\n", len(m.Dims))
	w("// dimSpecs 是尺寸公式，按计算顺序排；引用都指向前面的项。\nvar dimSpecs = [dimCount]dimSpec{\n")
	for _, d := range m.Dims {
		w("\t{base: %s, axis: %d, op: %d, a: %d, b: %d, c: %d}, // %s\n", fmtNum(d.Base), d.Axis, d.Op, d.A, d.B, d.C, d.Path)
	}
	w("}\n\nfunc (d *Dims) fill(v *[dimCount]float32) {\n")
	for _, f := range m.DimFields {
		w("\td.%s = v[%d]\n", f.Name, f.Index)
	}
	w("}\n\n// byPath 按令牌路径列出全部尺寸（标准答案、对照表用，别的语言按路径对得上）。\nfunc (d *Dims) byPath() map[string]float32 {\n\treturn map[string]float32{\n")
	for _, f := range m.DimFields {
		w("\t\t%q: d.%s,\n", f.Path, f.Name)
	}
	w("\t}\n}\n\n// Colors 是语义层的颜色（随模式换整张表）。\ntype Colors struct {\n")
	for _, f := range m.Colors {
		w("\t%s ui.Color // %s\n", f.Name, f.Path)
	}
	w("}\n\n// Nums 是语义层的数（字重、透明度、角色色配方……，随模式换整张表）。\ntype Nums struct {\n")
	for _, f := range m.Nums {
		w("\t%s float64 // %s\n", f.Name, f.Path) // 不用 float32：配方数（0.9 之类）截成 float32 会让叠色取整差一档
	}
	w("}\n\n// Durations 是动效时长。\ntype Durations struct {\n")
	for _, f := range m.Durs {
		w("\t%s time.Duration // %s\n", f.Name, f.Path)
	}
	w("}\n\nvar durations = Durations{\n")
	for i, f := range m.Durs {
		w("\t%s: %s * time.Millisecond,\n", f.Name, fmtNum(m.durVals[i]))
	}
	w("}\n\n// Curves 是缓动曲线（cubic-bezier 的四个数）。\ntype Curves struct {\n")
	for _, f := range m.Curves {
		w("\t%s [4]float32 // %s\n", f.Name, f.Path)
	}
	w("}\n\nvar curves = Curves{\n")
	for i, f := range m.Curves {
		c := m.curveVals[i]
		w("\t%s: [4]float32{%s, %s, %s, %s},\n", f.Name, fmtNum(c[0]), fmtNum(c[1]), fmtNum(c[2]), fmtNum(c[3]))
	}
	w("}\n\n// modifierNames 是 resolver 里 modifier 的顺序，modeTables 的键按它拼。\nvar modifierNames = [...]string{")
	for i, n := range m.Modifiers {
		if i > 0 {
			w(", ")
		}
		w("%q", n)
	}
	w("}\n\nvar modeTables = map[string]modeTable{\n")
	for i, in := range m.Inputs {
		var parts []string
		for _, n := range m.Modifiers {
			parts = append(parts, in[n])
		}
		w("\t%q: {\n\t\tcolors: Colors{\n", strings.Join(parts, "/"))
		for j, f := range m.Colors {
			c := m.colorTab[i][j]
			w("\t\t\t%s: ui.Color{R: %d, G: %d, B: %d, A: %d},\n", f.Name, c.R, c.G, c.B, c.A)
		}
		w("\t\t},\n\t\tnums: Nums{\n")
		for j, f := range m.Nums {
			w("\t\t\t%s: %s,\n", f.Name, fmtNum(m.numTab[i][j]))
		}
		w("\t\t},\n\t},\n")
	}
	w("}\n\n// colorNames 按令牌路径查颜色（检查器、文档、检查脚本用）。\nfunc (c *Colors) byPath(p string) (ui.Color, bool) {\n\tswitch p {\n")
	for _, f := range m.Colors {
		w("\tcase %q:\n\t\treturn c.%s, true\n", f.Path, f.Name)
	}
	w("\t}\n\treturn ui.Color{}, false\n}\n")
	out, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("生成的 Go 代码格式化失败：%w", err)
	}
	return out, nil
}
