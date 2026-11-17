// Package tokenspec 读设计令牌（W3C 设计令牌 2025.10 格式 + Resolver），校验、算值、生成 Go 代码。
// 规格见 docs/规格-设计令牌.md 第三节；生成器 internal/design/gen 和 design 包的新鲜度测试都调它。
package tokenspec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 三层的名字，也是 resolver 里 set 的名字。
const (
	LayerPrimitive = "primitive"
	LayerSemantic  = "semantic"
	LayerComponent = "component"
)

var layerRank = map[string]int{LayerPrimitive: 0, LayerSemantic: 1, LayerComponent: 2}

// 认识的 $type。
var knownTypes = map[string]bool{
	"color": true, "dimension": true, "number": true, "fontWeight": true, "duration": true, "cubicBezier": true,
}

// 我们自己的扩展键（$extensions.sumika 下）；其他厂商的扩展照规范忽略。
var knownExt = map[string]bool{
	"axis": true, "go": true, "owner": true, "neutral": true, "alpha": true, "reach": true, "shift": true, "over": true, "concentric": true,
}

var tokenKeys = map[string]bool{"$value": true, "$type": true, "$description": true, "$extensions": true, "$deprecated": true}
var groupKeys = map[string]bool{"$type": true, "$description": true, "$extensions": true, "$deprecated": true}

var nameOK = regexp.MustCompile(`^[a-z0-9_][a-z0-9-]*$`)

// Token 是展开后的一个令牌定义（某个文件里的那一份）。
type Token struct {
	Path    string
	Type    string
	Layer   string
	File    string
	Owner   string
	GoName  string
	Private bool // 组名以 _ 开头：只给计算用，不生成字段
	value   *node
	ext     *node
	def     *node
	home    Input // 这份定义生效的那个模式（核对 $value 用）
}

// Input 是 resolver 的输入：每个 modifier 选哪个 context。
type Input map[string]string

func (in Input) key(mods []*modifier) string {
	parts := make([]string, len(mods))
	for i, m := range mods {
		parts[i] = in[m.name]
	}
	return strings.Join(parts, "/")
}

type source struct {
	file   string
	root   *node
	tokens []*Token
	dirty  bool
}

type modifier struct {
	name     string
	def      string
	ctxNames []string
	contexts map[string][]*source
}

type orderItem struct {
	set string    // 非空：这一步是 set
	mod *modifier // 非空：这一步是 modifier
}

// Spec 是读进来的整套令牌。
type Spec struct {
	dir   string
	sets  map[string][]*source
	mods  []*modifier
	order []orderItem
	files []*source
}

