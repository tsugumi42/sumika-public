package content

import (
	"strings"
	"time"
	"unicode/utf16"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
	"golang.org/x/text/unicode/bidi"
)

// MarkdownState belongs to one message. Reuse it while that message is hidden.
// Expanded is keyed by the quote's byte offset. Spoilers reveal together.
type MarkdownState struct {
	Expanded         map[int]bool
	SpoilersRevealed bool
	doc              *parser.Document
	source           string
	blocks           []mdBlock
	revealAt         time.Time
}
type MarkdownOptions struct {
	Appearance
	OnLink                                             func(url string)
	OnCopyCode                                         func(code string)
	ExpandLabel, CollapseLabel, RevealLabel, CopyLabel string
	// Trailing is the bubble's invisible S×K bottom-info reservation.
	Trailing ui.Span
}
type mdBlock struct {
	kind           parser.Kind
	at             int
	code, language string
	fold           bool
	children       []mdBlock
	runs           []mdRun
}
type mdRun struct {
	value, link                         string
	bold, italic, code, strike, spoiler bool
}

// Markdown reads CommonMark emphasis, code, links, quotes and fenced code.
// >^ starts a collapsed quote; ||text|| marks spoilers. No resource is loaded.
func Markdown(c *ui.Context, source string, state *MarkdownState, o MarkdownOptions) ui.Element {
	if state.doc == nil {
		state.doc = parser.New(parser.ExtAutolinks | parser.ExtStrikethrough)
	}
	if state.source != source {
		state.doc.Set(source)
		state.source = source
		state.blocks = readBlocks(state.doc, 0)
	}
	t := theme.T(c)
	if state.SpoilersRevealed && state.revealAt.IsZero() {
		state.revealAt = c.Now()
	}
	if !state.SpoilersRevealed {
		state.revealAt = time.Time{}
	}
	p := colors(t, o.Appearance)
	selection := t.Colors().Message().In().BackgroundSelected()
	if o.Outgoing {
		selection = t.Colors().Message().Out().BackgroundSelected()
	}
	root := ui.Column(c).FillWidth().MinWidth(0).Font(t.Typography().Font()).FontSize(t.Typography().Message().Size() * c.Preferences().TextScale).FontWeight(t.Typography().Message().Weight()).TextColor(p.text).SelectionColor(selection).Disabled(o.Disabled)
	root.Children(func() { markdownBlocks(c, state.blocks, state, o) })
	return root
}
func readBlocks(d *parser.Document, parent int) []mdBlock {
	var blocks []mdBlock
	for i := range d.Children(parent) {
		b := d.Block(i)
		out := mdBlock{kind: b.Kind, at: b.Start}
		switch b.Kind {
		case parser.CodeBlock:
			out.code = d.Lines(i)
			out.language = b.Language()
		case parser.Paragraph, parser.Heading, parser.TableCell:
			spoiler := false
			out.runs = readRuns(d.Inlines(i), 0, len(d.Inlines(i)), mdRun{}, &spoiler)
		default:
			out.children = readBlocks(d, i)
			if b.Kind == parser.Quote && len(out.children) > 0 && len(out.children[0].runs) > 0 && strings.HasPrefix(out.children[0].runs[0].value, "^") {
				out.fold = true
				out.children[0].runs[0].value = strings.TrimPrefix(out.children[0].runs[0].value, "^")
				out.children[0].runs[0].value = strings.TrimPrefix(out.children[0].runs[0].value, " ")
			}
			if len(out.children) == 0 {
				out.code = d.Lines(i)
			}
		}
		blocks = append(blocks, out)
	}
	return blocks
}
func readRuns(inlines []parser.Inline, from, to int, style mdRun, spoiler *bool) []mdRun {
	var out []mdRun
	for i := from; i < to; {
		in := inlines[i]
		next := in.Next
		s := style
		switch in.Kind {
		case parser.Strong:
			s.bold = true
		case parser.Emphasis:
			s.italic = true
		case parser.Strikethrough:
			s.strike = true
		case parser.Link:
			s.link = in.Dest
		case parser.Code:
			s.code = true
			s.value = in.Text
			s.spoiler = *spoiler
			out = append(out, s)
		case parser.SoftBreak, parser.LineBreak:
			s.value = "\n"
			s.spoiler = *spoiler
			out = append(out, s)
		default:
			value := in.Text
			for {
				j := strings.Index(value, "||")
				if j < 0 {
					s.value = value
					s.spoiler = *spoiler
					if value != "" {
						out = append(out, s)
					}
					break
				}
				if j > 0 {
					s.value = value[:j]
					s.spoiler = *spoiler
					out = append(out, s)
				}
				*spoiler = !*spoiler
				value = value[j+2:]
			}
		}
		if next > i+1 {
			out = append(out, readRuns(inlines, i+1, next, s, spoiler)...)
		}
		i = next
	}
	return out
}
func markdownBlocks(c *ui.Context, blocks []mdBlock, state *MarkdownState, o MarkdownOptions) {
	t := theme.T(c)
	m := t.Metrics().Quote()
	p := colors(t, o.Appearance)
	for i, block := range blocks {
		blockOptions := o
		if i != len(blocks)-1 {
			blockOptions.Trailing = ui.Span{}
		}
		c.Key(block.at)
		switch block.kind {
		case parser.Quote:
			e := ui.Column(c).FillWidth().MinWidth(0).Padding(m.PaddingTop(), m.TextPaddingRight(), m.PaddingBottom(), m.PaddingLeft()).Radius(m.Radius()).Clip().Margin(m.VerticalGap(), 0).Background(p.bar.Alpha(m.BackgroundOpacity()))
			quoteBar(e, m.OutlineWidth(), m.Radius(), p.bar.Alpha(m.OutlineOpacity()))
			e.Children(func() {
				content := ui.Column(c).FillWidth().MinWidth(0)
				if block.fold && !state.Expanded[block.at] {
					_, line := c.MeasureText(0, ui.Span{Text: "M", Size: t.Typography().Message().Size() * c.Preferences().TextScale, Font: t.Typography().Font()})
					content.MaxHeight(line * m.TextCollapsedLines()).Clip()
				}
				inner := blockOptions
				inner.Trailing = ui.Span{}
				content.Children(func() { markdownBlocks(c, block.children, state, inner) })
				if block.fold {
					label := o.ExpandLabel
					if state.Expanded[block.at] {
						label = o.CollapseLabel
					}
					action(c, label, o.Disabled, func() {
						if state.Expanded == nil {
							state.Expanded = make(map[int]bool)
						}
						state.Expanded[block.at] = !state.Expanded[block.at]
					}).Children(func() { text(c, label, t.Typography().ContentTitle(), p.service) })
				}
			})
		case parser.CodeBlock:
			background := p.code.Alpha(m.BackgroundOpacity())
			if t.Dark() {
				background = t.Colors().Quote().CodeDarkBackground().Alpha(m.CodeDarkOpacity())
			}
			ui.Column(c).FillWidth().MinWidth(0).Margin(m.VerticalGap(), 0).Radius(m.Radius()).Clip().Background(background).Children(func() {
				ui.Row(c).FillWidth().Height(m.CodeHeaderHeight()).Padding(0, m.PaddingRight(), 0, m.PaddingLeft()).Background(p.code.Alpha(m.CodeHeaderOpacity())).Children(func() {
					text(c, block.language, t.Typography().ContentTitle(), p.code).Grow(1).SingleLine()
					var copyCode func()
					if o.OnCopyCode != nil {
						copyCode = func() { o.OnCopyCode(block.code) }
					}
					action(c, o.CopyLabel, o.Disabled, copyCode).Size(m.CodeHeaderHeight(), m.CodeHeaderHeight()).Children(func() {
						draw.Element(c, copyIcon, t.Typography().ContentTitle().Size(), t.Typography().ContentTitle().Size()).TextColor(p.code)
					})
				})
				ui.ScrollHorizontal(c).FillWidth().MinWidth(0).Padding(m.PaddingTop(), m.PaddingRight(), m.PaddingBottom(), m.PaddingLeft()).Children(func() {
					text(c, strings.TrimSuffix(block.code, "\n"), t.Typography().ContentCode(), p.code).Font("monospace").NoWrap().Selectable()
				})
			})
		case parser.Paragraph, parser.Heading, parser.TableCell:
			paragraph(c, block.runs, state, blockOptions)
		default:
			if len(block.children) > 0 {
				inner := blockOptions
				inner.Trailing = ui.Span{}
				ui.Column(c).FillWidth().MinWidth(0).Children(func() { markdownBlocks(c, block.children, state, inner) })
			} else {
				text(c, block.code, t.Typography().Message(), p.text).Selectable()
			}
		}
		// paint/message-bottom.md#没有回应时正文末行让时间:
		// a terminal quote/code block gets a separate reservation.
		if i == len(blocks)-1 && o.Trailing.ObjectWidth > 0 && block.kind != parser.Paragraph && block.kind != parser.Heading && block.kind != parser.TableCell {
			ui.Box(c).Size(o.Trailing.ObjectWidth, o.Trailing.ObjectHeight)
		}
	}
}
func paragraph(c *ui.Context, runs []mdRun, state *MarkdownState, o MarkdownOptions) {
	t := theme.T(c)
	p := colors(t, o.Appearance)
	root := ui.RichText(c).FillWidth().MinWidth(0).Selectable()
	if o.Trailing.ObjectWidth > 0 {
		root.FontSize(o.Trailing.Size)
	}
	var atlas *spoilerAtlas
	reduced := c.Preferences().ReduceMotion
	root.Children(func() {
		for _, run := range runs {
			if run.spoiler && !state.SpoilersRevealed {
				if atlas == nil {
					atlas = spoilerTexture(t, false)
					// Inline children share the paragraph's shaped layout; only its
					// owner receives DrawObjects in mygo v0.7.4.
					// Merge adjacent concealed objects into line bands instead of
					// submitting one clipped GPU image for every character.
					var pending ui.Rect
					last := 0
					root.Draw(func(_ *ui.Painter, _ ui.Rect) { pending = ui.Rect{} })
					root.DrawObjects(func(painter *ui.Painter, at int, r ui.Rect) {
						if pending.W > 0 && (at == last+1 || at == last-1) && pending.Y == r.Y && pending.H == r.H {
							right := max(pending.X+pending.W, r.X+r.W)
							pending.X = min(pending.X, r.X)
							pending.W = right - pending.X
						} else {
							if pending.W > 0 {
								atlas.draw(painter, pending, p.muted, reduced)
							}
							pending = r
						}
						last = at
					})
					root.DrawOver(func(painter *ui.Painter, _ ui.Rect) {
						if pending.W > 0 {
							atlas.draw(painter, pending, p.muted, reduced)
						}
					})
				}
				hiddenSpoiler(c, run, state, o)
				continue
			}
			s := ui.Span{Text: run.value, Size: t.Typography().Message().Size() * c.Preferences().TextScale, Italic: run.italic, Strikethrough: run.strike}
			if run.bold {
				s.Weight = t.Typography().ContentStrong().Weight()
			}
			if run.code {
				s.Font = "monospace"
				s.Color = p.code
			}
			e := ui.RichText(c, s)
			if run.link != "" {
				e.TextColor(p.link)
				if o.OnLink != nil {
					e.Cursor(ui.CursorPointer).Focusable().OnClick(func() { o.OnLink(run.link) })
				}
				if e.Hovered() {
					e.Underline()
				}
			}
			if run.spoiler && !state.revealAt.IsZero() {
				age := c.Now().Sub(state.revealAt)
				duration := t.Motion().Fade().Duration()
				if age < duration {
					opacity := 1 - float32(age)/float32(duration)
					e.TextBackground(p.muted.Alpha(opacity))
					c.AnimationFrame()
				}
			}
		}
		if o.Trailing.ObjectWidth > 0 {
			// paint/message-bottom.md#没有回应时正文末行让时间
			start, offset := 0, 0
			for i, run := range runs {
				if at := strings.LastIndexByte(run.value, '\n'); at >= 0 {
					start, offset = i, at+1
				}
			}
			rtl := c.Direction() == ui.RightToLeft
			found := false
			for i := start; i < len(runs) && !found; i++ {
				value := runs[i].value
				if i == start {
					value = value[offset:]
				}
				for _, r := range value {
					p, _ := bidi.LookupRune(r)
					if p.Class() == bidi.L {
						rtl, found = false, true
						break
					}
					if p.Class() == bidi.R || p.Class() == bidi.AL {
						rtl, found = true, true
						break
					}
				}
			}
			if rtl != (c.Direction() == ui.RightToLeft) {
				ui.Text(c, "\n")
			}
			ui.RichText(c, o.Trailing)
		}
	})
}

