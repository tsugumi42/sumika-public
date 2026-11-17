package chatui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"sumika-native/internal/segview"
)

// blocks uses the host's existing immutable block slice directly.
func (h Host) blocks(c *ui.Context, m *Msg, l layout, ink ui.Color, acc Accent) (ui.Element, bool) {
	t := h.T
	side := t.BubblePadH - t.BubblePadV
	flush := l.Media != mediaNone
	rootW := l.ContentW + side*2
	if flush {
		rootW = l.ContentW + t.BubblePadH*2 // 铺满：气泡没有内边距，根就是气泡的内宽
	}
	root := ui.Column(c.Key("text")).Width(rootW).MinWidth(0).Gap(t.S2)
	ready := true
	mark, markLo, markHi := h.highlightIn(c, m)
	root.Children(func() {
		for i, b := range m.Blocks {
			blk := ui.Column(c.Key(i)).MinWidth(0) // 不写 FillWidth：它是 100% 宽、不扣外边距，带外边距的块会比父元素宽出一个外边距；列里默认拉伸，扣得对
			if flush && !isMediaBlock(b) {
				// 铺满的气泡没有内边距：文字、卡片自己缩进一个竖内边距，最后一块再留下边（下面有表情回应时由它留）
				bottom := float32(0)
				if i == len(m.Blocks)-1 && len(l.Chips) == 0 {
					bottom = t.BubblePadV
				}
				blk.Margin(0, t.BubblePadV, bottom, t.BubblePadV)
			}
			blk.Children(func() {
				switch b.Kind {
				case segview.Inline:
					if h.useMD(m) {
						part := *m
						part.Text = b.Plain()
						entry := h.MD.entry(m.ID)
						if entry.blocks == nil {
							entry.blocks = make(map[int]*mdEntry)
						}
						if entry.blocks[i] == nil {
							entry.blocks[i] = &mdEntry{}
						}
						opts := h.optionsFor(c, &part, ink, acc, entry.blocks[i])
						opts.OnLink = h.OnLink
						md := h.mdSelectable(c, "markdown", m, i, side, func(k *ui.Context) ui.Element {
							if m.State == Streaming {
								return markdown.Stream(k, mdSource(&part), opts)
							}
							return markdown.View(k, mdSource(&part), opts)
						})
						if !timeReady(c, &part, md) {
							ready = false
						}
					} else {
						lo, hi := -1, 0
						if i == mark {
							lo, hi = markLo, markHi
						}
						h.inlineBlock(c, m, i, b, ink, acc, lo, hi).Margin(0, side)
					}
				case segview.Images:
					h.imageGroup(c, m, b.Imgs, l.TextW, l.Media, rootW)
				case segview.Note, segview.StickerNote:
					ui.Text(c.Key("note"), b.Text).Font(h.Font).FontSize(t.Body).FixedLineHeight(t.BodyLine).TextColor(t.C.Text3).Padding(0, side).Wrap().Selectable()
				case segview.CardBlock:
					h.cardBlock(c, m.ID, b.Card, rootW)
				case segview.ForwardBlock:
					h.forwardBlock(c, m.ID, b.Card, acc)
				case segview.Video, segview.File:
					h.downloadBlock(c, m.ID, b.Video, b.VideoIndex, b.Kind == segview.Video, l.TextW, flush, rootW)
				}
			})
		}
	})
	return root, ready
}

// blockMeasure 是分段消息的版式量：照真正画的样子量每个文字块每一行的宽，不用固定像素估。
type blockMeasure struct {
	Lines   []float32 // 所有文字块每一行的宽（按顺序）
	LastW   float32   // 最后一块是文字时它最后一行的宽；否则 -1，时间另起一行
	Fit     float32   // 非文字块的自然宽度里最宽的那个（图片按图宽，其余取满）
	H       float32   // 正文高的估值（只用在圆角里 高/4 那一项）
	NonText bool      // 有图片、视频、文件、卡片、转发或注释
}

