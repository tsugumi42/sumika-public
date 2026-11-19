package chatui

import (
	"fmt"
	"math"
	"sync"

	"github.com/egoist/mygo/ui"
)

// chip 是一个表情回应按钮在表情区里的位置（相对表情区左上）。
type chip struct {
	X, Y, W float32
	R       Reaction
}

// layoutChips 按 Telegram 的做法排表情回应（history_view_reactions.cpp:328-418）：
// 内容宽取"正文最长一行"和"一排排开 + 时间占位块"里大的那个（不超过可用宽），按钮从左往右排满换行；
// 最后一个按钮右边放得下时间占位块，时间就在这一排，放不下就另起一行。
func (h Host) layoutChips(c *ui.Context, m *Msg, l *layout) {
	t := h.T
	l.Userpics = ShowUserpics(m.Reactions)
	widths := make([]float32, len(m.Reactions))
	oneRow := float32(0)
	for i, r := range m.Reactions {
		w := t.ChipInset + t.ChipEmoji
		if l.Userpics {
			// 头像一串：到按钮右、上、下的距离一样（(高 − 头像) / 2），和表情隔一个表情框的边距
			w += t.ChipInset + t.avatarsWidth(len(r.By)) + (t.ChipH-t.ChipAvatar)/2
		} else {
			nw, _ := c.MeasureText(1e6, ui.Span{Text: fmt.Sprint(r.Count), Size: t.FS[1], Weight: t.Weight[2], Font: h.Font, Features: Tabular})
			w += t.ChipGap + nw + t.ChipPadR
		}
		widths[i] = float32(math.Ceil(float64(w)))
		if i > 0 {
			oneRow += t.ChipBetween
		}
		oneRow += widths[i]
	}
	side := t.BubblePadH - t.BubblePadV
	avail := l.TextW + side*2 // 表情排从竖内边距开始，比正文多两边各 side
	// 时间挂在表情排：文字框和按钮竖直居中，右边距取数字到气泡底的距离，右、下仍然相等。
	// 表情排右沿离气泡右边是竖内边距，所以时间在排里要让出 (右边距 − 竖内边距) + 时间宽 + s2。
	chipBottom := t.BubblePadV + (t.ChipH-l.InfoH)/2
	chipRight := chipBottom + (l.InfoH - l.InfoBase)
	chipSkip := float32(0)
	if l.Info != "" {
		chipSkip = (chipRight - t.BubblePadV) + l.InfoW + t.S2
	}
	width := min(avail, max(l.Fit+side*2, oneRow+chipSkip))
	for _, w := range widths {
		width = max(width, min(avail, w))
	}
	x, y := float32(0), float32(0)
	for i, r := range m.Reactions {
		w := widths[i]
		if x > 0 && x+w > width {
			x, y = 0, y+t.ChipH+t.ChipBetween
		}
		l.Chips = append(l.Chips, chip{X: x, Y: y, W: w, R: r})
		x += w + t.ChipBetween
	}
	last := l.Chips[len(l.Chips)-1]
	l.ReactH = y + t.ChipH
	l.InfoInline = l.Info == "" || last.X+last.W+chipSkip <= width
	if l.InfoInline {
		l.InfoRight, l.InfoBottom = chipRight, chipBottom
	}
	l.ContentW = width - side*2 // 正文宽 = 表情排宽去掉两边的 side
}

func (t Tokens) avatarsWidth(n int) float32 {
	if n <= 0 {
		return 0
	}
	return t.ChipAvatar + float32(n-1)*(t.ChipAvatar-t.ChipAvShift)
}

// reactions 画表情区：别人的回应是淡底，主人点过的是角色色浅档加细边，都是胶囊。
func (h Host) reactions(c *ui.Context, l layout, acc Accent, self bool) ui.Element {
	t := h.T
	side := t.BubblePadH - t.BubblePadV
	mine := t.AccentOf(h.SelfChar)
	e := ui.Box(c.Key("reactions")).Label("表情回应").Size(l.ContentW+side*2, l.ReactH)
	e.Children(func() {
		for i, ch := range l.Chips {
			bg := t.C.Hover
			if self {
				bg = acc.SoftHover
			}
			b := ui.Row(c.Key(i)).Absolute().Left(ch.X).Top(ch.Y).Size(ch.W, t.ChipH).Radius(t.ChipH / 2).
				Padding(t.ChipPad).AlignItems(ui.Center).Label(ch.R.Emoji)
			if ch.R.Mine {
				// 我点过的：不加边框，整个按钮用强调色当底（主人 11-06）；是"我的"状态，所以用我自己的强调色，
				// 别人的消息上也一样，不用发送人的角色色（ADR-285 第 8 条）
				bg = mine.Base
			}
			b.Background(bg)
			ring := bg
			r := ch.R
			b.Children(func() {
				// 表情放在正方形框里，框到按钮上、下、左都是 ChipInset；表情画成图片填满框（照 Telegram）
				if r.Face != "" && h.Media != nil {
					face := h.Media.Face(r.Face)
					if face.State == MediaReady && face.Image != nil {
						ui.Image(c.Key("e"), face.Image).Size(t.ChipEmoji, t.ChipEmoji).Margin(t.ChipInset - t.ChipPad)
					} else {
						text := face.Description
						if text == "" {
							text = "[表情" + r.Face + "]"
						}
						h.Emoji(c, "e", text, t.ChipEmoji).Margin(t.ChipInset - t.ChipPad)
					}
				} else {
					text := r.Emoji
					if text == "" && r.Face != "" {
						text = "[表情" + r.Face + "]"
					}
					h.Emoji(c, "e", text, t.ChipEmoji).Margin(t.ChipInset - t.ChipPad)
				}
				if l.Userpics {
					h.userpics(c, r.By, ring, r.Mine)
					return
				}
				ink := t.C.Text2
				if r.Mine {
					ink = mine.On
				}
				ui.Text(c.Key("n"), fmt.Sprint(r.Count)).Font(h.Font).FontSize(t.FS[1]).FontWeight(t.Weight[2]).FontFeatures(Tabular).
					FixedLineHeight(t.ChipH).TextColor(ink).Margin(0, t.ChipPadR-t.ChipPad, 0, t.ChipGap-(t.ChipInset-t.ChipPad)).Translate(0, textNudge(t.FS[1]))
			})
		}
	})
	return e
}

