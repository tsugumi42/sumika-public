package main

// 按条显示一条消息（规格-原生界面第二版 2.2、2.3、场合表）：
//   1. 展示 DTO → msg（data.go 的 msgFromView）：谁发的、在哪一边、段分成哪几块；要不要显示名字看 showName。纯函数，测试里直接调。
//   2. 段 → 块（segview.BlocksOf）：连着的文字、@、小黄脸合成一个行内块，连着的图合成一组，其余各成一块；按段的原序排，
//      不像原型那样先画完所有图再画文字。
//   3. 消息壳统一交 chat.Row/Bubble，引用、Markdown 和媒体按原序填入内容槽。
// 小黄脸：行内块里的 face 段是行内框（inline.go、faces.go）；名字表没到、图没有时退成文字（[表情<id>]）。图和网格是第六步。

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/chat"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/theme"
	"sumika-native/internal/segview"
)

// ───────────── 事件 → msg ─────────────

// showName 一组的第一条上面要不要写名字。私聊不写；群里别人（左边）的都写；右边的只有角色，且群里有两个以上角色说过话时才写
// （右边只有一个她，写名字是多余的，网页同款：web/src/Outside.tsx 的 named={several}）
func showName(group, mine, isChar, several, first bool) bool {
	if !group || !first {
		return false
	}
	if !mine {
		return true
	}
	return isChar && several
}

// sameRun 同一个人连着说的合成一组（5 分钟内）。按发送人的 id 比，不按显示名比：两个人可能同名，也可能都没有名字
func sameRun(a, b *msg) bool {
	return a.actor == b.actor && a.mine == b.mine && b.at-a.at < 5*60_000
}

// notesOf 气泡末尾的灰色小字（规格 2.3）：被打断；已撤回（撤回通知指向它，或者自检路径写的 unsent 含"已撤回"）；
// 没发出去。partial（发送时结果未知）时不写"没发出去"：结果未知不是明确失败，首批也不另写
// proactiveNote 是主动联系消息气泡时间旁的小标记（规格-主动联系 第十节第 1 条）
const proactiveNote = "主动联系"

func notesOf(m *msg, recalled bool) []string {
	var out []string
	if m.proactive != nil {
		out = append(out, proactiveNote)
	}
	if m.aborted {
		out = append(out, "被打断")
	}
	switch {
	case recalled || strings.Contains(m.unsent, "已撤回"):
		out = append(out, "已撤回")
	case m.unsent != "" && !m.partial:
		out = append(out, "没发出去")
	}
	return out
}

// ───────────── 画 ─────────────

// docOf 只读转换时生成的块；没有段的消息也在进入界面前识别链接。
func docOf(m *msg) []segview.Block {
	return m.blocks
}

// onlyImages 全是图：不套壳，图本身就是气泡（规格 2.2）
func onlyImages(bs []segview.Block) bool {
	for _, b := range bs {
		if b.Kind != segview.Images {
			return false
		}
	}
	return len(bs) > 0
}

// footnote 气泡末尾的灰色小字，和 chat.Bubble 的时间同一个字号和透明度
func footnote(c *ui.Context, text string, mine bool) {
	e := ui.Text(c, text).FontSize(c.Theme().Rem(0.75)).Opacity(0.7)
	if mine {
		e.AlignSelf(ui.End)
	}
}