func (h Host) blockMeasure(m *Msg, width, flushW float32) blockMeasure {
	t := h.T
	bm := blockMeasure{LastW: -1}
	for i, b := range m.Blocks {
		if i > 0 {
			bm.H += t.S2
		}
		switch b.Kind {
		case segview.Inline:
			var ws []float32
			if h.useMD(m) {
				part := *m
				part.Text = b.Plain()
				entry := h.MD.entry(m.ID)
				if entry.blocks == nil {
					entry.blocks = make(map[int]*mdEntry)
				}
				if entry.blocks[i] == nil {
					entry.blocks[i] = &mdEntry{}
				}
				lay := h.mdMeasureIn(entry.blocks[i], &part, width)
				ws = lay.Lines
				bm.H += lay.EstH
				if lay.Blocky { // 列表、代码块……：不收紧，最后一块不是段落
					bm.NonText = true
					bm.Fit = width
					bm.LastW = -1
					bm.Lines = append(bm.Lines, ws...)
					continue
				}
			} else {
				ws = h.inlineLines(b, width)
				bm.H += float32(len(ws)) * t.BodyLine
			}
			if len(ws) == 0 {
				ws = []float32{0}
			}
			bm.Lines = append(bm.Lines, ws...)
			bm.LastW = ws[len(ws)-1]
		case segview.Images:
			bm.NonText = true
			bm.LastW = -1
			if flushW > 0 { // 铺满：高度按相册排法，宽度不参与收紧（气泡宽就是图宽）
				bm.H += albumLayout(b.Imgs, flushW).H
				continue
			}
			// 图不在最前面：缩在内边距里，同样按相册排法占位；图宽 = 气泡内宽，所以内容宽取"图宽 − 2·side"
			side := t.BubblePadH - t.BubblePadV
			a := albumLayout(b.Imgs, width+2*side)
			bm.Fit = max(bm.Fit, a.W-2*side)
			bm.H += a.H
		case segview.Video:
			bm.NonText = true
			bm.LastW = -1
			if flushW > 0 { // 铺满：高度按封面比例，宽度不参与收紧
				_, ht := singleMediaSize(b.Video, flushW)
				bm.H += ht
				continue
			}
			bm.Fit = max(bm.Fit, min(width, float32(mediaMaxW)-2*(t.BubblePadH-t.BubblePadV)))
			bm.H += 3 * t.BodyLine
		default:
			bm.NonText = true
			bm.LastW = -1
			bm.Fit = max(bm.Fit, h.cardFit(b, width))
			bm.H += 3 * t.BodyLine
		}
	}
	return bm
}

// 文件、转发、卡片的宽度上下限（设计像素，随字号缩放）：太窄放不下一行字，太宽就是主人 2026-11-09 说的那一大条。
const (
	cardMinW = 240
	cardMaxW = 360
)

// cardFit 是非文字块的自然宽度，和文字气泡同一个道理：内容有多宽气泡就多宽。返回气泡内容宽（不含两边的 side）。
func (h Host) cardFit(b segview.Block, width float32) float32 {
	t := h.T
	side := t.BubblePadH - t.BubblePadV
	longest := func(size float32, texts ...string) float32 {
		w := float32(0)
		for _, text := range texts {
			if text == "" {
				continue
			}
			for _, ln := range shapeLines(text, h.Font, size, 1.6, 1e6) {
				w = max(w, ln)
			}
		}
		return w
	}
	natural := float32(0)
	switch b.Kind {
	case segview.File:
		name := b.Video.DisplayName()
		meta := ""
		if b.Video.Size > 0 {
			meta = humanBytes(b.Video.Size) + " · 点一下下载"
		}
		natural = 44 + t.S2 + max(longest(t.FS[2], name)*1.05, longest(t.FS[1], meta)) + 2*t.S2
	case segview.ForwardBlock:
		title, count := "聊天记录", 0
		lines := []string{"没有可显示的消息"}
		if f := b.Card.Forward; f != nil {
			if f.Title != "" {
				title = f.Title
			}
			count = f.Count
			lines = f.Lines[:]
		}
		natural = max(longest(t.FS[2], title)*1.05, longest(t.FS[1], lines...), longest(t.FS[1], forwardFootText(count))) + 2*t.S2
	case segview.CardBlock:
		if card := b.Card.Card; card != nil {
			natural = longest(t.FS[2], card.Title, card.Description, card.Source) + 2*t.S2
			if b.Card.HasHash { // 有缩略图：按上限的宽，图才看得清
				natural = cardMaxW * t.TS
			}
		}
	default:
		// 灰字注释之类：就是一段字，量字（可以比卡片窄）
		text := longest(t.FS[1], b.Text)
		if text == 0 {
			return width
		}
		return min(width, text)
	}
	natural = min(max(natural, cardMinW*t.TS), cardMaxW*t.TS)
	return min(width, natural-2*side)
}

