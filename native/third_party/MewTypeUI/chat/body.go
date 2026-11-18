package chat

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/go-text/typesetting/segmenter"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
	"golang.org/x/text/unicode/bidi"
)

type BodyOptions struct {
	Side     Side
	Selected bool
	Image    bool
}

// InfoReservation is the single tail object transferred to the last visible
// text or reaction list. paint/message-bottom.md#统一占位.
func InfoReservation(c *ui.Context, info Info, o BodyOptions) ui.Span {
	if info == (Info{}) {
		return ui.Span{}
	}
	b := measureInfo(c, info, o)
	m := theme.T(c).Metrics().Message()
	// A nonpainting shaper seed removes the default font's descent from an
	// object-only line; all visible spans carry their real font size.
	return ui.Span{Text: "\uFFFC", Size: 0.0001, ObjectWidth: b.width + m.TimestampSpace() - m.TimestampDeltaX(), ObjectHeight: max(0, b.height-m.TimestampDeltaY())}
}

// Body reserves the actual shaped tail rather than a separate footer line.
func Body(c *ui.Context, value string, info Info, o BodyOptions) ui.Element {
	m := theme.T(c).Metrics().Message()
	root := ui.Column(c).MinWidth(0).MaxWidth(m.MaxWidth() - m.PaddingLeft() - m.PaddingRight())
	return root.Children(func() {
		bodyText(c, value, InfoReservation(c, info, o), o)
		if info != (Info{}) {
			BottomInfo(c, info, o).Absolute().Right(-m.TimestampDeltaX()).Bottom(-m.TimestampDeltaY())
		}
	})
}

func bodyText(c *ui.Context, value string, tail ui.Span, o BodyOptions) ui.Element {
	t := theme.T(c)
	_, fg, _, _, _ := messageColors(t, o.Side, o.Selected)
	// paint/message-bottom.md#没有回应时正文末行让时间: only a direction
	// mismatch forces the reservation onto another line.
	if tail.ObjectWidth > 0 && lastParagraphRTL(value, c.Direction()) != (c.Direction() == ui.RightToLeft) {
		value += "\n"
	}
	root := ui.RichText(c, span(c, value, t.Typography().Message(), fg), tail).MinWidth(0)
	if tail.ObjectWidth > 0 {
		root.FontSize(tail.Size)
	}
	return root
}

type infoGeometry struct {
	width, height, dateWidth, signatureWidth float32
	date                                     string
	sending                                  bool
}

func measureInfo(c *ui.Context, info Info, o BodyOptions) infoGeometry {
	t := theme.T(c)
	m := t.Metrics().Message()
	b := infoGeometry{date: info.Label, sending: info.Delivery == Sending || info.Delivery == Failed}
	if info.Prefix != "" {
		b.date = info.Prefix + " " + b.date
	}
	b.dateWidth, _ = c.MeasureText(0, span(c, b.date, t.Typography().MessageTime(), ui.Color{}))
	_, b.height = c.MeasureText(0, span(c, "M", t.Typography().MessageTime(), ui.Color{}))
	b.width = b.dateWidth
	if info.Signature != "" {
		sw, _ := c.MeasureText(0, span(c, info.Signature+", ", t.Typography().MessageTime(), ui.Color{}))
		b.signatureWidth = max(0, min(sw, m.SignatureMaxWidth()-b.dateWidth))
		b.width += b.signatureWidth
	}
	if o.Side == Outgoing || b.sending {
		b.width += m.ReceiptSpace()
	}
	if !b.sending {
		if info.Views != "" {
			w, _ := c.MeasureText(0, span(c, info.Views, t.Typography().MessageTime(), ui.Color{}))
			b.width += m.ViewsWidth() + w + m.ViewsSpace()
		}
		if info.Replies != "" && info.Replies != "0" && !info.RepliesContext && !info.Shortcut {
			w, _ := c.MeasureText(0, span(c, info.Replies, t.Typography().MessageTime(), ui.Color{}))
			b.width += m.ViewsWidth() + w + m.ViewsSpace()
		}
	}
	if info.Pinned {
		b.width += m.PinWidth()
	}
	if info.Ephemeral {
		b.width += m.EphemeralWidth()
	}
	if info.Silent {
		b.width += m.SilentWidth()
	}
	return b
}

