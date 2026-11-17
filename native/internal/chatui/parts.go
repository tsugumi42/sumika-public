package chatui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// 分块消息（规格-引用）的气泡：主人一条消息由有序的几块组成，按顺序画——引用块画"被引的人 + 被引的那一段 + 针对它的回复 + 附件"，
// 普通块画"文字 + 附件"。点引用块滚回原消息并高亮被引的那一段。

// PartQuote 是一个引用块的出处（宿主从后台的 QuoteView 转来）。
type PartQuote struct {
	Target     string // 被引消息的事件 id
	Name       string // 被引的人
	Snippet    string // 被引的那一段；整条引用时是整条（后台已截到 1000 字）
	Original   string // 被引消息的原文，Start、End 是 Snippet 在它里面的字符偏移
	Start, End int
	Whole      bool // 引的是整条
	Fallback   bool // 选中的文字没对上原文，Snippet 是原文开头：不高亮
	Truncated  bool
	Gone       bool // 被引的那条后来看不到了：只画回复文字
}

// PartAtt 是一块上的附件。图片附件宿主能给图时（Show）画成真图，否则、以及文件，都是一个"图标加名字"的小标签。
type PartAtt struct {
	Name  string
	Image bool
	// Show 为真：图画成真图。Pos 是它在这条消息展示段里的序号（图从 Host.Media 取、点开大图都按它认），
	// Width、Height 是像素宽高：加载前就按比例占好位置，加载完不跳；0 表示不知道，按 4:3 占位。
	Show          bool
	Pos           int
	Width, Height int
}

// Part 是一条分块消息的一块：Quote 为 nil 是普通文字块。
type Part struct {
	Quote *PartQuote
	Text  string
	Atts  []PartAtt
}

// Highlight 是"点引用回到原消息后"要在原文里高亮的那一段，窗口里一份，宿主持有。
type Highlight struct {
	MessageID  string
	Text       string // 要高亮的文字
	Original   string // 整条原文；Start、End 对得上它时直接按偏移
	Start, End int
	Whole      bool
	since      time.Time // 第一次画出来的时刻：高亮从这里起亮 highlightSpan
}

// highlightSpan 是高亮亮多久。
const highlightSpan = 2500 * time.Millisecond

// active 报告这一帧要不要高亮这条消息，第一次画出来时计时。
func (hl *Highlight) active(c *ui.Context, id string) bool {
	if hl == nil || hl.MessageID != id || hl.Text == "" {
		return false
	}
	if hl.since.IsZero() {
		hl.since = c.Now()
	}
	left := highlightSpan - c.Now().Sub(hl.since)
	if left <= 0 {
		return false
	}
	c.After(left)
	return true
}

// rangeIn 在一段纯文字里找要高亮的字符区间：整条引用高亮整段；偏移能对上整条原文时按偏移，否则找文字的第一处。
func (hl *Highlight) rangeIn(plain string, single bool) (lo, hi int, ok bool) {
	n := utf8.RuneCountInString(plain)
	switch {
	case plain == "":
		return 0, 0, false
	case hl.Whole:
		return 0, n, true
	case single && hl.Start < hl.End && hl.End <= n && plain == hl.Original:
		return hl.Start, hl.End, true
	}
	i := strings.Index(plain, hl.Text)
	if i < 0 {
		return 0, 0, false
	}
	lo = utf8.RuneCountInString(plain[:i])
	return lo, lo + utf8.RuneCountInString(hl.Text), true
}

// highlightIn 找这条消息里第一个含被引片段的行内块，返回块序号和区间；没有返回 -1。
func (h Host) highlightIn(c *ui.Context, m *Msg) (index, lo, hi int) {
	if !h.Highlight.active(c, m.ID) {
		return -1, 0, 0
	}
	inline := 0
	for _, b := range m.Blocks {
		if b.Kind == segview.Inline {
			inline++
		}
	}
	for i, b := range m.Blocks {
		if b.Kind != segview.Inline {
			continue
		}
		if lo, hi, ok := h.Highlight.rangeIn(b.Plain(), inline == 1); ok {
			return i, lo, hi
		}
	}
	return -1, 0, 0
}

// markedRun 是一段带"是否高亮"标记的行内文字。
type markedRun struct {
	segview.Run
	mark bool
}