// inlineLines 量一个文字块每一行的宽；表情图按画的时候的占位宽度算，和 inlineBlock 用同一套判断。
func (h Host) inlineLines(b segview.Block, width float32) []float32 {
	t := h.T
	plain := true
	var spans []ui.Span
	for _, r := range b.Runs {
		sp := ui.Span{Text: r.Text, Font: h.Font, Size: t.Body}
		if r.Bold {
			sp.Weight, plain = t.Weight[2], false
		}
		if r.Face != nil && h.Media != nil {
			res := h.Media.Face(r.Face.ID)
			fallback := res.Description
			if fallback == "" || (r.Face.Kind == "superface" && fallback == segview.FaceFallback(r.Face.ID, "face")) {
				fallback = r.Text
			}
			if res.State == MediaReady && res.Image != nil {
				sp.Text = "￼"
				sp.ObjectWidth, sp.ObjectHeight = t.Body*1.375, t.Body*1.075
				plain = false
			} else {
				sp.Text = fallback
			}
		}
		spans = append(spans, sp)
	}
	if plain {
		return shapeLines(b.Plain(), h.Font, t.Body, 1.6, width)
	}
	var out []float32
	for _, ln := range ui.ShapeRichText(spans, ui.Font{Family: h.Font, Size: t.Body, LineHeight: 1.6}, width).Lines() {
		out = append(out, ln.Rect.W)
	}
	return out
}

type inlineFace struct {
	at   int
	img  ui.ImageSource
	copy string
}

// lo、hi 是要高亮的字符区间（点引用回来时被引的那一段），lo < 0 不高亮。
func (h Host) inlineBlock(c *ui.Context, m *Msg, index int, b segview.Block, ink ui.Color, acc Accent, lo, hi int) ui.Element {
	var faces []inlineFace
	type piece struct {
		span ui.Span
		url  string
	}
	var pieces []piece
	at := 0
	for _, mr := range markRuns(b.Runs, lo, hi) {
		r := mr.Run
		sp := ui.Span{Text: r.Text, Font: h.Font, Size: h.T.Body}
		if mr.mark {
			sp.Background, sp.Radius = h.highlightColor(), 3
		}
		if r.Bold {
			sp.Weight = h.T.Weight[2]
		}
		if r.Face != nil && h.Media != nil {
			res := h.Media.Face(r.Face.ID)
			fallback := res.Description
			if fallback == "" || (r.Face.Kind == "superface" && fallback == segview.FaceFallback(r.Face.ID, "face")) {
				fallback = r.Text
			}
			if res.State == MediaReady && res.Image != nil {
				faces = append(faces, inlineFace{at: at, img: res.Image, copy: fallback})
				sp.Text = "\uFFFC"
				sp.ObjectWidth, sp.ObjectHeight = h.T.Body*1.375, h.T.Body*1.075
			} else {
				sp.Text = fallback
			}
		}
		url := ""
		if r.Link != nil {
			url = r.Link.Open
			sp.Color = acc.Text
			sp.Underline = true
		}
		pieces = append(pieces, piece{sp, url})
		at += utf8.RuneCountInString(sp.Text)
	}
	// CopyData is read from a selectable box, not from the paragraph itself.
	scope := ui.Column(c.Key("inline")).MinWidth(0).Selectable() // 宽度交给列的拉伸：外面给它加了左右外边距，FillWidth 会让它比父元素宽出一个外边距
	if len(faces) > 0 {
		scope.CopyData(func(parts []ui.SelectedText) transfer.Data {
			var out strings.Builder
			for _, part := range parts {
				rs := []rune(part.Text)
				for n := max(0, part.Start); n < min(len(rs), part.End); n++ {
					if rs[n] != '\uFFFC' {
						out.WriteRune(rs[n])
						continue
					}
					found := false
					for _, f := range faces {
						if f.at == n {
							out.WriteString(f.copy)
							found = true
							break
						}
					}
					if !found {
						out.WriteString("[表情]")
					}
				}
			}
			return transfer.TextData(out.String())
		})
	}
	if h.Selection != nil {
		h.watchSelection(c, scope, m, index, faces, b.Plain(), func(width float32) *ui.TextLayout {
			spans := make([]ui.Span, len(pieces))
			for i, p := range pieces {
				spans[i] = p.span
			}
			return ui.ShapeRichText(spans, ui.Font{Family: h.Font, Size: h.T.Body, LineHeight: 1.6}, width)
		})
	}
	h.textMenu(c, scope, fmt.Sprintf("%s#%d", m.ID, index), m, faces)
	scope.Children(func() {
		root := ui.RichText(c).Font(h.Font).FontSize(h.T.Body).FixedLineHeight(h.T.BodyLine).TextColor(ink).Wrap().FillWidth()
		root.Children(func() {
			for i, p := range pieces {
				if p.url == "" {
					ui.RichText(c.Key(i), p.span)
					continue
				}
				link := ui.Link(c.Key(i), "", "")
				if h.OnLink != nil && link.Clicked() {
					h.OnLink(p.url)
				}
				link.Children(func() { ui.RichText(c, p.span) })
			}
		})
		if len(faces) > 0 {
			root.DrawObjects(func(p *ui.Painter, at int, r ui.Rect) {
				for _, f := range faces {
					if f.at == at {
						p.Image(f.img, r, ui.Contain)
						return
					}
				}
			})
		}
		if h.Selection != nil {
			h.selectionTool(c, fmt.Sprintf("%s#%d", m.ID, index))
		}
	})
	return scope
}