// BottomInfo renders one indivisible information block. Slot coordinates use
// the date line bottom, not its baseline. paint/message-bottom.md#基线与图标槽.
func BottomInfo(c *ui.Context, info Info, o BodyOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Message()
	b := measureInfo(c, info, o)
	_, _, _, _, date := messageColors(t, o.Side, o.Selected)
	if o.Image {
		date = t.Colors().Message().TimestampImgForeground()
	}
	root := ui.Box(c).Size(b.width, b.height).Label(info.Label)
	root.Children(func() {
		x := float32(0)
		icon := func(svg *ui.SVG, label string, left, top, w, h float32, color ui.Color) {
			// paint/icon-glyph.md#图形限框: retain the independently sized receipt canvas.
			gm := t.Metrics().Glyph()
			iw, ih := w, h
			if svg == pinInfoIcon {
				iw, ih = gm.MessagePin().Width(), gm.MessagePin().Height()
			}
			if svg == replyInfoIcon {
				iw, ih = gm.MessageReply().Width(), gm.MessageReply().Height()
			}
			glyph := draw.Fit(c, svg, w, h, iw, ih, gm.SmallStroke())
			glyph.Element(c).Absolute().Left(left).Top(top).TextColor(color).Label(label)
		}
		if b.sending && o.Side == Incoming {
			color := t.Colors().Message().SendingInIconForeground()
			if o.Image {
				color = t.Colors().Message().SendingInvertedIconForeground()
			}
			icon(pendingIcon, deliveryLabel(info.Delivery), x+m.IncomingSendingLeft(), b.height-m.ReceiptTop(), m.SendingSize(), m.SendingSize(), color)
			x += m.ReceiptSpace()
		}
		count := func(value, label string, svg *ui.SVG) {
			icon(svg, label, x, b.height-m.ReceiptTop(), m.ViewsIconWidth(), m.ReceiptHeight(), date)
			w, _ := c.MeasureText(0, span(c, value, t.Typography().MessageTime(), date))
			text(c, value, t.Typography().MessageTime(), date).Absolute().Left(x+m.ViewsWidth()).Top(0).Size(w, b.height).SingleLine()
			x += m.ViewsWidth() + w + m.ViewsSpace()
		}
		if !b.sending {
			if info.Replies != "" && info.Replies != "0" && !info.RepliesContext && !info.Shortcut {
				count(info.Replies, "Replies", replyInfoIcon)
			}
			if info.Views != "" {
				count(info.Views, "Views", viewsInfoIcon)
			}
		}
		if info.Pinned {
			icon(pinInfoIcon, "Pinned", x, b.height-m.PinSize(), m.PinSize(), m.PinSize(), date)
			x += m.PinWidth()
		}
		if info.Ephemeral {
			icon(pendingIcon, "Ephemeral", x, b.height-m.EphemeralSize(), m.EphemeralSize(), m.EphemeralSize(), date)
			x += m.EphemeralWidth()
		}
		if info.Silent {
			icon(silentInfoIcon, "Silent", x, b.height-m.SilentTop(), m.SilentSize(), m.SilentSize(), date)
			x += m.SilentWidth()
		}
		if b.signatureWidth > 0 {
			text(c, info.Signature+", ", t.Typography().MessageTime(), date).Absolute().Left(x).Top(0).Size(b.signatureWidth, b.height).SingleLine().Label("Signature")
			x += b.signatureWidth
		}
		text(c, b.date, t.Typography().MessageTime(), date).Absolute().Left(x).Top(0).Size(b.dateWidth, b.height).SingleLine().Label("Date text")
		if o.Side == Outgoing {
			color := t.Colors().Message().OutIconForeground()
			if o.Selected {
				color = t.Colors().Message().OutIconForegroundSelected()
			}
			if o.Image {
				color = t.Colors().Message().IconForegroundInverted()
			}
			if b.sending {
				color := t.Colors().Message().SendingOutIconForeground()
				if o.Image {
					color = t.Colors().Message().SendingInvertedIconForeground()
				}
				icon(pendingIcon, deliveryLabel(info.Delivery), b.width-m.SendingRight(), b.height-m.SendingTop(), m.SendingSize(), m.SendingSize(), color)
			} else if info.Delivery == Sent || info.Delivery == Read {
				w := m.SentWidth()
				if info.Delivery == Read {
					w = m.ReadWidth()
				}
				icon(receiptIcon(info.Delivery), deliveryLabel(info.Delivery), b.width-m.ReceiptRight(), b.height-m.ReceiptTop(), w, m.ReceiptHeight(), color)
			}
		}
	})
	return root
}

