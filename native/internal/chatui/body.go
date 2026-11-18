package chatui

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 她的回复正文喂给 mygo markdown 之前的整理（docs/规格-过程组件.md 3.7）。两步都只依赖完整文本，每帧重算；
// 对"后面追加文字"是前缀稳定的：显示出来的文字只会往后长，不会改前面已经出过的字
// （否则 mygo 的 reveal 会当成改写，整段直接出完，reveal.go:82）。

const thinkOpen, thinkClose = "<think>", "</think>"

// stripThink 去掉模型输出里的思考段：完整的 <think>…</think> 整段去掉；只有开标签、还没闭合的，从开标签起全部藏起来；
// 流式时末尾是开标签写到一半（"<"、"<thi"）的也先藏，等它写完再看是不是思考段。完成以后末尾单独的 "<" 照常显示。
func stripThink(s string, streaming bool) string {
	var b strings.Builder
	for {
		i := strings.Index(s, thinkOpen)
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i+len(thinkOpen):], thinkClose)
		if j < 0 {
			return strings.TrimLeft(b.String(), "\n")
		}
		s = strings.TrimLeft(s[i+len(thinkOpen)+j+len(thinkClose):], "\n")
	}
	if streaming {
		for k := min(len(thinkOpen)-1, len(s)); k > 0; k-- {
			if strings.HasSuffix(s, thinkOpen[:k]) {
				s = s[:len(s)-k]
				break
			}
		}
	}
	b.WriteString(s)
	return strings.TrimLeft(b.String(), "\n")
}