func mediaSize(s segview.Segment, width float32) (float32, float32) {
	w := min(width, float32(240))
	if w < 1 {
		w = 1
	}
	h := w * 0.75
	if s.Width > 0 && s.Height > 0 {
		h = w * float32(s.Height) / float32(s.Width)
	}
	return w, max(48, min(h, float32(320)))
}

func (h Host) imageGroup(c *ui.Context, m *Msg, imgs []segview.Segment, width float32, mode mediaMode, bubbleW float32) {
	if mode != mediaNone {
		h.albumGroup(c, m, imgs, mode, bubbleW)
		return
	}
	// 图不在最前面（文字在前）：缩在内边距里，圆角取最小内层圆角，排法和铺满时同一套 albumLayout
	t := h.T
	r := t.RInnerMin
	ui.Column(c.Key("pics")).Label("图片").Shrink(0).Radius(r, r, r, r).Clip().Children(func() {
		h.albumGroup(c, m, imgs, mediaFlush, width+2*(t.BubblePadH-t.BubblePadV))
	})
}

// imageCell 一张图：可点开大图、右键有菜单；图没好之前画占位文字，尺寸不变。
func (h Host) imageCell(c *ui.Context, m *Msg, key *ui.Context, s segview.Segment, w, ht, radius float32, fit ui.Fit) {
	id := m.ID
	result := MediaResult{}
	if h.Media != nil {
		result = h.Media.Image(id, s.Position, false, int(w))
	}
	b := ui.ButtonBase(key).Label("查看图片").Size(w, ht).Radius(radius).Background(h.T.C.Hover).Center().Shrink(0)
	if isStickerSeg(s) && fit == ui.Contain {
		b.Background(ui.Color{})
	}
	if h.OnViewImage != nil {
		pos := s.Position
		b.OnClick(func() { h.OnViewImage(id, pos) })
	}
	h.imageMenu(c, b, m, s.Position)
	b.Children(func() {
		if result.State == MediaReady && result.Image != nil {
			ui.Image(c.Key("image"), result.Image).Size(w, ht).Fit(fit)
			return
		}
		text := result.Description
		if text == "" {
			switch result.State {
			case MediaLoading:
				text = "[图片正在加载]"
			case MediaFailed:
				text = "[图片加载失败]"
			default:
				text = "[图片]"
			}
		}
		if s.Name != "" && result.State != MediaReady && result.State != MediaLoading {
			text += "\n" + s.Name // 本地附件取不到时写清是哪个文件（文件被删、挪走、读不到）
		}
		ui.Text(c.Key("placeholder"), text).Font(h.Font).FontSize(h.T.FS[1]).TextColor(h.T.C.Text3).Wrap()
	})
}