// ImageInfo is inserted into a bottom photo/album's BottomInfo callback.
// paint/message-bottom.md#普通气泡与图上底板.
func ImageInfo(c *ui.Context, info Info, o BodyOptions) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Media()
	bg := t.Colors().Message().TimestampImgBackground()
	if o.Selected {
		bg = t.Colors().Message().TimestampImgBackgroundSelected()
	}
	_, normalHeight := c.MeasureText(0, span(c, "M", t.Typography().Message(), ui.Color{}))
	root := ui.Box(c).Absolute().Right(m.BadgeInset()).Bottom(m.BadgeInset()).Padding(m.BadgePaddingY(), m.BadgePaddingX()).Radius(float32(int((normalHeight + 2*m.BadgePaddingY()) / 2))).Background(bg).PassThrough()
	o.Image = true
	root.Children(func() { BottomInfo(c, info, o) })
	return root
}

func receiptIcon(d Delivery) *ui.SVG {
	switch d {
	case Sending, Failed:
		return pendingIcon
	case Sent:
		return sentIcon
	case Read:
		return readIcon
	}
	return nil
}
func deliveryLabel(d Delivery) string {
	switch d {
	case Sending:
		return "Sending"
	case Sent:
		return "Sent"
	case Read:
		return "Read"
	case Failed:
		return "Failed"
	}
	return ""
}
func lastParagraphRTL(value string, direction ui.Direction) bool {
	value = value[strings.LastIndexByte(value, '\n')+1:]
	for _, r := range value {
		properties, _ := bidi.LookupRune(r)
		switch properties.Class() {
		case bidi.L:
			return false
		case bidi.R, bidi.AL:
			return true
		}
	}
	return direction == ui.RightToLeft
}

// BodyState 由消息段和窗口持有；只为已进入视口的段创建，离屏后由宿主释放。
type BodyState struct {
	spans         []ui.Span
	display       []ui.Span
	tail          ui.Span
	copyRunes     int
	font          ui.Font
	shape         ui.TextShape
	layout        *ui.TextLayout
	natural       *ui.TextLayout
	value         string
	plain         bool
	linkLayout    *ui.TextLayout
	text          string
	units         []int
	width, height float32
	right         bool
	objects       []ui.TextObject
	handle        ui.Handle
	links         []BodyLink
	linkBoxes     []bodyLinkBox
	clickedLink   string
}

// Reset 释放段落保留的字形和字体；下一次显示时重新排版。
func (s *BodyState) Reset() { *s = BodyState{} }

// BodyTextOptions 的 Width 是宿主提供的正文可用宽度，单位 DIP。
type BodyTextOptions struct {
	Width      float32
	Font       ui.Font
	Direction  ui.Direction
	Color      ui.Color
	Label      string
	Selection  *BodySelection
	DrawObject func(*ui.Painter, int, ui.Rect)
	Links      []BodyLink
	LinkTip    func(ui.Element, string)
	Trailing   ui.Span
}

// BodyLink 的起止按 rune 计，与 CopyData 的 SelectedText 一致。
type BodyLink struct {
	Start, End int
	Label, URL string
}
type bodyLinkBox struct {
	link int
	rect ui.Rect
}

