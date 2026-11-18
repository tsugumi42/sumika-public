package chatui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// 选区工具条（docs/规格-新界面主线.md "输入区和引用"）：在消息正文里选中文字、松开鼠标，选区旁边浮出一个小条，
// 有"回复"和"复制"。借鉴：Claude 桌面版（选中后浮出"回复"）、opencode（松手并稳定 150 毫秒才弹；Esc 收起后同一段选区不再弹；
// 按住 Alt 点"回复"只加卡、焦点不动，方便连着引好几段）。

// Selected 是用户在消息里选中的一段文字。
type Selected struct {
	MessageID string
	Text      string // 选中的文字，行内表情的占位换回表情名
	// Start、End 是这段文字在整条消息原文里的字符偏移；只有这条消息就是一整段、选区文字对得上时才有，
	// 其余都是 0，让后台按文字在原文里找。
	Start, End int
}

// selectionSettle 是选区稳定多久才弹工具条：拖选过程中、双击三击连着选时不闪。
const selectionSettle = 150 * time.Millisecond

// SelectionBar 是选区工具条的状态，窗口里所有消息共用一份（同一时刻只有一处选区），由宿主持有、通过 Host.Selection 传进来。
type SelectionBar struct {
	cur       Selected
	scope     string // 选区所在的块（消息 id 加块序号）
	held      bool   // 鼠标还按着在选
	since     time.Time
	open      bool   // Popover 的开关；点外面或 Esc 时 mygo 把它改回 false
	shown     bool   // 这一段选区已经弹过
	dismissed string // 被收起的选区文字，同样的选区不再弹

	// 选区的包围矩形，条的位置只看它，不看鼠标在哪松手（从右往左拖时松手点在选区最左端，条会压住字）。
	hs        map[string]*ui.Handle // 每个块、每条消息行的框（上一帧，窗口坐标），按 key 存
	rect      ui.Rect               // 相对块左上角
	origin    ui.Rect               // 选区所在的块在窗口里的框
	dwx, dwy  float32               // 按下的位置（窗口坐标）
	uwx, uwy  float32               // 松手的位置（窗口坐标）；松手在正文外也收得到（由消息行观察）
	upSet     bool
	moved     bool // 按下后到松手之间鼠标动过，是拖选而不是双击三击
	rectExact bool // rect 来自文字排版（准确）；否则按鼠标位置往外放宽（Markdown 拿不到每段的几何）
}

// handle 是 key 对应的元素框；只存引用，元素重建后下一帧会重新绑定。
func (s *SelectionBar) handle(key string) *ui.Handle {
	if s.hs == nil || len(s.hs) > 512 {
		s.hs = map[string]*ui.Handle{}
	}
	hd := s.hs[key]
	if hd == nil {
		hd = new(ui.Handle)
		s.hs[key] = hd
	}
	return hd
}

// Current 是工具条现在对应的选区（没有选区时 Text 为空）。
func (s *SelectionBar) Current() Selected { return s.cur }

// Visible 报告工具条现在是不是开着。
func (s *SelectionBar) Visible() bool { return s != nil && s.open }

func (s *SelectionBar) clear() {
	s.cur, s.scope, s.open, s.shown, s.dismissed = Selected{}, "", false, false, ""
}