// albumGroup 铺满气泡的图片块：按 albumLayout 分行，格间细缝，格是直角（外侧圆角由气泡裁）。
func (h Host) albumGroup(c *ui.Context, m *Msg, imgs []segview.Segment, mode mediaMode, bubbleW float32) {
	a := albumLayout(imgs, bubbleW)
	ui.Column(c.Key("album")).Label("相册").Width(a.W).Height(a.H).Shrink(0).Gap(albumGap).Children(func() {
		n := 0
		for ri, row := range a.Rows {
			ui.Row(c.Key("row" + fmt.Sprint(ri))).Gap(albumGap).Height(row.H).Children(func() {
				for _, cw := range row.W {
					fit := ui.Cover
					if mode == mediaSticker || isStickerSeg(imgs[n]) { // 贴纸完整显示，不裁
						fit = ui.Contain
					}
					h.imageCell(c, m, c.Key("cell"+fmt.Sprint(n)), imgs[n], cw, row.H, 0, fit)
					n++
				}
			})
		}
	})
}

// cardSourceText 是卡片的来源。QQ 的卡片常写内部编号（com.tencent.miniapp_01），没人看得懂，换成人话。
func cardSourceText(source string) string {
	if strings.HasPrefix(source, "com.tencent.") {
		if strings.Contains(source, "miniapp") {
			return "QQ 小程序"
		}
		return "QQ 卡片"
	}
	return source
}

// cardBlock 是小程序、链接卡片：上面缩略图，下面标题、简介、来源，整张能点（打开卡片的链接）。
func (h Host) cardBlock(c *ui.Context, id string, s segview.Segment, width float32) {
	t := h.T
	if s.Card == nil {
		ui.Text(c, "[卡片]")
		return
	}
	card := s.Card
	radius := t.RInnerMin
	box := ui.ButtonBase(c.Key("card")).Label("卡片：" + card.Title).FillWidth().MinWidth(0).Justify(ui.Start).AlignItems(ui.Start).Radius(radius).Clip()
	bg := t.C.Hover
	if card.URL != "" && h.OnLink != nil {
		box.OnClick(func() { h.OnLink(card.URL) })
		if box.Hovered() { // 整张能点：悬停深一档，和转发卡一样
			bg = t.C.Press
		}
	}
	box.Background(t.Fill(box, bg))
	box.Children(func() {
		ui.Column(c.Key("card-body")).FillWidth().MinWidth(0).Children(func() {
			if s.HasHash && h.Media != nil && width > 0 {
				thumbH := round1(width * 9 / 16)
				res := h.Media.Image(id, s.Position, false, int(width))
				frame := ui.Box(c.Key("card-thumb")).Label("卡片图片").FillWidth().Height(thumbH).Shrink(0).Background(t.C.Press)
				if res.State == MediaReady && res.Image != nil {
					frame.Children(func() { ui.Image(c.Key("thumb"), res.Image).Width(width).Height(thumbH).Fit(ui.Cover) })
				}
			}
			ui.Column(c.Key("card-text")).FillWidth().MinWidth(0).Padding(t.S2).Gap(t.S1).Children(func() {
				if card.Title != "" {
					ui.Text(c.Key("title"), card.Title).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[2]).TextColor(t.C.Text).FillWidth().Wrap()
				}
				if card.Description != "" {
					ui.Text(c.Key("desc"), card.Description).Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text2).FillWidth().Wrap()
				}
				if source := cardSourceText(card.Source); source != "" {
					ui.Text(c.Key("source"), source).Font(h.Font).FontSize(t.FS[0]).TextColor(t.C.Text3).FillWidth().SingleLine()
				}
			})
		})
	})
}