// markRuns 按字符区间 [lo, hi) 把行内文字切开并标记；lo < 0 表示不高亮。表情占一个整体：碰到区间就整个高亮。
func markRuns(runs []segview.Run, lo, hi int) []markedRun {
	out := make([]markedRun, 0, len(runs)+2)
	at := 0
	for _, r := range runs {
		n := utf8.RuneCountInString(r.Text)
		switch {
		case lo < 0 || n == 0:
			out = append(out, markedRun{Run: r})
		case r.Face != nil:
			out = append(out, markedRun{Run: r, mark: at < hi && at+n > lo})
		default:
			rs := []rune(r.Text)
			a, b := min(max(lo-at, 0), n), min(max(hi-at, 0), n)
			for _, piece := range []struct {
				from, to int
				mark     bool
			}{{0, a, false}, {a, b, true}, {b, n, false}} {
				if piece.from >= piece.to {
					continue
				}
				cut := r
				cut.Text = string(rs[piece.from:piece.to])
				out = append(out, markedRun{Run: cut, mark: piece.mark})
			}
		}
		at += n
	}
	return out
}

func (h Host) highlightColor() ui.Color { return h.T.AccentOf(h.SelfChar).Base.Alpha(0.35) }

// partsBlock 画一条分块消息的正文：各块按顺序，块与块之间隔一个竖内边距。
// 图片在最前面（这一块没有引用）时和图片气泡一样铺满气泡、不留内边距（mediabubble.go），后面的文字和标签自己缩进；
// 否则图画在引用、文字的下面，带圆角，离气泡边一个竖内边距。
func (h Host) partsBlock(c *ui.Context, m *Msg, l layout, ink ui.Color, acc Accent) ui.Element {
	t := h.T
	pv := t.BubblePadV
	side := t.BubblePadH - pv
	flush := l.Media != mediaNone
	rootW := l.ContentW + side*2
	if flush {
		rootW = l.ContentW + t.BubblePadH*2 // 铺满：气泡没有内边距，根就是气泡的内宽
	}
	root := ui.Column(c.Key("text")).Width(rootW).MinWidth(0).Gap(pv)
	hl := h.Highlight.active(c, m.ID)
	root.Children(func() {
		for i, p := range m.Parts {
			first, last := i == 0, i == len(m.Parts)-1
			imgs, files := p.split()
			bottom := float32(0)
			if last && len(l.Chips) == 0 { // 下面有表情回应条时，下边的留白由它留
				bottom = pv
			}
			// FillWidth 是 100% 宽、不扣外边距：带外边距的列不写它，列里默认拉伸、会扣掉外边距
			col := ui.Column(c.Key(i)).MinWidth(0).Gap(t.S1)
			if !flush {
				col.FillWidth()
			}
			if flush && first {
				col.Gap(0) // 引用卡、图、文字之间的间距由各自的外边距定
			} else if flush {
				col.Margin(0, pv, bottom, pv) // 铺满的气泡没有内边距：后面的块自己缩进，最后一块再留下边
			}
			col.Children(func() {
				if q := p.Quote; q != nil {
					if flush && first {
						// 图铺满、气泡没有内边距：引用卡自己离上、左、右、下各一个竖内边距，图接在它下面
						top := pv
						if l.ShowName {
							top = t.S1
						}
						ui.Column(c.Key("quote-box")).MinWidth(0).Margin(top, pv, pv, pv).Children(func() {
							h.partQuote(c, m, q, !l.ShowName, l, acc)
						})
					} else {
						h.partQuote(c, m, q, i == 0 && !l.ShowName, l, acc)
					}
				}
				if len(imgs) > 0 {
					h.partAlbum(c, m, imgs, l, flush && first, first && p.Quote == nil && !l.ShowName)
				}
				rest := func() {
					if strings.TrimSpace(p.Text) != "" {
						h.partText(c, m, i, p.Text, ink, acc, side, hl)
					}
					if len(files) > 0 {
						ui.Row(c.Key("atts")).Label("附件").FillWidth().Gap(t.S1).Wrap().Padding(0, side).Children(func() {
							for j, a := range files {
								h.partAtt(c.Key(j), a)
							}
						})
					}
				}
				if flush && first {
					if strings.TrimSpace(p.Text) != "" || len(files) > 0 {
						ui.Column(c.Key("rest")).MinWidth(0).Gap(t.S1).Margin(t.S2, pv, bottom, pv).Children(rest)
					}
					return
				}
				rest()
			})
		}
	})
	return root
}

