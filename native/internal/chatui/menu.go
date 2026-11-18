package chatui

import (
	"regexp"
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// 消息右键菜单：换掉 mygo 可选文字自带的系统菜单（英文的 Copy / Select All），用窗口里画的中文菜单。
// 对照 Claude 桌面版（右键消息有"附加为上下文"，选中文字时是"附加选中内容"）和 Telegram（复制文字、回复）：
//   - 右键时有选中的文字：复制（只复制选中的）、回复这一段（和选区条的"回复"同一个动作）；
//   - 没有选中：复制（整条消息的文字）、回复（引用整条）；
//   - 图片上：查看图片（和点图一样，旧界面只有这一个动作；复制图片、另存为旧界面没有，这里不硬做）。
//
// 没有"选择文字"：mygo 没有程序化全选的接口，只有系统菜单自己的"全选"，没法换成中文，所以不放。
// "回复"只在宿主给了 OnReply（本地私聊）时出现，其余会话根本没有回复这个功能，只有"复制"：不放常年灰着的项。
// 能回复的会话里，没有文字可引用（纯图片等）时"回复"变灰、右边写"没有文字"，不是整项消失。
// 选中了文字时"复制"右边标 Ctrl+C（mygo 的可选文字自带这个键，只复制选区）；复制整条没有对应的键，不标。
// 群聊的引用改为"引用 ▸ 指定角色 / 所有人"，由另一条线实现。

// fullText 是整条消息的文字：有原文用原文；分段消息把文字块拼起来，行内表情用表情名。
func fullText(m *Msg) string {
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	var parts []string
	for _, b := range m.Blocks {
		if s := b.Plain(); strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	for _, p := range m.Parts {
		if strings.TrimSpace(p.Text) != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// textMenu 给一块可选文字接上右键菜单。scope 是可选的容器；key 是这块在选区状态里的名字（消息 id 加块序号）。
func (h Host) textMenu(c *ui.Context, scope ui.Element, key string, m *Msg, faces []inlineFace) {
	s := h.Selection
	services := c.Services()
	defer h.withMenuTheme(c)()
	// 能取键盘焦点：Tab 走到这块正文，Shift+F10 / 菜单键打开和右键一样的菜单
	scope.Focusable().PopoverMenu(func(menu *ui.Menu) {
		text := selectedString(scope.SelectedTexts(), faces)
		sel := Selected{MessageID: m.ID, Text: text}
		if s != nil && s.scope == key && s.cur.Text == text {
			sel = s.cur // 偏移对得上的话一并带上
		}
		if h.whisperOn() {
			h.whisperTextMenu(menu, m, sel, text, func(s string) { services.WriteClipboardData(transfer.TextData(s)) })
			return
		}
		copyLabel, replyLabel := "复制", "回复"
		selected := text != "" // 有选中的字：Ctrl+C（mygo 的可选文字自带）复制的就是它，菜单里才标；复制整条没有对应的键
		if text == "" {
			sel = Selected{MessageID: m.ID, Text: fullText(m)}
			text = sel.Text
		} else {
			replyLabel = "回复这一段"
		}
		copyItem := menu.Item(copyLabel)
		if selected {
			copyItem.Shortcut(ui.Cmd, ui.KeyC)
		}
		if copyItem.Chosen() && text != "" {
			services.WriteClipboardData(transfer.TextData(text))
		}
		if link := firstLink(text); link != "" {
			if menu.Item("复制链接").Chosen() {
				services.WriteClipboardData(transfer.TextData(link))
			}
			if h.OnLink != nil && menu.Item("打开链接").Chosen() {
				h.OnLink(link)
			}
		}
		h.replyItem(menu, replyLabel, sel)
	})
}

// replyItem 加“回复”：这个会话能回复（宿主给了 OnReply）才有这一项；没有文字可引用时变灰并写原因，不藏起来。
func (h Host) replyItem(menu *ui.Menu, label string, sel Selected) {
	if h.OnReply == nil {
		return
	}
	empty := strings.TrimSpace(sel.Text) == ""
	item := menu.Item(label).Disabled(empty)
	if empty {
		item.Trailing("没有文字")
	}
	if item.Chosen() {
		h.OnReply(sel, true)
	}
}

var linkInText = regexp.MustCompile(`https?://[^\s<>"'，。；、）】》]+`)

// firstLink 是这段字里第一个链接；没有返回空串。
func firstLink(text string) string { return linkInText.FindString(text) }

// withMenuTheme 换上菜单主题，返回换回去的函数。
func (h Host) withMenuTheme(c *ui.Context) func() {
	if h.MenuTheme == nil {
		return func() {}
	}
	prev := c.Theme()
	c.SetTheme(h.MenuTheme)
	return func() { c.SetTheme(prev) }
}

// imageMenu 给一张图接上右键菜单。
func (h Host) imageMenu(c *ui.Context, el ui.Element, m *Msg, position int) {
	defer h.withMenuTheme(c)()
	el.PopoverMenu(func(menu *ui.Menu) {
		if h.OnViewImage != nil && menu.Item("查看图片").Chosen() {
			h.OnViewImage(m.ID, position)
		}
		if h.whisperOn() {
			// 图上：多选、引用（图没有文字：消息另有文字时引整条的文字，否则灰掉）
			if h.OnMultiSelect != nil && menu.Item("多选").Chosen() {
				h.OnMultiSelect(m.ID)
			}
			h.whisperItem(menu, Selected{MessageID: m.ID, Text: fullText(m)}, "")
			return
		}
		h.replyItem(menu, "回复", Selected{MessageID: m.ID, Text: fullText(m)})
	})
}
