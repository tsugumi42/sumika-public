// Package linktemplate 是链接解析结果的“通配符模板”：解析 $占位符、[可选段] 和转义，按字段值渲染成一串片段。
// 它不认识任何站点，也不取数、不发送；站点有哪些字段由调用方的 Set 给出（规格-链接解析模板第一、三节）。
package linktemplate

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 模板和结构的上限：防止设置里写进一大段文字或深到离谱的嵌套。
const (
	MaxRunes = 2000
	maxDepth = 8
	// MissingText 是裸占位符没取到时的输出，和发送端原有的“未返回”一致。
	MissingText = "未返回"
)

// Kind 是占位符的种类。
type Kind int

const (
	// KindText 换成一段文字。
	KindText Kind = iota
	// KindSlot 在这个位置放图片、卡片、视频或正文块；渲染时只产生一个槽位片段，内容由调用方展开。
	KindSlot
	// KindBreak 另起一条消息；KindForward 之后进合并转发；KindNode 合并转发里换下一个节点。
	KindBreak
	KindForward
	KindNode
)

// Field 是一个占位符。Names 是可以写的名字（中文在前、英文在后），第一个是设置页按钮上显示的名字。
type Field struct {
	ID     string
	Names  []string
	Kind   Kind
	Doc    string
	Sample string
}

// Set 是某个站点能用的占位符。通用的排版占位符（分段、合并转发、节点）由 NewSet 自动加入。
type Set struct {
	fields []Field
	byName map[string]*Field
	maxLen int
}

// NewSet 建一个占位符集合。名字重复会 panic：这是站点表的编写错误，不是用户输入。
func NewSet(fields ...Field) *Set {
	all := slices.Clone(fields)
	all = append(all,
		Field{ID: "break", Names: []string{"分段", "break"}, Kind: KindBreak, Doc: "另起一条消息"},
		Field{ID: "forward", Names: []string{"合并转发", "forward"}, Kind: KindForward, Doc: "这之后的内容进合并转发"},
		Field{ID: "node", Names: []string{"节点", "node"}, Kind: KindNode, Doc: "合并转发里换下一个节点"},
	)
	return newSet(all)
}

// NewPlainSet 建一个只有文字占位符的集合，不带分段、合并转发、节点（合并转发卡片的文字用）。
func NewPlainSet(fields ...Field) *Set { return newSet(slices.Clone(fields)) }

func newSet(all []Field) *Set {
	s := &Set{byName: map[string]*Field{}}
	s.fields = all
	for i := range s.fields {
		f := &s.fields[i]
		for _, n := range f.Names {
			key := strings.ToLower(n)
			if _, dup := s.byName[key]; dup {
				panic("linktemplate: 占位符名字重复：" + n)
			}
			s.byName[key] = f
			s.maxLen = max(s.maxLen, utf8.RuneCountInString(n))
		}
	}
	return s
}

// has 说这个集合里有没有这个 ID 的占位符。
func (s *Set) has(id string) bool {
	for _, f := range s.fields {
		if f.ID == id {
			return true
		}
	}
	return false
}

// Fields 按定义顺序返回全部占位符（设置页的按钮用）。
func (s *Set) Fields() []Field { return slices.Clone(s.fields) }

// Lookup 按名字（中文或英文，英文不分大小写）找占位符。
func (s *Set) Lookup(name string) (Field, bool) {
	f, ok := s.byName[strings.ToLower(name)]
	if !ok {
		return Field{}, false
	}
	return *f, true
}

// ParseError 带位置和建议，错误信息直接给用户看。
type ParseError struct {
	Pos  int // 第几个字（从 1 起，按字符数）
	Msg  string
	Hint string
}

func (e *ParseError) Error() string {
	s := fmt.Sprintf("第 %d 个字：%s", e.Pos, e.Msg)
	if e.Hint != "" {
		s += "（" + e.Hint + "）"
	}
	return s
}

type nodeKind int

const (
	nLit nodeKind = iota
	nField
	nOptional
)