// Load 读 dir 下的 resolver.json 和它引用的全部令牌文件。
func Load(dir string) (*Spec, error) {
	data, err := os.ReadFile(filepath.Join(dir, "resolver.json"))
	if err != nil {
		return nil, err
	}
	root, err := parseJSON(data)
	if err != nil {
		return nil, fmt.Errorf("resolver.json：%w", err)
	}
	if v := root.get("version"); v == nil || v.kind != 's' {
		return nil, fmt.Errorf("resolver.json：缺 version")
	}
	s := &Spec{dir: dir, sets: map[string][]*source{}}
	loaded := map[string]*source{}
	readSources := func(where string, arr *node, layer string, home Input) ([]*source, error) {
		if arr == nil || arr.kind != 'a' {
			return nil, fmt.Errorf("%s：sources 要是数组", where)
		}
		var out []*source
		for _, it := range arr.items {
			ref := it.get("$ref")
			if ref == nil || ref.kind != 's' || len(it.members) != 1 {
				return nil, fmt.Errorf("%s：只支持 {\"$ref\": \"文件\"} 形式的来源", where)
			}
			if strings.HasPrefix(ref.str, "#") || strings.Contains(ref.str, "://") {
				return nil, fmt.Errorf("%s：来源 %q 要是相对路径的令牌文件", where, ref.str)
			}
			if _, dup := loaded[ref.str]; dup {
				return nil, fmt.Errorf("%s：文件 %s 被引用了两次", where, ref.str)
			}
			src, err := s.loadFile(ref.str, layer, home)
			if err != nil {
				return nil, err
			}
			loaded[ref.str] = src
			out = append(out, src)
		}
		return out, nil
	}
	setsNode, modsNode := root.get("sets"), root.get("modifiers")
	order := root.get("resolutionOrder")
	if order == nil || order.kind != 'a' {
		return nil, fmt.Errorf("resolver.json：缺 resolutionOrder")
	}
	// 先读 modifier 的定义（默认输入要先知道，才能给每个文件定“生效的模式”）。
	if modsNode != nil {
		for _, m := range modsNode.members {
			ctx := m.val.get("contexts")
			if ctx == nil || ctx.kind != 'o' || len(ctx.members) < 2 {
				return nil, fmt.Errorf("modifier %s：contexts 至少要有两个", m.key)
			}
			mod := &modifier{name: m.key, contexts: map[string][]*source{}}
			for _, c := range ctx.members {
				mod.ctxNames = append(mod.ctxNames, c.key)
			}
			d := m.val.get("default")
			if d == nil || d.kind != 's' {
				return nil, fmt.Errorf("modifier %s：要写 default", m.key)
			}
			mod.def = d.str
			if ctx.get(mod.def) == nil {
				return nil, fmt.Errorf("modifier %s：default %q 不在 contexts 里", m.key, mod.def)
			}
			s.mods = append(s.mods, mod)
		}
	}
	def := s.DefaultInput()
	for _, it := range order.items {
		ref := it.get("$ref")
		if ref == nil || ref.kind != 's' {
			return nil, fmt.Errorf("resolutionOrder：只支持 {\"$ref\": \"#/sets/…\"} 和 {\"$ref\": \"#/modifiers/…\"}")
		}
		switch {
		case strings.HasPrefix(ref.str, "#/sets/"):
			name := strings.TrimPrefix(ref.str, "#/sets/")
			if _, ok := layerRank[name]; !ok {
				return nil, fmt.Errorf("set %q：名字只能是 primitive、semantic、component", name)
			}
			if _, dup := s.sets[name]; dup {
				return nil, fmt.Errorf("set %q 在 resolutionOrder 里出现两次", name)
			}
			sn := setsNode.get(name)
			if sn == nil {
				return nil, fmt.Errorf("resolutionOrder 引用了不存在的 set %q", name)
			}
			srcs, err := readSources("set "+name, sn.get("sources"), name, def)
			if err != nil {
				return nil, err
			}
			s.sets[name] = srcs
			s.order = append(s.order, orderItem{set: name})
		case strings.HasPrefix(ref.str, "#/modifiers/"):
			name := strings.TrimPrefix(ref.str, "#/modifiers/")
			var mod *modifier
			for _, m := range s.mods {
				if m.name == name {
					mod = m
				}
			}
			if mod == nil {
				return nil, fmt.Errorf("resolutionOrder 引用了不存在的 modifier %q", name)
			}
			ctx := modsNode.get(name).get("contexts")
			for _, c := range ctx.members {
				home := Input{}
				for k, v := range def {
					home[k] = v
				}
				home[name] = c.key
				srcs, err := readSources("modifier "+name+"."+c.key, c.val, LayerPrimitive, home)
				if err != nil {
					return nil, err
				}
				mod.contexts[c.key] = srcs
			}
			s.order = append(s.order, orderItem{mod: mod})
		default:
			return nil, fmt.Errorf("resolutionOrder：看不懂 %q", ref.str)
		}
	}
	for _, need := range []string{LayerPrimitive, LayerSemantic} {
		if _, ok := s.sets[need]; !ok {
			return nil, fmt.Errorf("resolutionOrder 里缺 set %q", need)
		}
	}
	if len(s.order) > 0 && s.order[0].set != LayerPrimitive {
		return nil, fmt.Errorf("resolutionOrder 要从 primitive 开始")
	}
	return s, nil
}

// DefaultInput 是每个 modifier 都取默认值的输入。
func (s *Spec) DefaultInput() Input {
	in := Input{}
	for _, m := range s.mods {
		in[m.name] = m.def
	}
	return in
}

// Inputs 列出全部模式组合，按 modifier、context 的书写顺序。
func (s *Spec) Inputs() []Input {
	out := []Input{{}}
	for _, m := range s.mods {
		var next []Input
		for _, in := range out {
			for _, c := range m.ctxNames {
				n := Input{}
				for k, v := range in {
					n[k] = v
				}
				n[m.name] = c
				next = append(next, n)
			}
		}
		out = next
	}
	return out
}

func (s *Spec) loadFile(rel, layer string, home Input) (*source, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	root, err := parseJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s：%w", rel, err)
	}
	src := &source{file: rel, root: root}
	if err := walk(root, "", "", false, src, layer, home); err != nil {
		return nil, fmt.Errorf("%s：%w", rel, err)
	}
	s.files = append(s.files, src)
	return src, nil
}