// forwardStateText 把转发卡片的内部状态码换成人话；就绪（ready）返回空，不显示。
func forwardStateText(state string) string {
	switch state {
	case "", "ready":
		return ""
	case "fetching", "loading":
		return "正在加载转发内容"
	case "not_receiving":
		return "转发内容取不到：现在没在接收 QQ"
	case "refused":
		return "转发内容取不到：QQ 连接还没核验通过"
	case "timeout":
		return "转发内容取不到：QQ 没有及时回话"
	case "not_connected", "connection_lost":
		return "转发内容取不到：和 QQ 的连接还没准备好"
	case "too_large":
		return "转发内容取不到：这条聊天记录太大"
	}
	return "转发内容取不到：QQ 那边已经没有这条记录了"
}

func forwardFootText(count int) string {
	if count > 0 {
		return fmt.Sprintf("查看 %d 条转发消息", count)
	}
	return "查看转发消息"
}

// forwardBlock 是合并转发卡片（照 QQ，参考 Telegram 的转发样式）：
// 铺满气泡宽度，标题、最多几行“发送人：内容”预览、一条细线、底部“查看 N 条转发消息”；整张卡都能点。
// 状态码不外露：加载中、取不到换成人话，就绪不显示。
func (h Host) forwardBlock(c *ui.Context, id string, s segview.Segment, acc Accent) {
	t := h.T
	f := s.Forward
	b := ui.ButtonBase(c.Key("forward")).Label("查看转发消息").FillWidth().MinWidth(0).Justify(ui.Start).AlignItems(ui.Start).
		Padding(t.S2).Radius(t.RInnerMin)
	bg := t.C.Hover
	if h.OnForward != nil {
		b.OnClick(func() { h.OnForward(id, s.Position) })
		if b.Hovered() {
			bg = t.C.Press
		}
	}
	b.Background(t.Fill(b, bg))
	b.Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Gap(t.S1).Children(func() {
			title, count, missing, state := "聊天记录", 0, 0, ""
			var lines []string
			if f != nil {
				if f.Title != "" {
					title = f.Title
				}
				count, missing, state = f.Count, f.Missing, f.State
				for _, ln := range f.Lines {
					if ln != "" {
						lines = append(lines, ln)
					}
				}
			}
			ui.Text(c.Key("title"), title).Font(h.Font).FontSize(t.FS[2]).FontWeight(t.Weight[2]).TextColor(t.C.Text).FillWidth().SingleLine()
			stateText := forwardStateText(state)
			switch {
			case stateText != "":
				ui.Text(c.Key("state"), stateText).Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text3).FillWidth().Wrap()
			case len(lines) == 0:
				ui.Text(c.Key("empty"), "没有可显示的消息").Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text3).FillWidth().SingleLine()
			}
			if stateText == "" {
				for i, ln := range lines {
					ui.Text(c.Key(i), ln).Font(h.Font).FontSize(t.FS[1]).TextColor(t.C.Text2).FillWidth().SingleLine()
				}
			}
			ui.Box(c.Key("rule")).FillWidth().Height(1).Background(t.C.Line)
			foot := forwardFootText(count)
			if missing > 0 {
				foot += fmt.Sprintf("（%d 条没取下来）", missing)
			}
			ui.Text(c.Key("foot"), foot).Font(h.Font).FontSize(t.FS[1]).TextColor(acc.Text).FillWidth().SingleLine()
		})
	})
}

func (h Host) downloadBlock(c *ui.Context, id string, s segview.Segment, pos int, video bool, width float32, flush bool, bubbleW float32) {
	if !video {
		h.fileBar(c, id, s, pos)
		return
	}
	if flush {
		w, ht := singleMediaSize(s, bubbleW)
		h.videoCard(c, id, s, pos, w, ht, 0) // 铺满：气泡裁圆角，卡片自己是直角
		return
	}
	w, ht := mediaSize(s, width)
	h.videoCard(c, id, s, pos, w, ht, h.T.RInnerMin)
}
