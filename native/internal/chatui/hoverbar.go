package chatui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// 消息悬停操作条（清单 2.2：Hermes desktop 的助手消息底部一排图标，悬停才显、默认透明且不可点、但始终占位）。
//
// 位置：放在气泡朝聊天中间的那一侧（自己的消息在气泡左边，别人的在右边），和气泡底边对齐，不放在气泡下方。
// 原因：虚拟列表的行高不能因为悬停变化，也不能给每一行多留一截高度；放在旁边只占横向的位置，
// 行高完全由气泡决定。按钮比单行气泡矮，所以不会把单行消息撑高。
//
// 内容：复制（整条消息的原文，分块消息按顺序拼）、回复（宿主给了 OnReply 才有，和选区条、右键菜单同一个入口）、
// 本轮用量（宿主给了 Usage、OnUsage 且这条有数据才有；只在本地私聊、本地群聊）。
// 已撤回的消息没有操作条；还在出字的消息也不摆，免得复制到半截。
// 复制后图标变对勾一小会儿：时间用帧里的 c.After 排，不起协程。

// copiedFor 是复制后对勾停留多久。
const copiedFor = 1500 * time.Millisecond

// actionSize 是操作条按钮的边长：不小于失败角标的点击格，也不小于一行字高，但永远比单行气泡矮（气泡 = 一行字高 + 上下内边距）。
func (t Tokens) actionSize() float32 { return max(t.BadgeHit, t.BodyLine) }

// copyMark 记最近一次点复制的时刻；放在这一行的局部状态里，滚出屏幕就丢，对勾本来也只显一会儿。
type copyMark struct{ at time.Time }

// hasActions 说明这条消息要不要操作条（要不要占位）。
func (h Host) hasActions(m *Msg) bool {
	if m.Service != "" || len(m.Steps) > 0 || m.State == Recalled || m.State == Streaming {
		return false
	}
	return strings.TrimSpace(copyText(m)) != ""
}

// actionCount 是这条消息的操作条有几个钮：复制，加上能回复的回复，加上有这一轮用量的用量；没有操作条是 0。
func (h Host) actionCount(m *Msg) float32 {
	if !h.hasActions(m) {
		return 0
	}
	n := float32(1)
	if h.OnReply != nil {
		n++
	}
	if h.Usage != nil && h.OnUsage != nil && h.Usage(m.ID) != "" {
		n++
	}
	return n
}

// copyText 是"复制"拿到的原文：分块消息按块的顺序拼（文字块、引用块里被引的话不算），其余同 fullText。
func copyText(m *Msg) string {
	if len(m.Parts) > 0 {
		var parts []string
		for _, p := range m.Parts {
			if strings.TrimSpace(p.Text) != "" {
				parts = append(parts, p.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return fullText(m)
}

// actions 画操作条；row 是这一条消息的整行（悬停到行上的任何地方都显）。
// 不该有操作条的消息什么也不画，其余消息不管显不显都占同样的宽。
func (h Host) actions(c *ui.Context, row ui.Element, m *Msg) {
	if !h.hasActions(m) {
		return
	}
	t := h.T
	size := t.actionSize()
	shown := row.Hovered()
	mark := ui.Local(row, "copied", func() copyMark { return copyMark{} })
	copied := false
	if !mark.at.IsZero() {
		if rest := copiedFor - c.Now().Sub(mark.at); rest > 0 {
			copied = true
			shown = true // 对勾显着的时候操作条不能收，免得反馈还没看到就没了
			c.After(rest)
		}
	}
	to := float32(0)
	if shown {
		to = 1
	}
	text := copyText(m)
	canReply := h.OnReply != nil
	usage := ""
	if h.Usage != nil && h.OnUsage != nil {
		usage = h.Usage(m.ID)
	}
	n := h.actionCount(m)
	bar := ui.Row(c.Key("actions")).Label("消息操作").Shrink(0).Gap(t.S1).AlignItems(ui.Center).Width(size*n + t.S1*(n-1))
	bar.Opacity(bar.AnimateTo("shown", to, ui.Animation{Duration: t.Fast, NonMotion: true}))
	bar.Children(func() {
		label, icon := "复制消息", "copy"
		if copied {
			label, icon = "已复制", "check"
		}
		h.actionButton(c, "copy", label, icon, shown, func() {
			if shown {
				c.Services().WriteClipboardData(transfer.TextData(text))
				mark.at = c.Now()
			}
		})
		if canReply {
			h.actionButton(c, "reply", "回复消息", "quote", shown, func() {
				if shown {
					h.OnReply(Selected{MessageID: m.ID, Text: fullText(m)}, true)
				}
			})
		}
		if usage != "" {
			// 悬停提示写这一轮的输入输出；点一下去右栏看这一轮的每次请求
			h.actionButton(c, "usage", usage, "chart", shown, func() {
				if shown {
					h.OnUsage(m.ID)
				}
			})
		}
	})
}

// actionButton 是操作条上的一个图标钮：和外壳的 IconButton 同一族（圆形，悬停叠 hover、按下叠 press，有读屏名和悬停提示）。
// 没显出来时禁用：透明的按钮不能被点到，也不抢指针。
func (h Host) actionButton(c *ui.Context, key, label, icon string, enabled bool, onClick func()) {
	t := h.T
	size := t.actionSize()
	b := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(label).Size(size, size).Center().Radius(size / 2).TextColor(t.C.Text2).Disabled(!enabled).DimDisabled(false).OnClick(onClick)
	bg := ui.Transparent
	if b.Pressed() {
		bg = t.C.Press
	} else if b.Hovered() {
		bg = t.C.Hover
	}
	b.Background(b.AnimateColor("fill", bg, ui.Animation{Duration: t.Fast, NonMotion: true}))
	b.Children(func() {
		ui.Icon(c, Icon(icon, false)).Size(round1(size*0.64), round1(size*0.64))
	})
}
