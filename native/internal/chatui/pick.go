package chatui

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
)

// 多选（docs/规格-群聊引用和路由.md 3.3）：右键"多选"进入，每条消息旁边出现勾选圈，点消息任意处切换，Shift 点选一个范围，
// Esc 退出并清空；输入区换成操作条"已选 N 条｜复制｜引用 ▸｜取消"。最多 MaxPick 条，第 MaxPick+1 条点不上并提示。
// 只在同一个会话里，切会话就退出（宿主按 Conv 判断）。

// MaxPick 是多选最多几条：和一次发送最多的块数一致（turn.MaxInputParts，主人已确认 20），根包有测试对着它。
const MaxPick = 20

// Picker 是多选的状态，宿主持有一份（同一时刻只有一个会话在多选）。
type Picker struct {
	Conv   string   // 在哪个会话里多选；空表示没在多选
	IDs    []string // 已选的消息，按选的先后
	anchor string   // 上一次点的那条：Shift 点选从它到被点的那条
	Notice string   // 操作条上的一行提示（满了、跳过了几条）
}

// Active 报告是不是正在 conv 里多选。
func (p *Picker) Active(conv string) bool { return p != nil && p.Conv != "" && p.Conv == conv }

// Start 在 conv 里进入多选，id 先被选中。
func (p *Picker) Start(conv, id string) {
	*p = Picker{Conv: conv}
	p.Click(id, false, nil)
}

// Stop 退出多选并清空。
func (p *Picker) Stop() { *p = Picker{} }

// Has 报告 id 选上了没有。
func (p *Picker) Has(id string) bool { return slices.Contains(p.IDs, id) }

// Click 是点了一条消息：平时切换这一条；shift 为真时把上一次点的那条到这一条之间（按 order 的顺序，含两端）都选上。
// 满了的不再加，提示里写明上限。order 是会话里消息的先后（旧到新），Shift 时才用。
func (p *Picker) Click(id string, shift bool, order []string) {
	p.Notice = ""
	if shift && p.anchor != "" && order != nil {
		i, j := slices.Index(order, p.anchor), slices.Index(order, id)
		if i >= 0 && j >= 0 {
			if i > j {
				i, j = j, i
			}
			for _, x := range order[i : j+1] {
				if !p.add(x) {
					break
				}
			}
			p.anchor = id
			return
		}
	}
	p.anchor = id
	if k := slices.Index(p.IDs, id); k >= 0 {
		p.IDs = slices.Delete(p.IDs, k, k+1)
		return
	}
	p.add(id)
}

// add 选上一条；满了返回假并写提示。
func (p *Picker) add(id string) bool {
	if p.Has(id) {
		return true
	}
	if len(p.IDs) >= MaxPick {
		p.Notice = fmt.Sprintf("最多选 %d 条", MaxPick)
		return false
	}
	p.IDs = append(p.IDs, id)
	return true
}