// partText 是一块的回复文字：可选、可以选中后回复；这条消息正被引用跳回来时，被引的那一段高亮。
func (h Host) partText(c *ui.Context, m *Msg, i int, text string, ink ui.Color, acc Accent, side float32, hl bool) {
	t := h.T
	scope := ui.Column(c.Key("reply")).MinWidth(0).FillWidth().Padding(0, side).Selectable()
	if h.Selection != nil {
		h.watchSelection(c, scope, m, i, nil, text, nil)
	}
	h.textMenu(c, scope, fmt.Sprintf("%s#%d", m.ID, i), m, nil)
	scope.Children(func() {
		e := ui.RichText(c).Font(h.Font).FontSize(t.Body).FixedLineHeight(t.BodyLine).TextColor(ink).Wrap().FillWidth()
		lo, hi, ok := 0, 0, false
		if hl {
			lo, hi, ok = h.Highlight.rangeIn(text, len(m.Parts) == 1)
		}
		if !ok {
			lo = -1
		}
		e.Children(func() {
			for k, r := range markRuns([]segview.Run{{Text: text}}, lo, hi) {
				sp := ui.Span{Text: r.Text, Font: h.Font, Size: t.Body}
				if r.mark {
					sp.Background, sp.Radius = h.highlightColor(), 3
				}
				ui.RichText(c.Key(k), sp)
			}
		})
		if h.Selection != nil {
			h.selectionTool(c, fmt.Sprintf("%s#%d", m.ID, i))
		}
	})
}

// partQuote 是一个引用块：被引的人和那一段（最多三行），点一下滚回原消息并高亮；被引的那条看不到了就只留一句说明。
// first 为真（在气泡最上面）时上面两角和气泡圆角同心，其余都是最小内层圆角。
func (h Host) partQuote(c *ui.Context, m *Msg, q *PartQuote, first bool, l layout, acc Accent) {
	t := h.T
	in := t.BubblePadH - t.BubblePadV
	name, text := q.Name, q.Snippet
	if name == "" {
		name = "对方"
	}
	if q.Gone {
		ui.Text(c.Key("gone"), "引用的消息已经看不到了").Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text3).Padding(0, in).SingleLine()
		return
	}
	r := Corners{t.RInnerMin, t.RInnerMin, t.RInnerMin, t.RInnerMin}
	if first {
		r = QuoteCorners(t, l.Corners, false)
	}
	line := round1(t.FS[2] * 1.45)
	b := ui.ButtonBase(c.Key("quote")).Label("引用 " + name + "：点一下回到原消息").FillWidth().Padding(in).Radius(r[:]...).Background(t.C.Hover).AlignItems(ui.Start)
	if h.OnQuotePart != nil {
		b.OnClick(func() { h.OnQuotePart(m.ID, *q) })
	}
	b.Children(func() {
		ui.Row(c.Key("row")).FillWidth().Gap(t.S2).Children(func() {
			ui.Box(c.Key("bar")).Width(quoteBarBase * t.TS).AlignSelf(ui.Stretch).Radius(quoteBarBase * t.TS / 2).Background(acc.Base)
			ui.Column(c.Key("q")).MinWidth(0).Grow(1).Margin(0, t.S2-in, 0, 0).Children(func() {
				ui.Text(c.Key("qn"), name).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[2]).FixedLineHeight(line).TextColor(acc.Text).SingleLine().Translate(0, textNudge(t.FS[2]))
				ui.Text(c.Key("qt"), text).Font(h.Font).FontSize(t.FS[2]).FixedLineHeight(line).TextColor(t.C.Text2).MaxLines(3).Wrap().FillWidth().Translate(0, textNudge(t.FS[2]))
			})
		})
	})
}

// partAtt 是气泡里的一个附件小条：图标加名字。
func (h Host) partAtt(c *ui.Context, a PartAtt) {
	t := h.T
	icon := "file"
	if a.Image {
		icon = "pic"
	}
	ui.Row(c).Label("附件："+a.Name).Height(t.HSm).Padding(0, t.S2).Gap(t.S1).AlignItems(ui.Center).Radius(t.RInnerMin).Background(t.C.Hover).Children(func() {
		ui.Icon(c.Key("icon"), Icon(icon, false)).Size(t.FS[2], t.FS[2]).Shrink(0).TextColor(t.C.Text3)
		ui.Text(c.Key("name"), a.Name).Font(h.Font).FontSize(t.FS[2]).TextColor(t.C.Text2).MaxWidth(attName).SingleLine()
	})
}