// watchSelection 读一个可选容器的选区：文字变了就重新计时，松手并稳定后弹工具条。
// plain 是这个块的纯文字，用来判断偏移能不能直接用。
//
// shape 在块是一整段富文本时给出和画出来一致的排版（用它换算选区的行矩形）；Markdown 块没有，传 nil。
func (h Host) watchSelection(c *ui.Context, scope ui.Element, m *Msg, index int, faces []inlineFace, plain string, shape func(width float32) *ui.TextLayout) {
	s := h.Selection
	key := fmt.Sprintf("%s#%d", m.ID, index)
	parts := scope.SelectedTexts()
	text := selectedString(parts, faces)
	hd := s.handle(key)
	scope.Bind(hd)
	scope.ObserveInput(func(ev ui.InputEvent) {
		switch ev.Kind {
		case ui.InputPointerDown:
			if ev.Button == 0 {
				b := hd.Bounds(c)
				s.held, s.upSet, s.moved = true, false, false
				s.dwx, s.dwy = b.X+ev.X, b.Y+ev.Y
			}
		case ui.InputPointerUp:
			s.held = false
			b := hd.Bounds(c)
			s.recordUp(b.X+ev.X, b.Y+ev.Y)
		}
	})
	switch {
	case text == "":
		if s.scope == key {
			s.clear()
		}
	case s.scope != key || s.cur.Text != text:
		s.scope, s.open, s.shown, s.dismissed = key, false, false, ""
		s.since = c.Now()
		s.cur = Selected{MessageID: m.ID, Text: text}
		if len(parts) == 1 && parts[0].Text == m.Text && len(faces) == 0 && plain == m.Text {
			s.cur.Start, s.cur.End = parts[0].Start, parts[0].End
		}
	}
	if s.scope == key && text != "" {
		s.origin = hd.Bounds(c)
		s.rect, s.rectExact = h.selectionRect(parts, text, shape, s.origin.W, s)
	}
	if s.scope != key || s.held || text == "" || s.dismissed == text {
		return
	}
	if wait := selectionSettle - c.Now().Sub(s.since); wait > 0 {
		c.After(wait)
		return
	}
	if !s.shown {
		s.open, s.shown = true, true
	}
}

// selectedString 把选区各段拼成文字：段与段之间换行，行内表情的占位（U+FFFC）换回表情名，和复制出来的一样。
func selectedString(parts []ui.SelectedText, faces []inlineFace) string {
	var out strings.Builder
	for i, part := range parts {
		rs := []rune(part.Text)
		lo, hi := max(0, part.Start), min(len(rs), part.End)
		if lo >= hi {
			continue
		}
		if i > 0 && out.Len() > 0 {
			out.WriteByte('\n')
		}
		for n := lo; n < hi; n++ {
			if rs[n] != '￼' {
				out.WriteRune(rs[n])
				continue
			}
			name := "[表情]"
			for _, f := range faces {
				if f.at == n {
					name = f.copy
					break
				}
			}
			out.WriteString(name)
		}
	}
	return strings.TrimSpace(out.String())
}

// selectionRect 是选区的包围矩形（相对块左上角）。
// 块是一整段富文本时用排版给的行矩形，准确；Markdown 块拿不到每一段的几何（mygo 只给每段的 ID 和字符区间，
// 没有按 ID 取框的接口），用按下、松手两点往外放宽一行（鼠标一定在某一行里，行高不超过 1.4 个正文行高），
// 双击三击只有一个点时再按选中文字折成几行往上下放宽。放宽只会让条离字更远，不会压住字。
func (h Host) selectionRect(parts []ui.SelectedText, text string, shape func(float32) *ui.TextLayout, width float32, s *SelectionBar) (ui.Rect, bool) {
	if shape != nil && width > 0 && len(parts) > 0 {
		layout := shape(width)
		var out ui.Rect
		found := false
		for _, part := range parts {
			for _, r := range layout.SelectionRects(ui.TextInputRange{Start: part.Start, End: part.End}) {
				if !found {
					out, found = r, true
					continue
				}
				x0, y0 := min(out.X, r.X), min(out.Y, r.Y)
				x1, y1 := max(out.X+out.W, r.X+r.W), max(out.Y+out.H, r.Y+r.H)
				out = ui.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
			}
		}
		if found {
			return out, true
		}
	}
	lh := h.T.BodyLine * 1.4
	ax, ay := s.dwx-s.origin.X, s.dwy-s.origin.Y // 按下、松手的位置换成相对块
	bx, by := ax, ay
	if s.upSet {
		bx, by = s.uwx-s.origin.X, s.uwy-s.origin.Y
	}
	x0, x1 := min(ax, bx), max(ax, bx)
	y0, y1 := min(ay, by), max(ay, by)
	top, bottom := y0-lh, y1+lh
	if !s.moved {
		// 没拖动：双击、三击选中一个词或一整段，位置只知道落在哪个点，按折行数往上下放宽
		n := 1
		tw := float32(0)
		if width > 0 {
			ws := shapeLines(text, h.Font, h.T.Body, 1.6, width)
			n = len(ws)
			for _, w := range ws {
				tw = max(tw, w)
			}
		}
		top, bottom = top-float32(n-1)*lh, bottom+float32(n-1)*lh
		x0, x1 = x0-tw, x1+tw
	}
	if y1-y0 > lh/2 || (!s.moved && width > 0) {
		x0, x1 = 0, max(width, x1) // 跨行：每行的起止都不知道，取整行宽
	}
	if width > 0 {
		x0, x1 = max(0, x0), min(width, x1)
	}
	return ui.Rect{X: x0, Y: top, W: max(1, x1-x0), H: bottom - top}, false
}