type spoilerMeasureKey struct{}
type spoilerMeasure struct {
	source, font string
	size         float32
	weight       int
	italic       bool
	spans        []ui.Span
}

func hiddenSpoiler(c *ui.Context, run mdRun, state *MarkdownState, o MarkdownOptions) {
	t := theme.T(c)
	e := ui.RichText(c).Label(o.RevealLabel).Focusable().Cursor(ui.CursorPointer).OnClick(func() { state.SpoilersRevealed = true })
	cache := ui.Local(e, spoilerMeasureKey{}, func() spoilerMeasure { return spoilerMeasure{} })
	size := t.Typography().Message().Size() * c.Preferences().TextScale
	font := t.Typography().Font()
	weight := t.Typography().Message().Weight()
	if run.bold {
		weight = t.Typography().ContentStrong().Weight()
	}
	if cache.source != run.value || cache.font != font || cache.size != size || cache.weight != weight || cache.italic != run.italic {
		cache.source, cache.font, cache.size, cache.weight, cache.italic = run.value, font, size, weight, run.italic
		cache.spans = cache.spans[:0]
		// Inline object height is ascent, not full line height; retain descent
		// so revealing a spoiler keeps the paragraph's baseline and extent.
		shape := ui.ShapeRichText([]ui.Span{{Text: run.value, Italic: run.italic}}, ui.Font{Family: font, Size: size, Weight: weight}, 0)
		lines := shape.Lines()
		position := 0
		lineIndex := 0
		// One opaque inline object per rune keeps long CJK spoilers wrappable and
		// keeps concealed text out of the accessibility tree and clipboard.
		for _, r := range run.value {
			if r == '\n' {
				cache.spans = append(cache.spans, ui.Span{Text: "\n"})
				lineIndex++
				position++
				continue
			}
			start := shape.CaretAt(ui.TextCaretPosition{Index: position, Affinity: ui.TextDownstream})
			position += utf16.RuneLen(r)
			end := shape.CaretAt(ui.TextCaretPosition{Index: position, Affinity: ui.TextUpstream})
			w := end.X - start.X
			if w < 0 {
				w = -w
			}
			line := lines[lineIndex]
			placeholder := "\uFFFC"
			if w == 0 {
				placeholder = "\u200B"
			}
			cache.spans = append(cache.spans, ui.Span{Text: placeholder, ObjectWidth: w, ObjectHeight: line.Ascent, ObjectDescent: line.Descent})
		}
	}
	e.Children(func() { ui.RichText(c, cache.spans...) })
}
