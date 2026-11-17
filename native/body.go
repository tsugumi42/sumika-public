package main

import (
	"slices"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/overlay"
	"sumika-native/internal/segview"
)

// 缓存属于 app 的窗口；转发层和会话另外隔离，同号消息不能共享选区。
type bodyKey struct {
	source       uint64
	conversation string
	layer        *forwardLayer
	message      string
	shell        int
}
type bodyShell struct {
	selection chat.BodySelection
	parts     map[int]*bodyParagraph
	seen      bool
	color     ui.Color
}
type bodyParagraph struct {
	state       chat.BodyState
	runs        []segview.Run
	doc         inlineDoc
	links       []chat.BodyLink
	label       string
	faceVersion int
	size        float32
	ink         ui.Color
	seen        bool
}
type bodyStore struct{ shells map[bodyKey]*bodyShell }

func (s *bodyStore) begin() {
	for _, shell := range s.shells {
		shell.seen = false
		for _, p := range shell.parts {
			p.seen = false
		}
	}
}
func (s *bodyStore) end() {
	for key, shell := range s.shells {
		if !shell.seen {
			delete(s.shells, key)
			continue
		}
		for i, p := range shell.parts {
			if !p.seen {
				delete(shell.parts, i)
			}
		}
	}
}
func (a *app) bodyShell(m *msg, shell int) *bodyShell {
	key := bodyKey{a.srcGen, a.sel, a.forward.rendering, m.id, shell}
	if a.bodies.shells == nil {
		a.bodies.shells = make(map[bodyKey]*bodyShell)
	}
	state := a.bodies.shells[key]
	if state == nil {
		state = &bodyShell{parts: make(map[int]*bodyParagraph)}
		a.bodies.shells[key] = state
	}
	state.seen = true
	return state
}

func (a *app) retainedInline(c *ui.Context, shell *bodyShell, index int, b segview.Block, cp *copyMap, mine bool, tail ui.Span, small bool) ui.Element {
	p := shell.parts[index]
	if p == nil {
		p = &bodyParagraph{}
		shell.parts[index] = p
	}
	p.seen = true
	f, e := a.fs(), a.envFor(c)
	if small {
		e.size = c.Theme().Rem(0.75)
		e.font.Size = e.size
	}
	ink := c.Theme().Accent
	if mine {
		ink = c.Theme().AccentText
	}
	if p.runs == nil || p.faceVersion != f.ver || p.size != e.size || p.ink != ink || !slices.Equal(p.runs, b.Runs) {
		p.runs = append(p.runs[:0], b.Runs...)
		p.faceVersion, p.size, p.ink = f.ver, e.size, ink
		p.doc = inlineOf(b, e.size, f.table, f.state)
		p.label = plainNamed(b, f.table)
		p.links = p.links[:0]
		// 链接样式和命中范围同按显示后的 rune 位置；缺图变文字时也重新计算。
		if hasLinks(b) {
			p.doc.spans = p.doc.spans[:0]
			at := 0
			for _, r := range b.Runs {
				part := inlineOf(segview.Block{Runs: []segview.Run{r}}, e.size, f.table, f.state)
				start := at
				for _, span := range part.spans {
					at += utf8.RuneCountInString(span.Text)
					if r.Link != nil {
						span.Color = ink
						span.Underline = true
					}
					p.doc.spans = append(p.doc.spans, span)
				}
				if r.Link != nil {
					p.links = append(p.links, chat.BodyLink{Start: start, End: at, Label: r.Text, URL: r.Link.Open})
				}
			}
		}
	}
	for _, id := range p.doc.imgs {
		f.want(id)
	}
	paint := a.faceObjectPainter(p.doc, e.size)
	var objects []ui.TextObject
	if drawCheck != nil {
		original := paint
		paint = func(painter *ui.Painter, at int, r ui.Rect) {
			if _, ok := p.doc.faceAt(at); ok {
				objects = append(objects, ui.TextObject{Index: at, Rect: r})
			}
			original(painter, at, r)
		}
	}
	root := chat.RetainedBody(c, &p.state, p.doc.spans, chat.BodyTextOptions{
		Width: e.bodyW, Font: e.font, Direction: c.Direction(), Color: shell.color, Label: p.label,
		Selection: &shell.selection, Trailing: tail, Links: p.links, DrawObject: paint,
		LinkTip: func(link ui.Element, url string) {
			ui.Overlay(c, func() { overlay.Tooltip(c.Key(link.ID()), link, clipURL(url), overlay.TooltipOptions{}) })
		},
	})
	cp.add(root.ID(), p.doc.copies)
	if drawCheck != nil {
		root.DrawOver(func(_ *ui.Painter, r ui.Rect) {
			for i := range objects {
				objects[i].Rect.X -= r.X
				objects[i].Rect.Y -= r.Y
			}
			drawCheck(r, p.doc.spans, objects)
			objects = objects[:0]
		})
	}
	if url := p.state.ClickedLink(); url != "" {
		a.openLink(c, url)
	}
	return root
}