type node struct {
	kind     nodeKind
	text     string
	field    Field
	children []node
}

// Template 是解析后的模板，可以并发渲染。
type Template struct {
	src   string
	nodes []node
}

// Source 返回原文。
func (t *Template) Source() string { return t.src }

// Uses 说模板里（含可选段）有没有写这个占位符，按内部名比，不管写的是中文名还是英文名。
func (t *Template) Uses(id string) bool { return usesField(t.nodes, id) }

func usesField(nodes []node, id string) bool {
	for _, n := range nodes {
		if n.kind == nField && n.field.ID == id || n.kind == nOptional && usesField(n.children, id) {
			return true
		}
	}
	return false
}

// Parse 解析模板。出错时返回 *ParseError，不 panic。
func Parse(src string, set *Set) (*Template, error) {
	runes := []rune(src)
	if len(runes) > MaxRunes {
		return nil, &ParseError{Pos: MaxRunes + 1, Msg: fmt.Sprintf("模板太长了，最多 %d 个字", MaxRunes)}
	}
	p := &parser{r: runes, set: set}
	nodes, err := p.parse(0, 0)
	if err != nil {
		return nil, err
	}
	return &Template{src: src, nodes: nodes}, nil
}

type parser struct {
	r   []rune
	i   int
	set *Set
}

func isNameRune(c rune) bool { return c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c) }

// parse 读到文末或遇到本层的 ]。depth 为 0 表示最外层，遇到 ] 是多余的。
func (p *parser) parse(depth, open int) ([]node, error) {
	var out []node
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, node{kind: nLit, text: lit.String()})
			lit.Reset()
		}
	}
	for p.i < len(p.r) {
		c := p.r[p.i]
		switch c {
		case '\\':
			if p.i+1 >= len(p.r) {
				return nil, &ParseError{Pos: p.i + 1, Msg: "结尾的 \\ 后面没有字", Hint: "要输出反斜杠本身请写 \\\\"}
			}
			n := p.r[p.i+1]
			switch n {
			case 'n':
				lit.WriteByte('\n')
			case '$', '[', ']', '\\':
				lit.WriteRune(n)
			default:
				return nil, &ParseError{Pos: p.i + 1, Msg: "不认识的转义 \\" + string(n), Hint: "可以写 \\n \\$ \\[ \\] \\\\"}
			}
			p.i += 2
		case '$':
			f, err := p.field()
			if err != nil {
				return nil, err
			}
			flush()
			out = append(out, node{kind: nField, field: f})
		case '[':
			if depth+1 > maxDepth {
				return nil, &ParseError{Pos: p.i + 1, Msg: fmt.Sprintf("[ ] 嵌套太深了，最多 %d 层", maxDepth)}
			}
			start := p.i
			p.i++
			children, err := p.parse(depth+1, start+1)
			if err != nil {
				return nil, err
			}
			flush()
			out = append(out, node{kind: nOptional, children: children})
		case ']':
			if depth == 0 {
				return nil, &ParseError{Pos: p.i + 1, Msg: "多出一个 ]，前面没有配对的 [", Hint: "要输出 ] 本身请写 \\]"}
			}
			p.i++
			flush()
			return out, nil
		default:
			lit.WriteRune(c)
			p.i++
		}
	}
	if depth > 0 {
		return nil, &ParseError{Pos: open, Msg: "这个 [ 没有配对的 ]", Hint: "补上 ]，或者写 \\[ 输出方括号本身"}
	}
	flush()
	return out, nil
}