// recordUp 记下松手的位置（窗口坐标）。
func (s *SelectionBar) recordUp(x, y float32) {
	s.uwx, s.uwy, s.upSet = x, y, true
	s.moved = s.moved || abs32(x-s.dwx) > 4 || abs32(y-s.dwy) > 4
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// selectionTool 画工具条。位置只由选区的包围矩形定（不看鼠标在哪松手）：默认在选区第一行上方、水平对准选区中心；
// 上方放不下（贴近列表顶部）就放到最后一行下方；水平方向夹在聊天列的可见范围内。
func (h Host) selectionTool(c *ui.Context, key string) {
	s := h.Selection
	t := h.T
	if s.scope != key {
		return
	}
	text := s.cur.Text
	// 条的大小：照下面按钮的写法量一遍，用来决定放上面还是下面、左右夹到哪
	names := []string{"复制"}
	switch {
	case h.whisperOn():
		names = append(names, "引用") // 悄悄话会话：复制、引用 ▸（规格-群聊引用和路由 3.2）
	case h.OnReply != nil:
		names = append([]string{"回复"}, names...)
	}
	barW, barH := t.S1*2+2+t.S1*float32(len(names)-1), t.HSm+t.S1*2+2
	for _, n := range names {
		w, _ := c.MeasureText(1e6, ui.Span{Text: n, Font: h.Font, Size: t.FS[2]})
		barW += t.S2*2 + t.FS[2] + t.S1 + w
	}
	r, o := s.rect, s.origin
	view := ui.Rect{X: -1e6, Y: -1e6, W: 2e6, H: 2e6}
	if h.ViewRect != nil {
		if v := h.ViewRect(); v.W > 0 && v.H > 0 {
			view = v
		}
	}
	above := o.Y+r.Y-barH-t.S2 >= view.Y
	cx := o.X + r.X + r.W/2
	cx = max(view.X+barW/2+t.S1, min(cx, view.X+view.W-barW/2-t.S1))
	anchor := ui.Box(c.Key("selection-anchor")).Absolute().Left(cx-o.X).Top(r.Y).Size(1, max(1, r.H))
	panel := ui.PopoverBase(c, anchor, &s.open, func(panel ui.Element) {
		// 条和选区之间留 S2：在上方时留在条的下面，在下方时留在条的上面（AttachTo 的外边距照条所在的一侧算）
		if above {
			panel.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom).Margin(0, 0, t.S2, 0)
		} else {
			panel.AttachTo(anchor, ui.AnchorBottom, ui.AnchorTop).Margin(t.S2, 0, 0, 0)
		}
		panel.Label("选区操作").Padding(t.S1).Radius(t.R1).Background(t.C.Float).Border(1, t.C.FloatRing).
			Shadow(0, 0.5, 1, 0, t.C.ShadowA).Shadow(0, 1, 3, 0, t.C.ShadowB)
		dir := float32(-1)
		if above {
			dir = 1
		}
		panel.Transition(t.TipTransition(dir)) // 第一次出现淡入加 2 像素，消失更快；选区变动时位置跟手，不被缓动拖着走
		inner := t.Inner(t.R1, t.S1)
		ui.Row(c.Key("selection-tools")).Gap(t.S1).Children(func() {
			button := func(name, icon string, act func(alt bool)) ui.Element {
				b := ui.ButtonBase(c.Key(name)).Label(name).Height(t.HSm).Padding(0, t.S2).Radius(inner).Gap(t.S1).AlignItems(ui.Center)
				if act != nil {
					b.OnClick(func() { act(c.Modifiers()&ui.Alt != 0) })
				}
				fill := ui.Transparent
				if b.Pressed() {
					fill = t.C.Press
				} else if b.Hovered() {
					fill = t.C.Hover
				}
				b.Background(t.Fill(b, fill)).TextColor(t.C.Text).Children(func() {
					ui.Icon(c, Icon(icon, false)).Size(t.FS[2], t.FS[2])
					ui.Text(c, name).Font(h.Font).FontSize(t.FS[2]).SingleLine()
				})
				return b
			}
			done := func() { s.open, s.dismissed, s.shown = false, text, false }
			if h.whisperOn() {
				button("复制", "copy", func(bool) {
					c.Services().WriteClipboardData(transfer.TextData(text))
					done()
				})
				sel := s.cur
				if one, ok := h.singleTarget(sel.MessageID); ok {
					// 只有一位能被唤起：直接加卡，没有子菜单（规格 3.4）
					button("引用", "quote", func(alt bool) {
						done()
						h.OnWhisper(sel, []string{one.ID}, false, !alt)
					})
					return
				}
				targets := orderTargets(h.WhisperTargets(sel.MessageID))
				q := button("引用", "quote", nil).Disabled(len(targets) == 0)
				q.PopoverMenuButton(func(m *ui.Menu) { h.targetItems(m, sel, targets, true, done) }, h.T.MenuButton(c))
				return
			}
			if h.OnReply != nil {
				button("回复", "quote", func(alt bool) {
					sel := s.cur
					s.open, s.dismissed, s.shown = false, text, false
					h.OnReply(sel, !alt)
				})
			}
			button("复制", "copy", func(bool) {
				c.Services().WriteClipboardData(transfer.TextData(text))
				done()
			})
		})
	})
	if panel.Valid() {
		return
	}
	if s.shown && !s.open && s.dismissed != text {
		s.dismissed, s.shown = text, false // 点外面或 Esc 收起：同一段选区不再弹，直到选区变了
	}
}