// ClickedLink 取走上一轮输入产生的链接动作，由宿主在当前构建中打开。
func (s *BodyState) ClickedLink() string { v := s.clickedLink; s.clickedLink = ""; return v }

func (s *BodyState) prepare(spans []ui.Span, o BodyTextOptions) {
	shape := ui.TextShape{Width: o.Width, Direction: o.Direction}
	sameContent := s.natural != nil && s.font == o.Font && s.shape.Direction == shape.Direction && s.tail == o.Trailing && slices.Equal(s.spans, spans)
	if sameContent && s.shape == shape {
		return
	}
	if !sameContent {
		var b strings.Builder
		for _, sp := range spans {
			b.WriteString(sp.Text)
		}
		if o.Selection != nil && !strings.HasPrefix(b.String(), s.text) {
			o.Selection.forget(s)
		}
		s.text = b.String()
		s.spans = append(s.spans[:0], spans...)
		s.font, s.tail = o.Font, o.Trailing
		s.right = o.Direction == ui.RightToLeft || lastParagraphRTL(s.text, o.Direction)
		s.display = append(s.display[:0], spans...)
		if o.Trailing.ObjectWidth > 0 {
			if lastParagraphRTL(s.text, o.Direction) != (o.Direction == ui.RightToLeft) {
				s.display = append(s.display, ui.Span{Text: "\n"})
				b.WriteByte('\n')
			}
			s.display = append(s.display, o.Trailing)
			b.WriteString(o.Trailing.Text)
		}
		s.value = b.String()
		s.plain = len(s.display) == 1 && s.display[0] == (ui.Span{Text: s.value}) && o.Direction == ui.LeftToRight
		if s.plain {
			s.natural = ui.ShapeText(s.value, o.Font, 0)
		} else {
			s.natural = ui.ShapeRichTextWith(s.display, o.Font, ui.TextShape{Direction: o.Direction})
		}
		s.units = append(s.units[:0], 0)
		for _, r := range s.text {
			n := 1
			if r > 0xFFFF {
				n = 2
			}
			s.units = append(s.units, s.units[len(s.units)-1]+n)
		}
		s.copyRunes = len(s.units) - 1
		for _, r := range s.value[len(s.text):] {
			n := 1
			if r > 0xFFFF {
				n = 2
			}
			s.units = append(s.units, s.units[len(s.units)-1]+n)
		}
	}
	s.shape = shape
	w, h := s.natural.Size()
	s.width = min(w, o.Width)
	s.layout = s.natural
	if w > o.Width {
		if s.plain {
			s.layout = ui.ShapeText(s.value, o.Font, o.Width)
		} else {
			s.layout = ui.ShapeRichTextWith(s.display, o.Font, shape)
		}
		_, h = s.layout.Size()
	}
	s.height = h
	s.objects = s.layout.Objects()
	for i := range s.objects {
		s.objects[i].Index, _ = slices.BinarySearch(s.units, s.objects[i].Index)
	}
}