// field 从 $ 开始读一个占位符：${名字} 或 $名字（取最长的已知名字，所以 $标题是… 不会吞掉后面的字）。
func (p *parser) field() (Field, error) {
	pos := p.i + 1
	p.i++ // $
	if p.i < len(p.r) && p.r[p.i] == '{' {
		end := slices.Index(p.r[p.i:], '}')
		if end < 0 {
			return Field{}, &ParseError{Pos: pos, Msg: "${ 没有配对的 }"}
		}
		name := string(p.r[p.i+1 : p.i+end])
		f, ok := p.set.Lookup(name)
		if !ok {
			return Field{}, p.unknown(pos, name)
		}
		p.i += end + 1
		return f, nil
	}
	rest := p.r[p.i:]
	for n := min(len(rest), p.set.maxLen); n >= 1; n-- {
		if f, ok := p.set.Lookup(string(rest[:n])); ok {
			p.i += n
			return f, nil
		}
	}
	var name []rune
	for _, c := range rest {
		if !isNameRune(c) || len(name) >= 12 {
			break
		}
		name = append(name, c)
	}
	if len(name) == 0 {
		return Field{}, &ParseError{Pos: pos, Msg: "$ 后面要接字段名", Hint: "要输出 $ 本身请写 \\$"}
	}
	return Field{}, p.unknown(pos, string(name))
}

// unknown 给出最像的几个名字，写错一两个字时用户能直接看到该写什么。
func (p *parser) unknown(pos int, name string) error {
	var near []string
	lower := strings.ToLower(name)
	for _, f := range p.set.fields {
		for _, n := range f.Names {
			ln := strings.ToLower(n)
			if strings.HasPrefix(lower, ln) || strings.HasPrefix(ln, lower) || strings.Contains(ln, lower) || strings.Contains(lower, ln) {
				near = append(near, "$"+n)
				break
			}
		}
	}
	hint := "这个站点没有这个字段，设置页的按钮里有可用的"
	if len(near) > 0 {
		hint = "你是不是想写 " + strings.Join(near[:min(len(near), 3)], " 或 ")
	}
	return &ParseError{Pos: pos, Msg: "不认识 $" + name, Hint: hint}
}

// Env 给出字段的值。ok 为假表示没取到；ok 为真而值为空，表示取到了一个空值。
// 槽位和排版占位符不调用 Env（槽位在可选段里用 Slot 判断有没有）。
type Env interface {
	Text(id string) (value string, ok bool)
	// Slot 说这个槽位（封面、图片、正文……）现在有没有内容，只用于可选段里的判断。
	Slot(id string) bool
}

// PieceKind 是渲染结果里一个片段的种类。
type PieceKind int

const (
	PieceText PieceKind = iota
	PieceSlot
	PieceBreak
	PieceForward
	PieceNode
)

// Piece 是渲染结果：文字，或一个槽位（ID 是占位符的内部名），或排版控制。
type Piece struct {
	Kind PieceKind
	Text string
	ID   string
}

// Render 按字段值渲染。可选段里有字段没取到或为空，整段省略；裸占位符没取到输出“未返回”。
// 文字片段两侧挨着槽位或排版控制时，去掉紧贴的换行；开头结尾的换行也去掉；空文字片段丢弃。
func (t *Template) Render(env Env) []Piece {
	var pieces []Piece
	renderNodes(t.nodes, env, &pieces, &lineState{start: true})
	return tidy(pieces)
}

// leadSeps 是行首会被去掉的分隔符：可选段把分隔符写在自己里面（比如 [｜收藏 $收藏]），
// 前面的段整段省略后它就落在行首，孤零零的不好看，所以只去掉模板里写的字面分隔符，字段的值不动。
const leadSeps = "｜|·•、，,/／"

// lineState 记着“现在是不是在一行的开头”。
type lineState struct{ start bool }

func renderNodes(nodes []node, env Env, out *[]Piece, st *lineState) {
	for _, n := range nodes {
		switch n.kind {
		case nLit:
			addLiteral(out, n.text, st)
		case nField:
			switch n.field.Kind {
			case KindText:
				v, ok := env.Text(n.field.ID)
				if !ok {
					v = MissingText
				}
				addText(out, v)
				if v != "" {
					st.start = strings.HasSuffix(v, "\n")
				}
			case KindSlot:
				*out = append(*out, Piece{Kind: PieceSlot, ID: n.field.ID})
				st.start = true
			case KindBreak:
				*out = append(*out, Piece{Kind: PieceBreak})
				st.start = true
			case KindForward:
				*out = append(*out, Piece{Kind: PieceForward})
				st.start = true
			case KindNode:
				*out = append(*out, Piece{Kind: PieceNode})
				st.start = true
			}
		case nOptional:
			if !optionalShown(n.children, env) {
				continue
			}
			renderNodes(n.children, env, out, st)
		}
	}
}