// InOrder 是已选的消息按 order（会话里的先后）排好；order 里没有的（已经不在列表里）放最后，保持选的先后。
func (p *Picker) InOrder(order []string) []string {
	out := make([]string, 0, len(p.IDs))
	for _, id := range order {
		if p.Has(id) {
			out = append(out, id)
		}
	}
	for _, id := range p.IDs {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// pickSize 是勾选圈的直径。
func (t Tokens) pickSize() float32 { return round1(18 * t.TS) }

// pickGutter 是多选时消息行右边让出来放勾选圈的宽度。勾选圈放右边不放左边：群聊里别人的头像由
// StickyAvatars 浮层画在行的最左边、盖在行上面，放左边会被头像挡住（2026-11-10 截图核对）。
func (t Tokens) pickGutter() float32 { return t.pickSize() + t.S2*2 }

// pickOverlay 是多选时盖在整行上的点击层：点消息任意处切换这一条（正文不能选字、链接不能点，免得误操作），
// 右边画勾选圈。行高不变（只多了右边距），虚拟列表不跳。
func (h Host) pickOverlay(c *ui.Context, m *Msg) {
	t := h.T
	on := h.Pick.Has(m.ID)
	label := "选择这条消息"
	if on {
		label = "取消选择这条消息"
	}
	size := t.pickSize()
	b := ui.ButtonBase(c.Key("pick")).Label(label).Absolute().Left(0).Top(0).Right(0).Bottom(0).AlignItems(ui.Center).Justify(ui.End).Padding(0, t.S2).
		OnClick(func() {
			var order []string
			if h.PickOrder != nil {
				order = h.PickOrder()
			}
			h.Pick.Click(m.ID, c.Modifiers()&ui.Shift != 0, order)
		})
	fill := ui.Transparent
	if b.Hovered() {
		fill = t.C.Hover
	}
	b.Background(t.Fill(b, fill)).Radius(t.R1).Children(func() {
		ui.Row(c.Key("pick-row")).FillHeight().AlignItems(ui.Center).Children(func() {
			circle := ui.Box(c.Key("circle")).Label("勾选圈").Size(size, size).Radius(size / 2).Center()
			if on {
				acc := t.AccentOf(h.SelfChar)
				circle.Background(acc.Base).Children(func() {
					ui.Icon(c, Icon("check", false)).Size(size*0.7, size*0.7).TextColor(ui.RGB(255, 255, 255))
				})
			} else {
				circle.Border(1.5, t.C.Text3)
			}
		})
	})
}

// PickBar 是多选时代替输入区的操作条："已选 N 条｜复制｜引用 ▸｜取消"，下面一行是提示（满了、有几条没有文字会被跳过）。
// targets 是能指给的角色（一位时"引用"直接加卡）；skipped 是已选里不能引用的条数。
func (h ComposerHost) PickBar(c *ui.Context, pk *Picker, targets []WhisperTarget, skipped int, onCopy func(), onQuote func(ids []string, all bool), onCancel func()) {
	t := h.T
	pad := ComposerPad(t)
	box := ui.Column(c.Key("pick-bar")).Label("多选操作").FillWidth().Shrink(0).Padding(pad).Gap(t.S1).Radius(ComposerRadius(t)).Border(1, h.C.Line)
	if h.Surface != nil {
		h.Surface(box)
	} else {
		box.Background(h.C.Fill)
	}
	n := len(pk.IDs)
	quotable := n - skipped
	box.Children(func() {
		ui.Row(c.Key("row")).FillWidth().Gap(t.S1).AlignItems(ui.Center).Children(func() {
			ui.Text(c.Key("count"), fmt.Sprintf("已选 %d 条", n)).Font(h.Font).FontSize(t.Body).TextColor(h.C.Text).SingleLine().Padding(0, t.S2)
			ui.Spacer(c)
			btn := func(key, label, icon string) ui.Element {
				b := ui.ButtonBase(c.Key(key)).Label(label).Height(t.HSm).Padding(0, t.S2).Radius(t.HSm / 2).Gap(t.S1).AlignItems(ui.Center).TextColor(h.C.Text)
				b.Background(t.Fill(b, h.pressed(b))).Children(func() {
					if icon != "" {
						ui.Icon(c, Icon(icon, false)).Size(t.FS[2], t.FS[2])
					}
					ui.Text(c, label).Font(h.Font).FontSize(t.FS[2]).SingleLine()
				})
				return b
			}
			btn("copy", "复制", "copy").Disabled(n == 0).OnClick(onCopy)
			q := btn("quote", "引用", "quote").Disabled(quotable <= 0 || len(targets) == 0)
			switch {
			case len(targets) == 1:
				q.OnClick(func() { onQuote([]string{targets[0].ID}, false) })
			case len(targets) > 1:
				q.PopoverMenuButton(func(m *ui.Menu) {
					for _, x := range targets {
						if m.Item(targetLabel(WhisperTarget{ID: x.ID, Name: x.Name})).ID("to:" + x.ID).Chosen() {
							onQuote([]string{x.ID}, false)
						}
					}
					m.Separator()
					if m.Item(WhisperAll).ID("to:*").Chosen() {
						onQuote(nil, true)
					}
				}, t.MenuButton(c))
			}
			btn("cancel", "取消", "").OnClick(onCancel)
		})
		note := pk.Notice
		if note == "" && skipped > 0 {
			note = fmt.Sprintf("其中 %d 条没有文字或已撤回，引用时跳过", skipped)
		}
		if note != "" {
			ui.Text(c.Key("notice"), note).Font(h.Font).FontSize(t.FS[1]).TextColor(h.C.Muted).Wrap().FillWidth().Padding(0, t.S2)
		}
	})
}