// RetainedBody 将同一份排版用于字形、命中、选区和行内对象，不创建隐藏文字层。
func RetainedBody(c *ui.Context, s *BodyState, spans []ui.Span, o BodyTextOptions) ui.Element {
	s.prepare(spans, o)
	if !slices.Equal(s.links, o.Links) || s.linkLayout != s.layout {
		s.links = slices.Clone(o.Links)
		s.linkBoxes = s.linkBoxes[:0]
		s.linkLayout = s.layout
		for i, link := range s.links {
			for _, rect := range s.layout.SelectionRects(ui.TextInputRange{Start: s.units[link.Start], End: s.units[link.End]}) {
				s.linkBoxes = append(s.linkBoxes, bodyLinkBox{i, rect})
			}
		}
	}
	root := ui.Box(c).Width(s.width).MinWidth(0).MaxWidth(o.Width).Height(s.height).Role(ui.RoleNone)
	if o.Selection != nil {
		o.Selection.add(s, root.ID())
	}
	selectionColor := c.Theme().Selection
	root.Draw(func(p *ui.Painter, r ui.Rect) {
		x := r.X + s.offset(r.W)
		if o.Selection != nil {
			for _, v := range o.Selection.ranges(s) {
				for _, box := range s.layout.SelectionRects(v) {
					p.Fill(ui.Rect{X: x + box.X, Y: r.Y + box.Y, W: box.W, H: box.H}, selectionColor, 0)
				}
			}
		}
		p.TextLayout(s.layout, x, r.Y, o.Color)
		if o.DrawObject != nil {
			for _, obj := range s.objects {
				box := obj.Rect
				box.X += x
				box.Y += r.Y
				o.DrawObject(p, obj.Index, box)
			}
		}
	})
	root.Children(func() {
		// RoleText 是无障碍叶节点；正文与链接并列，不能把链接藏在文字叶节点下。
		text := ui.Box(c.Key("text")).Absolute().Fill().Bind(&s.handle).Role(ui.RoleText).Label(o.Label)
		if o.Selection != nil {
			text.Focusable().HandleInput(s.selectionInput(c, o.Selection, ui.Point{}))
		}
		for i, box := range s.linkBoxes {
			link := s.links[box.link]
			r := box.rect
			target := ui.Box(c.Key(i)).Absolute().Top(r.Y).Size(r.W, r.H).Focusable().Role(ui.RoleLink).Label(link.Label).Value(link.URL).Cursor(ui.CursorPointer)
			if s.right {
				target.Right(s.width - r.X - r.W)
			} else {
				target.Left(r.X)
			}
			if o.Selection != nil {
				input := s.selectionInput(c, o.Selection, ui.Point{X: r.X, Y: r.Y})
				target.HandleInput(func(ev ui.InputEvent) bool {
					if ev.Kind == ui.InputKeyDown && ev.Mods == 0 && (ev.Key == ui.KeyEnter || ev.Key == ui.KeySpace) {
						s.clickedLink = link.URL
						return true
					}
					return input(ev)
				})
			} else {
				target.OnClick(func() { s.clickedLink = link.URL })
			}
			if o.LinkTip != nil {
				o.LinkTip(target, link.URL)
			}
		}
	})
	return root
}

// 自绘文字的焦点节点自己接键盘输入；mygo 不把键盘事件冒泡给父 HandleInput。
func (s *BodyState) selectionInput(c *ui.Context, selection *BodySelection, offset ui.Point) func(ui.InputEvent) bool {
	return func(ev ui.InputEvent) bool {
		body, root := s.handle.Bounds(c), selection.handle.Bounds(c)
		ev.X += body.X + s.offset(body.W) + offset.X - root.X
		ev.Y += body.Y + offset.Y - root.Y
		return selection.input(c, ev)
	}
}

// BodySelection 保留一个消息壳的跨段选区。宿主每次构建 Attach 一次，然后顺序构建正文。
type BodySelection struct {
	parts                     []bodyPart
	anchor, caret             bodyPoint
	dragging                  bool
	pressPoint                ui.Point
	pressLink                 string
	pressBody                 *BodyState
	moved                     bool
	unit                      int
	unitStart, unitEnd        bodyPoint
	handle                    ui.Handle
	services                  ui.Services
	copyData                  func([]ui.SelectedText) transfer.Data
	paddingTop, paddingBottom float32
}

// BodySelectionOptions 指明壳的上下留白，避免把中间媒体误当成文字留白。
type BodySelectionOptions struct {
	CopyData                  func([]ui.SelectedText) transfer.Data
	PaddingTop, PaddingBottom float32
}
type bodyPart struct {
	state *BodyState
	id    uint64
}
type bodyPoint struct {
	state    *BodyState
	position ui.TextCaretPosition
}

func (s *BodySelection) add(state *BodyState, id uint64) {
	s.parts = append(s.parts, bodyPart{state, id})
}
func (s *BodySelection) forget(state *BodyState) {
	if s.anchor.state == state || s.caret.state == state {
		s.anchor, s.caret = bodyPoint{}, bodyPoint{}
		s.dragging = false
	}
}