// addLiteral 输出模板里的字面文字；落在行首的分隔符（连同后面的空格）去掉。
func addLiteral(out *[]Piece, s string, st *lineState) {
	var b strings.Builder
	for i, seg := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
			st.start = true
		}
		if st.start {
			if t := strings.TrimLeft(seg, " \t"); t != "" {
				if r, _ := utf8.DecodeRuneInString(t); strings.ContainsRune(leadSeps, r) {
					seg = strings.TrimLeft(t, leadSeps+" \t")
				}
			}
		}
		if seg != "" {
			b.WriteString(seg)
			st.start = false
		}
	}
	addText(out, b.String())
}

// optionalShown 决定一个可选段输出不输出：自己直接写的字段都要取到；
// 如果段里没有直接写的字段、只有嵌套的可选段（比如一整行统计 [\n[播放 $播放][｜收藏 $收藏]]），
// 就要求里面至少有一个子段能输出，不然整行连同开头的换行一起省略。
func optionalShown(nodes []node, env Env) bool {
	if !optionalFilled(nodes, env) {
		return false
	}
	nested, direct := false, false
	for _, n := range nodes {
		switch n.kind {
		case nOptional:
			nested = true
		case nField:
			if n.field.Kind == KindText || n.field.Kind == KindSlot {
				direct = true
			}
		}
	}
	if !nested || direct {
		return true
	}
	for _, n := range nodes {
		if n.kind == nOptional && optionalShown(n.children, env) {
			return true
		}
	}
	return false
}

// optionalFilled：段里（不含更深的可选段）每个文字字段都取到且不为空，每个槽位都有内容。
func optionalFilled(nodes []node, env Env) bool {
	for _, n := range nodes {
		if n.kind != nField {
			continue
		}
		switch n.field.Kind {
		case KindText:
			if v, ok := env.Text(n.field.ID); !ok || v == "" {
				return false
			}
		case KindSlot:
			if !env.Slot(n.field.ID) {
				return false
			}
		}
	}
	return true
}

func addText(out *[]Piece, s string) {
	if s == "" {
		return
	}
	if n := len(*out); n > 0 && (*out)[n-1].Kind == PieceText {
		(*out)[n-1].Text += s
		return
	}
	*out = append(*out, Piece{Kind: PieceText, Text: s})
}

func tidy(in []Piece) []Piece {
	var out []Piece
	for i, p := range in {
		if p.Kind != PieceText {
			out = append(out, p)
			continue
		}
		s := p.Text
		if i == 0 || in[i-1].Kind != PieceText {
			s = strings.TrimLeft(s, "\r\n")
		}
		if i == len(in)-1 || in[i+1].Kind != PieceText {
			s = strings.TrimRight(s, "\r\n")
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, Piece{Kind: PieceText, Text: s})
	}
	return out
}

// Describe 把渲染结果写成一行行文字，设置页预览和 sumika-ctl 用：图片类写成 [槽位名]，排版控制写成分隔线。
func Describe(pieces []Piece, label func(id string) string) string {
	var b strings.Builder
	for _, p := range pieces {
		switch p.Kind {
		case PieceText:
			b.WriteString(p.Text)
		case PieceSlot:
			name := p.ID
			if label != nil {
				name = label(p.ID)
			}
			b.WriteString("\n[" + name + "]\n")
		case PieceBreak:
			b.WriteString("\n──── 另起一条消息 ────\n")
		case PieceForward:
			b.WriteString("\n──── 以下进合并转发 ────\n")
		case PieceNode:
			b.WriteString("\n──── 下一个节点 ────\n")
		}
	}
	return strings.Trim(b.String(), "\n")
}