// userpics 是表情后面叠在一起的头像（最多 3 个，Telegram GroupCallUserpics：20、叠 7、描边 1）；
// 描边用按钮底色，叠在一起也分得开。头像到按钮右边 = 到上下 = (高 − 头像) / 2。
func (h Host) userpics(c *ui.Context, by []Reactor, ring ui.Color, onAccent bool) {
	t := h.T
	av := t.ChipAvatar
	inset := (t.ChipH - av) / 2
	_ = inset // 头像到按钮右、上、下 = 按钮四边内边距 ChipPad
	ui.Box(c.Key("pics")).Size(t.avatarsWidth(len(by)), av).Margin(0, 0, 0, t.ChipInset-(t.ChipInset-t.ChipPad)).Shrink(0).Children(func() {
		for i, p := range by {
			acc := t.AccentOf(p.Char)
			if p.Char == (OKLCH{}) {
				acc = t.AccentOf(DefaultChar)
				acc.Text = t.NameColor(p.NameID)
			}
			initial := " "
			if rs := []rune(p.Name); len(rs) > 0 {
				initial = string(rs[0])
			}
			in := av - 2*t.ChipAvStroke
			ui.Box(c.Key(i)).Absolute().Left(float32(i)*(av-t.ChipAvShift)).Top(0).Size(av, av).Radius(av / 2).
				Background(ring).Center().Label(p.Name).Children(func() {
				fill, ink := acc.Line, acc.On
				if onAccent { // 强调色底上用浅档，不然同色相融成一片
					fill, ink = acc.Soft, acc.Text
				}
				ui.Box(c.Key("in")).Size(in, in).Radius(in / 2).Background(fill).Center().Children(func() {
					if p.Avatar != "" && h.ReactorImage != nil {
						if img := h.ReactorImage(p.Avatar); img != nil {
							ui.Image(c.Key("pic"), img).Size(in, in).Radius(in / 2).Fit(ui.Cover)
							return
						}
					}
					ui.Text(c, initial).Font(h.Font).FontSize(round1(in * ReactorInitial)).FontWeight(t.Weight[2]).TextColor(ink).SingleLine()
				})
			})
		}
	})
}

var (
	shapeMu    sync.Mutex
	shapeCache = map[shapeKey][]float32{}
)

type shapeKey struct {
	text, font  string
	size, lh, w float32
}

// shapeLines 返回文字在宽度 w 内排开后每一行的宽度（不含行尾空白）；结果按参数缓存，满 2048 条清空重来。
func shapeLines(text, font string, size, lh, w float32) []float32 {
	k := shapeKey{text, font, size, lh, w}
	shapeMu.Lock()
	defer shapeMu.Unlock()
	if v, ok := shapeCache[k]; ok {
		return v
	}
	layout := ui.ShapeText(text, ui.Font{Family: font, Size: size, LineHeight: lh}, w)
	var out []float32
	for _, ln := range layout.Lines() {
		out = append(out, ln.Rect.W)
	}
	if len(out) == 0 {
		out = []float32{0}
	}
	if len(shapeCache) >= 2048 {
		shapeCache = map[shapeKey][]float32{}
	}
	shapeCache[k] = out
	return out
}

var (
	baseMu    sync.Mutex
	baseCache = map[shapeKey]float32{}
)

// baseline 是一行文字排开后基线离文字框顶多远（缓存）。
func baseline(text, font string, size float32) float32 {
	k := shapeKey{text: text, font: font, size: size}
	baseMu.Lock()
	defer baseMu.Unlock()
	if v, ok := baseCache[k]; ok {
		return v
	}
	var b float32
	if lines := ui.ShapeText(text, ui.Font{Family: font, Size: size}, 1e6).Lines(); len(lines) > 0 {
		b = lines[0].Baseline - lines[0].Rect.Y
	}
	baseCache[k] = b
	return b
}