// messageRows 同时供聊天历史和转发节点使用；part 保留节点内虚拟分块身份。
func (a *app) messageRows(c *ui.Context, m *msg, bs []segview.Block, first, last, group, selected bool, at time.Time, quoted bool, part int) ui.Element {
	groups := shellGroups(bs)
	notes := notesOf(m, a.recalled[m.id] || m.recalled)
	if onlyImages(bs) && len(notes) == 0 && (!quoted || m.quote == nil) {
		return a.mediaRows(c, m, bs, first, last, group, selected, at)
	}
	if len(groups) == 0 && (m.quote != nil || len(notes) > 0) {
		groups = [][]segview.Block{nil}
	}
	root := ui.Column(c).FillWidth().MinWidth(0)
	root.Children(func() {
		for gi, blocks := range groups {
			c.Key(part + gi)
			final := gi == len(groups)-1
			message := chat.Message{Info: chat.Info{Label: at.Format("15:04")}}
			if !final {
				message.Info = chat.Info{}
			}
			if m.who != "" && first && gi == 0 && (showName(group, m.mine, m.isChar, len(a.speakers) > 1, true) || group && m.owner) {
				message.SenderName = m.who
			}
			if m.owner {
				message.SenderNameColor = c.Theme().Accent
			}
			if !m.mine {
				avatars := a.avs()
				avatars.want(m.avatar)
				message.Avatar, message.AvatarInitials, message.AvatarLabel = avatars.peek(m.avatar), initialOf(m.who), avatarLabel(m.who)
				message.AvatarColor = nameColor(c, m.who)
			}
			side := chat.Incoming
			if m.mine {
				side = chat.Outgoing
			}
			cp := &copyMap{}
			o := chat.RowOptions{ReserveAvatar: !m.mine, BubbleOptions: chat.BubbleOptions{Side: side, Selected: selected, Group: chat.Group{Previous: !first || gi > 0, Next: !last || !final}}}
			o.AvatarGroupTop = a.avatarGroupTop(c, first && gi == 0)
			if final && len(m.reacts) > 0 {
				o.Footer = func(tail ui.Span) { a.reactionFooter(c, m, tail, false) }
			}
			hasQuote := quoted && gi == 0 && m.quote != nil
			o.Body = func(tail ui.Span) {
				metrics := theme.T(c).Metrics().Message()
				top := metrics.PaddingTop()
				if hasQuote {
					top = 0
				}
				// 选区涵盖壳内边距，保留从文字外侧起拖；名字与时间仍在选区之外。
				selectable := ui.Column(c).MinWidth(0).MaxWidth(a.envFor(c).bodyW+metrics.PaddingLeft()+metrics.PaddingRight()).Margin(-top, -metrics.PaddingRight(), -metrics.PaddingBottom(), -metrics.PaddingLeft()).Padding(top, metrics.PaddingRight(), metrics.PaddingBottom(), metrics.PaddingLeft())
				var shell *bodyShell
				if m.md {
					selectable.Selectable().CopyData(cp.restore)
				} else {
					shell = a.bodyShell(m, part+gi)
					shell.color = theme.T(c).Colors().Message().In().Text()
					if selected {
						shell.color = theme.T(c).Colors().Message().In().TextSelected()
					}
					if m.mine {
						shell.color = theme.T(c).Colors().Message().Out().Text()
						if selected {
							shell.color = theme.T(c).Colors().Message().Out().TextSelected()
						}
					}
					shell.selection.Attach(c, selectable, chat.BodySelectionOptions{CopyData: cp.restore, PaddingTop: top, PaddingBottom: metrics.PaddingBottom()})
				}
				selectable.Children(func() {
					body := ui.Column(c).MinWidth(0).MaxWidth(a.envFor(c).bodyW).Gap(theme.T(c).Metrics().Preview().MediaGap())
					a.probeOf(fmt.Sprintf("body:%s:%d", m.id, part+gi), body)
					body.Children(func() {
						for bi, b := range blocks {
							c.Key(bi)
							trailing := ui.Span{}
							if bi == len(blocks)-1 && (!final || len(notes) == 0) {
								trailing = tail
								if b.Kind != segview.Inline {
									trailing = ui.Span{}
								}
							}
							a.messageBlock(c, [2]int{part + gi, bi}, m, b, cp, trailing, shell)
						}
						if final {
							for i, n := range notes {
								if shell == nil {
									footnote(c, n, m.mine)
									continue
								}
								c.Key(len(blocks) + i)
								text := a.retainedInline(c, shell, len(blocks)+i, segview.Block{Runs: []segview.Run{{Text: n}}}, cp, m.mine, ui.Span{}, true).Opacity(0.7)
								if m.mine {
									text.AlignSelf(ui.End)
								}
							}
						}
						if len(blocks) == 0 || blocks[len(blocks)-1].Kind != segview.Inline || (final && len(notes) > 0) {
							ui.RichText(c, tail)
						}
					})
				})
				var edit func(*ui.Menu)
				if shell != nil {
					shell.selection.Finish()
					edit = shell.selection.EditItems
				}
				a.linkMenu(c, selectable, m.links, edit)
			}
			var leading func()
			if hasQuote {
				leading = func() { a.quoteBlock(c, m) }
			}
			row := chat.Row(c, message, o, leading).FillWidth()
			a.probeOf(fmt.Sprintf("shell:%s:%d", m.id, part+gi), row)
		}
	})
	return root
}