// Finish 在本壳全部段落构建后清掉已移除端点，避免选区保留离开的消息。
func (s *BodySelection) Finish() {
	if s.index(s.anchor.state) < 0 || s.index(s.caret.state) < 0 {
		s.anchor, s.caret = bodyPoint{}, bodyPoint{}
		s.dragging = false
		s.pressBody, s.pressLink = nil, ""
	}
	if !s.dragging {
		s.unitStart, s.unitEnd = bodyPoint{}, bodyPoint{}
	}
}
func (s *BodySelection) index(state *BodyState) int {
	for i, p := range s.parts {
		if p.state == state {
			return i
		}
	}
	return -1
}
func (s *BodySelection) ordered() (bodyPoint, bodyPoint) {
	a, b := s.anchor, s.caret
	if s.index(a.state) > s.index(b.state) || (a.state == b.state && a.position.Index > b.position.Index) {
		a, b = b, a
	}
	return a, b
}
func (s *BodySelection) ranges(state *BodyState) []ui.TextInputRange {
	if s.anchor == s.caret {
		return nil
	}
	a, b := s.ordered()
	i, j, k := s.index(a.state), s.index(b.state), s.index(state)
	if i < 0 || j < 0 || k < i || k > j {
		return nil
	}
	if a.state == b.state {
		return state.layout.SelectionRanges(a.position, b.position)
	}
	start, end := 0, state.units[len(state.units)-1]
	if k == i {
		start = a.position.Index
	}
	if k == j {
		end = b.position.Index
	}
	return []ui.TextInputRange{{Start: start, End: end}}
}
func (s *BodySelection) SelectedTexts() []ui.SelectedText {
	var out []ui.SelectedText
	for _, p := range s.parts {
		for _, r := range s.ranges(p.state) {
			a, _ := slices.BinarySearch(p.state.units, r.Start)
			b, _ := slices.BinarySearch(p.state.units, r.End)
			a, b = min(a, p.state.copyRunes), min(b, p.state.copyRunes)
			if a == b {
				continue
			}
			out = append(out, ui.SelectedText{ID: p.id, Text: p.state.text, Start: a, End: b})
		}
	}
	return out
}
func (s *BodySelection) hit(c *ui.Context, x, y float32) bodyPoint {
	var nearest *BodyState
	best := float32(1e30)
	for _, p := range s.parts {
		r := p.state.handle.Bounds(c)
		dx := max(r.X-x, x-r.X-r.W, float32(0))
		dy := max(r.Y-y, y-r.Y-r.H, float32(0))
		d := dx*dx + dy*dy
		if nearest == nil || d < best {
			nearest, best = p.state, d
		}
	}
	if nearest == nil {
		return bodyPoint{}
	}
	r := nearest.handle.Bounds(c)
	pos := nearest.layout.PositionAt(ui.Point{X: x - r.X - nearest.offset(r.W), Y: y - r.Y})
	if y < r.Y {
		pos.Index = 0
	}
	if y >= r.Y+r.H {
		pos.Index = nearest.units[len(nearest.units)-1]
	}
	return bodyPoint{nearest, pos}
}
func (s *BodySelection) all() {
	if len(s.parts) == 0 {
		return
	}
	a, b := s.parts[0].state, s.parts[len(s.parts)-1].state
	s.anchor = bodyPoint{state: a}
	s.caret = bodyPoint{state: b, position: ui.TextCaretPosition{Index: b.units[b.copyRunes]}}
}
func (s *BodySelection) Copy() {
	parts := s.SelectedTexts()
	if len(parts) == 0 {
		return
	}
	if s.copyData != nil {
		s.services.WriteClipboardData(s.copyData(parts))
		return
	}
	var value strings.Builder
	for i, p := range parts {
		if i > 0 && parts[i-1].ID != p.ID {
			value.WriteString("\n\n")
		}
		value.WriteString(string([]rune(p.Text)[p.Start:p.End]))
	}
	s.services.WriteClipboard(value.String())
}
func (s *BodySelection) EditItems(m *ui.Menu) {
	if m.Item("Copy").Disabled(len(s.SelectedTexts()) == 0).Chosen() {
		s.Copy()
	}
	if m.Item("Select All").Chosen() {
		s.all()
	}
}
func (s *BodySelection) Attach(c *ui.Context, root ui.Element, o BodySelectionOptions) {
	clear(s.parts)
	s.parts = s.parts[:0]
	s.services = c.Services()
	s.copyData = o.CopyData
	s.paddingTop, s.paddingBottom = o.PaddingTop, o.PaddingBottom
	root.Bind(&s.handle).Cursor(ui.CursorText).HandleInput(func(ev ui.InputEvent) bool { return s.input(c, ev) })
}