// mdSelectable 画一块 markdown 正文并接上选区条。markdown 自己的根是 Selectable，选区归它持有（ShareSelection 关着）；
// 工具条的锚点是绝对定位的子元素，不能塞进 markdown 的根里（它的 Children 由 mygo 管），所以外面包一层同原点的列，
// 外边距也放在这一层，让根和包层的左上角重合，松手位置（相对根）直接当锚点用。
// 选中的文字是渲染后的文字（不含 Markdown 记号），偏移留 0，让后台按文字在原文里找（见规格 9.4）。
func (h Host) mdSelectable(c *ui.Context, key string, m *Msg, index int, side float32, build func(*ui.Context) ui.Element) ui.Element {
	if h.Selection == nil {
		md := build(c.Key(key))
		md.Margin(0, side)
		return md
	}
	var md ui.Element
	// 选区归外面这层列持有（Options.ShareSelection）：markdown 自己的根会给自己挂一个系统右键菜单（英文的
	// Copy / Select All 加"复制为 Markdown"），挂在同一个根上换不掉，所以把"可选"和菜单都放到外层。
	wrap := ui.Column(c.Key(key+"-sel")).MinWidth(0).Margin(0, side).Selectable() // 带外边距，宽度交给列的拉伸；FillWidth 不扣外边距，会越出父元素
	wrap.Children(func() {
		md = build(c.Key(key))
		h.watchSelection(c, wrap, m, index, nil, "", nil)
		h.selectionTool(c, fmt.Sprintf("%s#%d", m.ID, index))
	})
	h.textMenu(c, wrap, fmt.Sprintf("%s#%d", m.ID, index), m, nil)
	return md
}