// 裸媒体本身就是组件；空 Row 仅负责同一套组尾头像，透明叠在自然测量的媒体行上。
func (a *app) mediaRows(c *ui.Context, m *msg, bs []segview.Block, first, last, group, selected bool, at time.Time) ui.Element {
	t := theme.T(c)
	metrics := t.Metrics().Message()
	top := metrics.MarginTop()
	if !first {
		top = metrics.AttachedTop()
	}
	left, right := metrics.MarginLeft()+metrics.AvatarSkip(), metrics.MarginRight()-metrics.AvatarSkip()
	if m.mine {
		left, right = metrics.MarginRight(), metrics.MarginLeft()
	}
	root := ui.Box(c).FillWidth().MinWidth(0)
	root.Children(func() {
		column := ui.Column(c).MinWidth(0).MaxWidth(t.Metrics().Media().MaxSize()).Margin(top, right, metrics.MarginBottom(), left)
		if m.mine {
			column.AlignSelf(ui.End)
		}
		column.Children(func() {
			if m.who != "" && (showName(group, m.mine, m.isChar, len(a.speakers) > 1, first) || group && m.owner && first) {
				color := t.Colors().Message().In().ServiceForeground()
				if m.owner {
					color = c.Theme().Accent
				}
				ui.Text(c, m.who).Font(t.Typography().Font()).FontSize(t.Typography().MessageName().Size() * c.Preferences().TextScale).FontWeight(t.Typography().MessageName().Weight()).TextColor(color).SingleLine()
			}
			media := ui.Box(c).MinWidth(0)
			media.Children(func() {
				for i, b := range bs {
					c.Key(i)
					a.imageGroup(c, m, b.Imgs, true)
				}
			})
			if len(m.reacts) > 0 {
				a.reactionFooter(c, m, ui.Span{}, true)
			}
		})
		if !m.mine {
			avatars := a.avs()
			avatars.want(m.avatar)
			chat.Row(c, chat.Message{Avatar: avatars.peek(m.avatar), AvatarInitials: initialOf(m.who), AvatarLabel: avatarLabel(m.who), AvatarColor: nameColor(c, m.who)}, chat.RowOptions{ReserveAvatar: true, AvatarGroupTop: a.avatarGroupTop(c, first), BubbleOptions: chat.BubbleOptions{Group: chat.Group{Previous: !first, Next: !last}}}, nil).Absolute().Fill().PassThrough()
		}
	})
	return root
}

func (a *app) messageBlock(c *ui.Context, key [2]int, m *msg, b segview.Block, cp *copyMap, tail ui.Span, shell *bodyShell) {
	switch b.Kind {
	case segview.Inline:
		if m.md {
			if m.markdown == nil {
				m.markdown = map[[2]int]*content.MarkdownState{}
			}
			state := m.markdown[key]
			if state == nil {
				state = &content.MarkdownState{}
				m.markdown[key] = state
			}
			content.Markdown(c, plainNamed(b, a.fs().table), state, content.MarkdownOptions{Appearance: content.Appearance{Outgoing: m.mine}, Trailing: tail, OnLink: func(raw string) { a.openLink(c, raw) }, ExpandLabel: "展开引用", CollapseLabel: "收起引用", RevealLabel: "显示内容", CopyLabel: "复制代码"})
		} else {
			a.retainedInline(c, shell, key[1], b, cp, m.mine, tail, false)
		}
	case segview.Images:
		a.imageGroup(c, m, b.Imgs, onlyImages(m.blocks))
	case segview.CardBlock:
		a.cardBlock(c, m.segs[b.Card.Position], m.mine)
	case segview.ForwardBlock:
		a.forwardCard(c, m, m.segs[b.Card.Position])
	case segview.Video:
		s := m.segs[b.Video.Position]
		s.position = b.Video.Position
		a.videoBubble(c, m, s, b.VideoIndex)
	case segview.File:
		s := m.segs[b.Video.Position]
		s.position = b.Video.Position
		a.fileBubble(c, m, s, b.VideoIndex)
	default:
		if shell == nil {
			ui.Text(c, b.Text).Wrap().Opacity(0.7)
		} else {
			a.retainedInline(c, shell, key[1], segview.Block{Runs: []segview.Run{{Text: b.Text}}}, cp, m.mine, ui.Span{}, false).Opacity(0.7)
		}
	}
}

// shellGroups 把块分给几个壳：商城表情占位、卡片、转发各占一个，其余相邻块合并。
func shellGroups(bs []segview.Block) [][]segview.Block {
	var out [][]segview.Block
	var cur []segview.Block
	for _, b := range bs {
		if b.Kind == segview.StickerNote || b.Kind == segview.CardBlock || b.Kind == segview.ForwardBlock {
			if cur != nil {
				out = append(out, cur)
				cur = nil
			}
			out = append(out, []segview.Block{b})
			continue
		}
		cur = append(cur, b)
	}
	if cur != nil {
		out = append(out, cur)
	}
	return out
}