func (s *BodySelection) input(c *ui.Context, ev ui.InputEvent) bool {
	r := s.handle.Bounds(c)
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		p := s.hit(c, r.X+ev.X, r.Y+ev.Y)
		// 混合消息里的按钮和媒体继续接收点击；壳上下留白可以起拖。
		if p.state == nil {
			return false
		}
		bounds := p.state.handle.Bounds(c)
		if y := r.Y + ev.Y; y < bounds.Y || y > bounds.Y+bounds.H {
			if y >= r.Y+s.paddingTop && y < r.Y+r.H-s.paddingBottom {
				return false
			}
		}
		if ev.Mods&ui.Shift == 0 {
			s.anchor = p
		}
		s.caret = p
		s.unit = min(ev.Clicks, 3)
		if s.unit > 1 {
			a, b := p.state.unitRange(p.position, s.unit)
			s.unitStart, s.unitEnd = bodyPoint{p.state, a}, bodyPoint{p.state, b}
			s.anchor, s.caret = s.unitStart, s.unitEnd
		}
		s.dragging = true
		s.pressPoint = ui.Point{X: ev.X, Y: ev.Y}
		s.moved = false
		s.pressBody = p.state
		s.pressLink = ""
		if p.state != nil {
			br := p.state.handle.Bounds(c)
			x, y := r.X+ev.X-br.X, r.Y+ev.Y-br.Y
			for _, box := range p.state.linkBoxes {
				b := box.rect
				if x >= b.X && x < b.X+b.W && y >= b.Y && y < b.Y+b.H {
					s.pressLink = p.state.links[box.link].URL
					break
				}
			}
		}
		if s.pressLink == "" {
			p.state.handle.Focus(c)
		}
		return true
	case ui.InputPointerMove:
		if !s.dragging {
			return false
		}
		dx, dy := ev.X-s.pressPoint.X, ev.Y-s.pressPoint.Y
		s.moved = s.moved || dx*dx+dy*dy > 16
		s.extend(s.hit(c, r.X+ev.X, r.Y+ev.Y))
		return true
	case ui.InputPointerUp:
		if ev.Button != 0 || !s.dragging {
			return false
		}
		s.extend(s.hit(c, r.X+ev.X, r.Y+ev.Y))
		s.dragging = false
		if !s.moved && s.pressLink != "" {
			s.pressBody.clickedLink = s.pressLink
		}
		s.pressBody = nil
		s.pressLink = ""
		return true
	case ui.InputCommand:
		switch ev.Text {
		case "copy":
			s.Copy()
			return true
		case "selectAll":
			s.all()
			return true
		}
	case ui.InputKeyDown:
		if ev.Mods&ui.Cmd != 0 {
			switch ev.Key {
			case ui.KeyC:
				s.Copy()
				return true
			case ui.KeyA:
				s.all()
				return true
			}
		}
		return s.key(ev)
	}
	return false
}