func walk(n *node, prefix, typ string, private bool, src *source, layer string, home Input) error {
	if n.kind != 'o' {
		return fmt.Errorf("%s：组要是对象", prefix)
	}
	if t := n.get("$type"); t != nil {
		if t.kind != 's' || !knownTypes[t.str] {
			return fmt.Errorf("%s：不认识的 $type", prefix)
		}
		typ = t.str
	}
	if n.get("$value") != nil {
		for _, m := range n.members {
			if !tokenKeys[m.key] {
				return fmt.Errorf("令牌 %s：不认识的键 %q", prefix, m.key)
			}
		}
		if typ == "" {
			return fmt.Errorf("令牌 %s：没有 $type（自己或上层组要写）", prefix)
		}
		tk := &Token{Path: prefix, Type: typ, Layer: layer, File: src.file, Private: private, value: n.get("$value"), def: n, home: home}
		if ex := n.get("$extensions"); ex != nil {
			if ex.kind != 'o' {
				return fmt.Errorf("令牌 %s：$extensions 要是对象", prefix)
			}
			if su := ex.get("sumika"); su != nil {
				if su.kind != 'o' {
					return fmt.Errorf("令牌 %s：$extensions.sumika 要是对象", prefix)
				}
				for _, m := range su.members {
					if !knownExt[m.key] {
						return fmt.Errorf("令牌 %s：$extensions.sumika 里不认识的键 %q", prefix, m.key)
					}
				}
				tk.ext = su
				if g := su.get("go"); g != nil {
					tk.GoName = g.str
				}
				if o := su.get("owner"); o != nil {
					tk.Owner = o.str
				}
			}
		}
		if layer == LayerComponent && tk.Owner == "" {
			return fmt.Errorf("组件令牌 %s：要写 $extensions.sumika.owner（哪个包的组件）", prefix)
		}
		src.tokens = append(src.tokens, tk)
		return nil
	}
	for _, m := range n.members {
		if strings.HasPrefix(m.key, "$") {
			if !groupKeys[m.key] {
				return fmt.Errorf("组 %s：不支持的键 %q", prefix, m.key)
			}
			continue
		}
		if !nameOK.MatchString(m.key) {
			return fmt.Errorf("组 %s：名字 %q 只能用小写字母、数字、连字符", prefix, m.key)
		}
		p := m.key
		if prefix != "" {
			p = prefix + "." + m.key
		}
		if err := walk(m.val, p, typ, private || strings.HasPrefix(m.key, "_"), src, layer, home); err != nil {
			return err
		}
	}
	return nil
}

// resolve 按 resolutionOrder 叠出 in 这个模式下的令牌表。
func (s *Spec) resolve(in Input) (map[string]*Token, error) {
	out := map[string]*Token{}
	for _, it := range s.order {
		if it.set != "" {
			for _, src := range s.sets[it.set] {
				for _, tk := range src.tokens {
					if prev, ok := out[tk.Path]; ok && prev.Layer != tk.Layer {
						return nil, fmt.Errorf("令牌 %s 在 %s 和 %s 两层都有定义", tk.Path, prev.Layer, tk.Layer)
					}
					out[tk.Path] = tk
				}
			}
			continue
		}
		c, ok := in[it.mod.name]
		if !ok {
			c = it.mod.def
		}
		srcs, ok := it.mod.contexts[c]
		if !ok {
			return nil, fmt.Errorf("modifier %s 没有 context %q", it.mod.name, c)
		}
		for _, src := range srcs {
			for _, tk := range src.tokens {
				prev, ok := out[tk.Path]
				if !ok {
					return nil, fmt.Errorf("%s：modifier 只能覆盖已有的原料令牌，%s 不存在", src.file, tk.Path)
				}
				if prev.Layer != LayerPrimitive {
					return nil, fmt.Errorf("%s：modifier 只能覆盖原料层，%s 在 %s 层", src.file, tk.Path, prev.Layer)
				}
				if prev.Type != tk.Type {
					return nil, fmt.Errorf("%s：覆盖 %s 时类型从 %s 变成了 %s", src.file, tk.Path, prev.Type, tk.Type)
				}
				out[tk.Path] = tk
			}
		}
	}
	return out, nil
}

func sortedPaths(m map[string]*Token) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