// hardBreaks 把段落里的单个换行变成硬换行（行末补两个空格）：mygo 的 markdown 把段内换行当空格（spans.go:264），
// 而聊天里她换一行就是换一行（和纯文字气泡一样，审查 C1）。代码围栏里面、空行、缩进 4 格的行不动；
// 只改已经收到换行的完整行，最后一行（可能还在写）不动，所以对追加是前缀稳定的。
func hardBreaks(s string) string {
	var b strings.Builder
	fence := ""
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		line := s[:i]
		s = s[i+1:]
		trim := strings.TrimLeft(line, " ")
		indent := len(line) - len(trim)
		switch {
		case fence != "":
			// 结束行：同一种符号、不少于开头那么多，后面只能有空格（CommonMark 4.5；四个反引号里的三个反引号不算结束）
			if indent < 4 && strings.HasPrefix(trim, fence) && strings.TrimRight(trim, fence[:1]+" ") == "" {
				fence = ""
			}
		case indent < 4 && (strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~")):
			fence = trim[:len(trim)-len(strings.TrimLeft(trim, trim[:1]))] // 开头那一整串 ` 或 ~
		case strings.TrimSpace(line) != "" && indent < 4 && !strings.HasSuffix(line, "  "):
			line += "  "
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

// MD 是宿主持有的、每条消息一份的 markdown 状态（规格-过程组件 3.7）：markdown.Options 里有 sync.Map，
// 不能按值拷、不能几条消息共用（共用时一条的逐字出现会带着别的消息一起动）。Host 按值传，所以放在指针里。
// Host.MD 为空时她的正文照旧按纯文字画（chatui 自己的测试、还没接 markdown 的宿主）。
type MD struct{ m map[string]*mdEntry }

type mdEntry struct {
	opts   markdown.Options
	reveal markdown.Reveal
	theme  markdown.Theme
	// 量排版的缓存：同一段文字、同一宽度、同一套令牌不重算
	key    mdKey
	lay    mdLayout
	shaped mdShapeCache       // 段落排版缓存：流式时前面没变的段落不重排
	copied map[int]time.Time  // 代码块按下复制的时刻（按块序号），两秒内显示对勾
	morphs map[int]*IconMorph // 代码块复制钮的图标变形状态（按块序号）：复制 ↔ 对勾流过去
	blocks map[int]*mdEntry

	// 表格（table_md.go）：可用宽（构建前由调用方填，ContentW）、量过的各列、流式时上一帧的列宽、格子里的字用的选项
	width      float32
	tables     map[string]tableMeasure
	tablePrev  map[int][]float32
	cell       markdown.Options
	cellReveal markdown.Reveal

	// 整块出现时淡入（规格-过渡动画 C1 的“已播过”那一套）：optionsFor 每帧数一次；第一次画就在场的块（历史消息、滚出去又回来）不播，
	// 之后才冒出来的表格、代码块（流式、追加）淡入一次，不位移、不动高度。
	passes int
	fades  map[string]*blockFade
}

type blockFade struct{ play, seen bool }

type mdKey struct {
	src         string
	streaming   bool
	width, body float32
	font, mono  string
}

// mdLayout 是 chatui 按 markdown 同一套字体、行高量出来的正文排版（几何还是 chatui 自己算，不读上一帧的 Bounds）。
type mdLayout struct {
	Lines  []float32 // 段落里每行的宽（只算段落；有别的块时 Blocky）
	Paras  int
	Blocky bool    // 有列表、代码块、引用、标题、表格……：宽度取满，高度按源码行数估
	EstH   float32 // Blocky 时估的正文高
	LastW  float32 // 最后一块是段落时它最后一行的宽；否则 -1（时间另起一行）
}

// NewMD 建一个空的仓库。
func NewMD() *MD { return &MD{m: map[string]*mdEntry{}} }

func (s *MD) entry(id string) *mdEntry {
	e := s.m[id]
	if e == nil {
		e = &mdEntry{}
		s.m[id] = e
	}
	return e
}

// Len 是存着状态的消息条数（量内存、查泄漏用）。
func (s *MD) Len() int { return len(s.m) }

// Forget 丢掉不在 keep 里的消息的状态（宿主在消息被删、会话关掉时调）。
func (s *MD) Forget(keep func(id string) bool) {
	for id := range s.m {
		if !keep(id) {
			delete(s.m, id)
		}
	}
}

// monoFont 是等宽字族：没有 Maple Mono 时和 mygo markdown 一样退回 "monospace"（量和画用同一个）。
func (h Host) monoFont() string {
	if h.Mono == "" {
		return "monospace"
	}
	return h.Mono
}

// useMD 只由消息的 Markdown 属性决定，不从左右方向推断。
func (h Host) useMD(m *Msg) bool { return h.MD != nil && m.Markdown && m.State != Recalled }

// mdSource 是喂给 markdown 的正文：剥思考段、单个换行改硬换行；还在出字时按流式剥（藏写到一半的标签）。
func mdSource(m *Msg) string { return hardBreaks(stripThink(m.Text, m.State == Streaming)) }

// mdTheme 是 markdown 的主题，全部从令牌推：行高和 chatui 量行一样用令牌的正文行高（换成倍数给 markdown），段距一个正文字号（和纯文字气泡一致），
// 等宽字用 Host.Mono，代码块圆角按同心规则（气泡圆角 − 横内边距），不用 mygo 默认的"窗口圆角 + 2"。
func (h Host) mdTheme(th *markdown.Theme, ink ui.Color, acc Accent) *markdown.Theme {
	t := h.T
	*th = markdown.Theme{
		FontSize: t.Body, Font: h.Font, CodeFont: h.monoFont(), LineHeight: t.BodyLeading(), CodeLineHeight: 1.4,
		Text: ink, Muted: t.C.Text2, Link: acc.Text, Strong: ink, StrongWeight: 600,
		Code: ink, CodeBackground: t.C.Hover, CodeSize: mdCodeSize,
		BlockSpacing: 1, ItemSpacing: 0.25, ListIndent: 1.6,
		QuoteBar: t.C.LineStrong, QuoteText: t.C.Text2, QuoteBarWidth: 3, Rule: t.C.Line,
		CodeBlockBackground: t.C.Hover, CodeBlockRadius: max(t.Inner(t.R1, t.BubblePadH), t.RInnerMin),
		CodeBlockPadding: mdCodeInset, // 代码块外框是 codeBlock 钩子自己画的；mygo 的 Body 四边留同样的 0.25em
		TableBorder:      t.C.Line, TableHeader: t.C.Hover, TableCellPadding: [2]float32{0.6, 0.4}, TableMaxColumn: 24,
		Checkbox: acc.Text,
	}
	// 代码高亮配色跟着 chatui 自己的深浅走，不跟窗口主题（两边不同步时深色气泡里会用上浅色那套高亮，对比只有 1.6～2.2）
	if t.S.Dark {
		th.CodeColors = editor.DefaultTheme(ui.DarkTheme())
	} else {
		th.CodeColors = editor.DefaultTheme(ui.LightTheme())
	}
	sizes := [6]float32{1.35, 1.2, 1.1, 1, 1, 1} // 聊天里的标题压平一点，不像文档那么大
	for i := range th.Headings {
		th.Headings[i] = markdown.HeadingStyle{Size: sizes[i], Weight: 600, Color: ink, Top: 1, Bottom: 0.5}
	}
	return th
}

const (
	mdCodeSize  = 0.875 // 行内代码的字号（em）
	mdFade      = 130 * time.Millisecond
	mdCodeInset = 0.25 // mygo 代码块 Body 自己的内距（em），上边写死 0.25em，左右下跟 CodePadding 走，设成一样四边才等距（code.go:158）
)

// options 是这条消息这一帧的 markdown 设置：每条一份；停下、系统要求减少动态效果时整段直接出（mygo 只关淡入不改节奏，reveal.go:228）。
// chatMarkdown 是聊天里读的 Markdown 语法：mygo 0.8.3 起默认带 GitHub 的 @提及和 #123 引用，
// 聊天里的 @ 是叫人、#123 就是字，不按那一套读。
const chatMarkdown = parser.Default &^ (parser.ExtMentions | parser.ExtReferences)

func (h Host) options(c *ui.Context, m *Msg, ink ui.Color, acc Accent) *markdown.Options {
	return h.optionsFor(c, m, ink, acc, h.MD.entry(m.ID))
}

func (h Host) optionsFor(c *ui.Context, m *Msg, ink ui.Color, acc Accent, e *mdEntry) *markdown.Options {
	e.passes++
	e.reveal = markdown.Reveal{Unit: markdown.ByWord, Fade: mdFade}
	if m.State == Interrupted || c.Preferences().ReduceMotion {
		e.reveal.Unit = markdown.AtOnce
	}
	e.opts.Reveal = &e.reveal
	e.opts.Theme = h.mdTheme(&e.theme, ink, acc)
	e.opts.CodeBlock = h.codeBlock(e)
	e.opts.Extensions = chatMarkdown
	e.opts.OnLink = h.OnLink
	e.opts.ShareSelection = h.Selection != nil // 选区由 mdSelectable 外层的列持有，见 selection.go
	// 代码长行折行，不横向滚动：气泡里横向滚动的内容会越出滚动视口，几何检查已批的"滚动内容"例外只管竖向（ADR-285 第 11 条），
	// 要改成横向滚动得先请主人把那条例外扩到横向
	e.opts.WrapCode = true
	e.opts.Block = nil
	if hasTable(mdSource(m)) {
		e.opts.Block = h.tableBlock(e) // 表格自己排（table_md.go）；没有表格的消息不接管，免得每个块多包一层
	}
	return &e.opts
}

// mdMeasure 按 markdown 的解析和同一套字体量正文：段落用 ui.ShapeRichText 量每行宽（粗体、斜体、行内代码都按真实字形），
// 别的块不精确量，宽度取满、高度按源码行数估（只影响圆角里 min(r-1, 高/4) 那一项）。
func (h Host) mdMeasure(m *Msg, width float32) mdLayout {
	return h.mdMeasureIn(h.MD.entry(m.ID), m, width)
}

// mdMeasureIn 和 mdMeasure 一样，缓存放在调用方给的 e 里（分段消息每个文字块一份，互不顶掉）。
func (h Host) mdMeasureIn(e *mdEntry, m *Msg, width float32) mdLayout {
	t := h.T
	src := mdSource(m)
	k := mdKey{src, m.State == Streaming, width, t.Body, h.Font, h.Mono}
	if e.key == k && e.lay.Paras > 0 {
		return e.lay
	}
	d := parser.New(chatMarkdown)
	d.SetStreaming(k.streaming)
	d.Set(src)
	var lay mdLayout
	lay.LastW = -1
	font := ui.Font{Family: h.Font, Size: t.Body, LineHeight: t.BodyLeading()}
	noteNum := map[string]int{}
	notes := func(label string) int { // 脚注按第一次引用的顺序编号
		if noteNum[label] == 0 {
			noteNum[label] = len(noteNum) + 1
		}
		return noteNum[label]
	}
	e.shaped.begin()
	defer e.shaped.end()
	for i := range d.Children(0) {
		b := d.Block(i)
		lay.Paras++
		if b.Kind != parser.Paragraph {
			lay.Blocky = true
			lay.LastW = -1
			lay.EstH += float32(strings.Count(strings.TrimRight(src[b.Start:b.End], "\n"), "\n")+1) * t.BodyLine
			continue
		}
		ws := []float32{0}
		if spans := h.mdSpans(d.Inlines(i), notes); len(spans) > 0 {
			ws = e.shaped.lines(spans, font, width)
		}
		lay.Lines = append(lay.Lines, ws...)
		lay.EstH += float32(len(ws)) * t.BodyLine
		lay.LastW = ws[len(ws)-1]
	}
	if lay.Paras == 0 { // 还没有字（刚开始出、全是思考段）：按一行空段量
		lay.Paras, lay.Lines, lay.LastW, lay.EstH = 1, []float32{0}, 0, t.BodyLine
	}
	lay.EstH += float32(lay.Paras-1) * t.Body
	e.key, e.lay = k, lay
	return lay
}

// mdShapeCache 缓存每个段落量出来的各行宽。键是 mdSpans 转出来的 Span 序列（含全部文字、字体、字号、粗细）加宽度，
// 而 ShapeRichText 的结果只由这几样决定，所以命中和重排结果完全一致；整篇照旧每次重新解析（解析很便宜），
// 后面的文字改变前面块的解析（围栏没闭合、列表续行、setext 标题……）时前面段落的 Span 变了，键自然不同，不会用到旧结果。
// 脚注编号、引用定义这类跨段的东西也已经体现在 Span 的文字里。
// 淘汰：条目记最后一次用到的轮次，每次量完丢掉两轮都没用到的（流式里长出来又变掉的最后一段、改过宽度的旧排版）。
type mdShapeCache struct {
	m    map[string]*mdShaped
	pass int
}

type mdShaped struct {
	ws   []float32
	used int
}

// mdShapeCount 是 ShapeRichText 被调用的次数（测试用：流式每帧排版次数不该随正文变长）。
var mdShapeCount atomic.Int64

func (c *mdShapeCache) begin() { c.pass++ }

func (c *mdShapeCache) end() {
	for k, v := range c.m {
		if v.used < c.pass-1 {
			delete(c.m, k)
		}
	}
}

func (c *mdShapeCache) lines(spans []ui.Span, font ui.Font, width float32) []float32 {
	var kb strings.Builder
	fmt.Fprintf(&kb, "%v|%v|", width, font)
	for _, sp := range spans {
		fmt.Fprintf(&kb, "%#v;", sp)
	}
	key := kb.String()
	if v := c.m[key]; v != nil {
		v.used = c.pass
		return v.ws
	}
	mdShapeCount.Add(1)
	ws := []float32{}
	for _, ln := range ui.ShapeRichText(spans, font, width).Lines() {
		ws = append(ws, ln.Rect.W)
	}
	if len(ws) == 0 {
		ws = []float32{0}
	}
	if c.m == nil {
		c.m = map[string]*mdShaped{}
	}
	c.m[key] = &mdShaped{ws, c.pass}
	return ws
}

// mdSpans 把一个段落的行内节点照 mygo 画它的样子转成 Span，只用来量宽（颜色、底色、下划线、删除线不影响宽度，不转）。
// 照的是 ui/markdown/spans.go 的 inlines、inlineHTML、htmlStyle、addSpan：行内 HTML 的样式标签（<b>、<code>、<sub>……）
// 不出字、只改后面的样式，<br> 是换行，不认识的标签按字面；公式没设 InlineMath 时是等宽小字；图片是 "[说明]"；
// 脚注引用是 "[编号]" 的小字。notes 给脚注按第一次出现的顺序编号。
func (h Host) mdSpans(in []parser.Inline, notes func(label string) int) []ui.Span {
	t := h.T
	var out []ui.Span
	type style struct {
		weight            int
		italic, code, sub bool
	}
	var html []string // 开着的行内 HTML 样式标签（一个段落里）
	withHTML := func(s style) style {
		for _, tag := range html {
			switch tag {
			case "b", "strong":
				s.weight = 600
			case "i", "em":
				s.italic = true
			case "code", "kbd", "samp":
				s.code = true
			case "sub", "sup", "small":
				s.sub = true
			}
		}
		return s
	}
	add := func(text string, s style, mono bool, size float32) {
		sp := ui.Span{Text: text, Font: h.Font, Size: t.Body, Weight: s.weight, Italic: s.italic}
		if size != 0 {
			sp.Size = size
		}
		if s.code || mono {
			sp.Font = h.monoFont()
			if size == 0 {
				sp.Size = t.Body * mdCodeSize
			}
		}
		if s.sub {
			sp.Size *= 0.75
		}
		out = append(out, sp)
	}
	// htmlTag 照 inlineHTML：认得的标签改样式栈、不出字，<br> 出换行；返回 false 时按字面画
	htmlTag := func(tag string, s style) bool {
		name := strings.ToLower(strings.Trim(tag, "<>/ \t\n"))
		if k := strings.IndexAny(name, " \t\n"); k >= 0 {
			name = name[:k]
		}
		switch name {
		case "br":
			add("\n", s, false, 0)
			return true
		case "b", "strong", "i", "em", "u", "ins", "s", "del", "strike", "code", "kbd", "samp", "mark", "sub", "sup", "small":
		default:
			return false
		}
		if strings.HasPrefix(tag, "</") {
			for k := len(html) - 1; k >= 0; k-- {
				if html[k] == name {
					html = append(html[:k], html[k+1:]...)
					break
				}
			}
		} else if !strings.HasSuffix(tag, "/>") {
			html = append(html, name)
		}
		return true
	}
	var walk func(from, to int, s style)
	walk = func(from, to int, s style) {
		for k := from; k < to; k = in[k].Next {
			n := &in[k]
			switch n.Kind {
			case parser.Text:
				add(n.Text, withHTML(s), false, 0)
			case parser.SoftBreak:
				add(" ", s, false, 0)
			case parser.LineBreak:
				add("\n", s, false, 0)
			case parser.Code:
				cs := withHTML(s)
				cs.code = true
				add(n.Text, cs, false, 0)
			case parser.HTML:
				if !htmlTag(n.Text, s) {
					add(n.Text, s, false, 0)
				}
			case parser.Math: // 没设 InlineMath：TeX 原文按等宽字画，斜体时不缩小（spans.go 的回退）
				cs := withHTML(s)
				size := float32(0)
				if cs.italic {
					size = t.Body
				}
				add(n.Text, cs, true, size)
			case parser.Image:
				alt := parser.PlainText(in[k+1 : n.Next])
				if alt == "" {
					alt = "image"
				}
				add("["+alt+"]", s, false, 0)
			case parser.FootnoteRef:
				label := n.Text
				if num := notes(n.Text); num > 0 {
					label = strconv.Itoa(num)
				}
				cs := s
				cs.sub = true
				add("["+label+"]", cs, false, 0)
			case parser.Emphasis:
				walk(k+1, n.Next, style{s.weight, true, s.code, s.sub})
			case parser.Strong:
				walk(k+1, n.Next, style{600, s.italic, s.code, s.sub})
			case parser.Strikethrough, parser.Link:
				walk(k+1, n.Next, s)
			}
		}
	}
	walk(0, len(in), style{})
	return out
}

// timeReady 报这条消息右下角的时间现在能不能显示（ADR-286：出字的时候不显示，字全部出完、淡入完才显示）。
// 看 mygo 的 markdown.Revealed（0.8.0 起，egoist/mygo#1380）：出字、淡入时它自己每帧要下一帧，变成 true 那一帧时间就出来；
// 被停下的、减少动态效果的直接显示（ADR-286）；历史消息整段直接出，当帧就是 true。还没有字（md 是空元素）的完成消息直接显示。
func timeReady(c *ui.Context, m *Msg, md ui.Element) bool {
	switch {
	case m.State == Streaming:
		return false
	case m.State == Interrupted || c.Preferences().ReduceMotion: // 停下的字一下子出全，不等那一下淡入
		return true
	}
	return !md.Valid() || markdown.Revealed(md)
}

// codeBlock 是代码块钩子（规格-过程组件 3.7）：外框自己画——底色同卡片里的凹块、圆角同心（气泡圆角 − 横内边距）、四边内距一样、不画边；
// 上面一行左边语言、右边只有图标的复制按钮（点了两秒内换成对勾）；代码和高亮用 mygo 的 Body。
// 不用 mygo 的 DefaultCodeBlock：它的标题行、复制按钮上下左右内距不一样，复制按钮的字跟着系统语言（code.go:87-130）。
func (h Host) codeBlock(e *mdEntry) func(c *ui.Context, b *markdown.CodeBlockView) {
	return func(c *ui.Context, b *markdown.CodeBlockView) {
		t := h.T
		r := max(t.Inner(t.R1, t.BubblePadH), t.RInnerMin)
		inset := t.Body * mdCodeInset
		pad := max(0, t.S2-inset) // 外框内距 + Body 自己的 0.25em = s2：代码离框边和别的凹块一样远
		label := "代码"
		if b.Language != "" {
			label += "：" + b.Language
		}
		box := ui.Column(c.Key("code")).Background(t.C.Hover).Radius(r).Padding(pad).AlignSelf(ui.Stretch).Clip().Label(label)
		e.fadeIn(t, box, "code", b.Index)
		box.Children(func() {
			// 标题行只放语言名、四边内距一样：行高等于字高时字到框上边、左边一样远（复制按钮比字高，放进这一行会把它撑高、字居中后上面多出一截，主人 2026-11-06 截图）
			line := t.FS[1]
			ui.Row(c.Key("code-head")).FillWidth().Padding(inset).Children(func() {
				ui.Text(c, b.Language).Font(h.Font).FontSize(t.FS[1]).FixedLineHeight(line).TextColor(t.C.Text3).Grow(1).SingleLine().Unselectable().Translate(0, textNudge(t.FS[1]))
			})
			// 复制按钮浮在右上角：和标题行同一带（上下都离框 pad），离框右边也是 pad；圆角和框同心
			copied := false
			if at, ok := e.copied[b.Index]; ok {
				if rest := at.Add(2 * time.Second).Sub(c.Now()); rest > 0 {
					copied = true
					c.After(rest + time.Millisecond) // 到点那一帧把对勾换回来（排在整点之后，那一帧才算"过了两秒"）
				}
			}
			name, tip := "copy", "复制代码"
			if copied {
				name, tip = "check", "已复制"
			}
			side := line + 2*inset
			btn := ui.ButtonBase(c.Key("copy")).Label(tip).Absolute().Top(pad).Right(pad).Size(side, side).Center().
				Radius(max(t.Inner(r, pad), t.RInnerMin)).Disabled(b.Open)
			bg := ui.Transparent
			if btn.Hovered() && !b.Open {
				bg = t.C.Hover
			}
			btn.Background(t.Fill(btn, bg))
			if e.morphs == nil {
				e.morphs = map[int]*IconMorph{}
			}
			im := e.morphs[b.Index]
			if im == nil {
				im = &IconMorph{}
				e.morphs[b.Index] = im
			}
			btn.Children(func() { im.Show(c, "copy-icon", MorphGlyph(name, false), line, t.C.Text3, t.Spring.Snappy) })
			if btn.Clicked() {
				c.WriteClipboard(b.Code())
				if e.copied == nil {
					e.copied = map[int]time.Time{}
				}
				e.copied[b.Index] = c.Now()
				c.After(time.Millisecond) // 下一帧换成对勾，并从那里排到两秒后
			}
			b.Body(c)
		})
	}
}