// unitRange 复用现有 Unicode 分词库；只有双击/三击时才分词或读取行表。
func (s *BodyState) unitRange(p ui.TextCaretPosition, unit int) (ui.TextCaretPosition, ui.TextCaretPosition) {
	if unit == 3 {
		r := s.layout.CaretAt(p)
		for _, line := range s.layout.Lines() {
			if r.Y >= line.Rect.Y && r.Y < line.Rect.Y+line.Rect.H {
				return ui.TextCaretPosition{Index: line.Start}, ui.TextCaretPosition{Index: min(line.End, s.units[s.copyRunes])}
			}
		}
	}
	index, _ := slices.BinarySearch(s.units, p.Index)
	var words segmenter.Segmenter
	words.Init([]rune(s.text))
	it := words.WordIterator()
	for it.Next() {
		word := it.Word()
		end := word.Offset + len(word.Text)
		if index >= word.Offset && index < end {
			return ui.TextCaretPosition{Index: s.units[word.Offset]}, ui.TextCaretPosition{Index: s.units[end]}
		}
	}
	end := s.layout.MoveCaret(p, 1)
	if end.Index < p.Index {
		p, end = end, p
	}
	end.Index = min(end.Index, s.units[s.copyRunes])
	return p, end
}

func (s *BodySelection) extend(p bodyPoint) {
	if s.unit <= 1 {
		s.caret = p
		return
	}
	a, b := p.state.unitRange(p.position, s.unit)
	if s.index(p.state) < s.index(s.unitStart.state) || p.state == s.unitStart.state && p.position.Index < s.unitStart.position.Index {
		s.anchor, s.caret = s.unitEnd, bodyPoint{p.state, a}
	} else {
		s.anchor, s.caret = s.unitStart, bodyPoint{p.state, b}
	}
}

func (s *BodySelection) key(ev ui.InputEvent) bool {
	p := s.caret
	if p.state == nil {
		return false
	}
	state := p.state
	switch ev.Key {
	case ui.KeyLeft, ui.KeyRight:
		delta := -1
		if ev.Key == ui.KeyRight {
			delta = 1
		}
		if ev.Mods&ui.Shift == 0 && s.anchor != s.caret {
			a, b := s.ordered()
			p = a
			if delta > 0 {
				p = b
			}
		} else {
			next := state.layout.MoveCaret(p.position, delta)
			next.Index = min(next.Index, state.units[state.copyRunes])
			if next == p.position {
				i := s.index(state) + delta
				if i >= 0 && i < len(s.parts) {
					p.state = s.parts[i].state
					next = ui.TextCaretPosition{}
					if delta < 0 {
						next.Index = p.state.units[p.state.copyRunes]
					}
				}
			}
			p.position = next
		}
	case ui.KeyHome, ui.KeyEnd:
		if ev.Mods&ui.Cmd != 0 {
			i := 0
			if ev.Key == ui.KeyEnd {
				i = len(s.parts) - 1
			}
			p = bodyPoint{state: s.parts[i].state}
			if ev.Key == ui.KeyEnd {
				p.position.Index = p.state.units[p.state.copyRunes]
			}
		} else {
			r := state.layout.CaretAt(p.position)
			x := float32(0)
			if ev.Key == ui.KeyEnd {
				x = state.width + 1
			}
			p.position = state.layout.PositionAt(ui.Point{X: x, Y: r.Y + r.H/2})
		}
	case ui.KeyUp, ui.KeyDown:
		r := state.layout.CaretAt(p.position)
		y := r.Y - r.H/2
		if ev.Key == ui.KeyDown {
			y = r.Y + r.H*1.5
		}
		i := s.index(state)
		if y < 0 && i > 0 {
			state = s.parts[i-1].state
			y = state.height - 1
		} else if y >= state.height && i+1 < len(s.parts) {
			state = s.parts[i+1].state
			y = 1
		}
		p.state = state
		p.position = state.layout.PositionAt(ui.Point{X: r.X, Y: y})
	default:
		return false
	}
	p.position.Index = min(p.position.Index, p.state.units[p.state.copyRunes])
	s.caret = p
	if ev.Mods&ui.Shift == 0 {
		s.anchor = p
	}
	return true
}

func (s *BodyState) offset(width float32) float32 {
	if s.right {
		return width - s.width
	}
	return 0
}
