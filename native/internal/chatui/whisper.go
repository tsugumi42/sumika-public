package chatui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 悄悄话的入口（docs/规格-群聊引用和路由.md 第三、四节）：本地群聊、QQ 私聊、QQ 群里，引用 = 主人对被指角色说的悄悄话。
// 整条消息右键是"复制、多选、引用 ▸"，选中文字（选区条和右键）是"复制、引用 ▸"；"引用 ▸"的子菜单列出这个会话里能被唤起的角色，
// 最后是"所有人"。只有一位能被唤起时没有子菜单，直接是"引用"。本地私聊不走这里（引用是带引用的回复，见 OnReply）。

// WhisperTarget 是"引用 ▸"子菜单里的一位：角色 ID、显示名、是不是说被引那句话的人（排第一并标出来）。
type WhisperTarget struct {
	ID, Name string
	Spoke    bool
}

// WhisperAll 是"所有人"的显示名。
const WhisperAll = "所有人"

// whisperOn 报告这个会话的引用是不是悄悄话。
func (h Host) whisperOn() bool { return h.WhisperTargets != nil && h.OnWhisper != nil }

// orderTargets 把说这句话的人排第一，其余保持宿主给的顺序。
func orderTargets(ts []WhisperTarget) []WhisperTarget {
	out := slices.Clone(ts)
	slices.SortStableFunc(out, func(a, b WhisperTarget) int {
		switch {
		case a.Spoke == b.Spoke:
			return 0
		case a.Spoke:
			return -1
		}
		return 1
	})
	return out
}

// targetLabel 是子菜单里一位的名字；说这句话的那位后面标"（说这句话的）"。
func targetLabel(t WhisperTarget) string {
	name := t.Name
	if name == "" {
		name = t.ID
	}
	if t.Spoke {
		name += "（说这句话的）"
	}
	return name
}

// whisperItem 往菜单里加"引用"：能被唤起的有两位以上时是子菜单（各位 + 分隔线 + 所有人），只有一位时是直接加卡的一项，
// 不能引用（没有文字、没有能指的角色）时灰掉并在右边写原因。why 是调用方已知的不能引用的原因。
func (h Host) whisperItem(menu *ui.Menu, sel Selected, why string) {
	if why == "" && strings.TrimSpace(sel.Text) == "" {
		why = "没有文字"
	}
	var targets []WhisperTarget
	if why == "" {
		targets = orderTargets(h.WhisperTargets(sel.MessageID))
		if len(targets) == 0 {
			why = "没有能指给的角色"
		}
	}
	if why != "" {
		menu.Item("引用").Disabled(true).Trailing(why)
		return
	}
	if len(targets) == 1 {
		if menu.Item("引用").Tooltip("交给 " + targetLabel(targets[0])).Chosen() {
			h.OnWhisper(sel, []string{targets[0].ID}, false, true)
		}
		return
	}
	menu.Submenu("引用", func(sub *ui.Menu) { h.targetItems(sub, sel, targets, true, nil) })
}

// targetItems 是对象列表：各位（说这句话的排第一）、分隔线、所有人。选中后调 OnWhisper，再调 after（可空）。
func (h Host) targetItems(menu *ui.Menu, sel Selected, targets []WhisperTarget, keepFocus bool, after func()) {
	for _, t := range targets {
		if menu.Item(targetLabel(t)).ID("to:" + t.ID).Chosen() {
			h.OnWhisper(sel, []string{t.ID}, false, keepFocus)
			if after != nil {
				after()
			}
		}
	}
	menu.Separator()
	if menu.Item(WhisperAll).ID("to:*").Chosen() {
		h.OnWhisper(sel, nil, true, keepFocus)
		if after != nil {
			after()
		}
	}
}

// whisperTextMenu 是悄悄话会话里文字的右键菜单。有选中文字：复制、引用 ▸（只有这两项，选区只在一条消息里，没有多选）；
// 没有选中：复制、多选、引用 ▸（引整条），整条里有链接时后面再加复制链接、打开链接。
func (h Host) whisperTextMenu(menu *ui.Menu, m *Msg, sel Selected, text string, copyText func(string)) {
	selected := text != ""
	if !selected {
		sel = Selected{MessageID: m.ID, Text: fullText(m)}
		text = sel.Text
	}
	if menu.Item("复制").Disabled(strings.TrimSpace(text) == "").Chosen() {
		copyText(text)
	}
	if selected {
		h.whisperItem(menu, sel, "")
		return
	}
	if h.OnMultiSelect != nil && menu.Item("多选").Chosen() {
		h.OnMultiSelect(m.ID)
	}
	why := ""
	if m.State == Recalled {
		why = "已撤回"
	}
	h.whisperItem(menu, sel, why)
	if link := firstLink(text); link != "" {
		menu.Separator()
		if menu.Item("复制链接").Chosen() {
			copyText(link)
		}
		if h.OnLink != nil && menu.Item("打开链接").Chosen() {
			h.OnLink(link)
		}
	}
}

// singleTarget 是只有一位能被唤起时的她（选区条的"引用"直接加卡用）；不是一位时 ok 为假。
func (h Host) singleTarget(messageID string) (WhisperTarget, bool) {
	ts := h.WhisperTargets(messageID)
	if len(ts) != 1 {
		return WhisperTarget{}, false
	}
	return ts[0], true
}

// targetNames 是对象标签上的字："所有人"，或几位的名字用顿号连起来；名字取不到用 ID。
func targetNames(b *Block, targets []WhisperTarget) string {
	if b.All {
		return WhisperAll
	}
	names := make([]string, 0, len(b.To))
	for _, id := range b.To {
		name := id
		for _, t := range targets {
			if t.ID == id && t.Name != "" {
				name = t.Name
			}
		}
		names = append(names, name)
	}
	return strings.Join(names, "、")
}

// targetTag 是悄悄话卡头上的对象标签"→ 小A、小B"：点开是勾选列表（各位加所有人），一次勾或取消一位；
// 一位都没有时标签变红写"→ 选对象"，发送前的检查也会拦下。
func (h ComposerHost) targetTag(c *ui.Context, p *Composer, b *Block, n int) {
	t := h.T
	text, ink := "→ "+targetNames(b, h.Targets), h.C.Accent
	if NoTarget(b) {
		text, ink = "→ 选对象", h.C.Danger
	}
	if len(h.Targets) == 1 && !NoTarget(b) {
		// 只有一位能被唤起（QQ 私聊、只有一位在观察的 QQ 群）：对象固定，标签只显示不能改
		ui.Text(c.Key("to"), text).Font(h.Font).FontSize(t.FS[1]).FontWeight(t.Weight[1]).TextColor(ink).Shrink(1).MinWidth(0).SingleLine().Padding(0, t.S2)
		return
	}
	tag := ui.ButtonBase(c.Key("to")).Label(fmt.Sprintf("引用 %d 的对象：%s", n, strings.TrimPrefix(text, "→ "))).Height(t.HSm).Shrink(1).MinWidth(0).
		Padding(0, t.S2).Radius(t.HSm / 2).AlignItems(ui.Center).Disabled(b.Sending)
	tag.Background(t.Fill(tag, h.pressed(tag))).Children(func() {
		ui.Text(c, text).Font(h.Font).FontSize(t.FS[1]).FontWeight(t.Weight[1]).TextColor(ink).SingleLine()
	})
	// 勾一位不收起：选完把 Open 重新置真，下一帧原地再开；点外面、Esc、Tab 走开才收起（mygo 的菜单项选中即关，没有"留着"的开关）
	tag.PopoverMenuButton(func(m *ui.Menu) {
		for _, x := range h.Targets {
			on := slices.Contains(b.To, x.ID)
			name := x.Name
			if name == "" {
				name = x.ID
			}
			if m.Item(name).ID("to:" + x.ID).Checked(on).Chosen() {
				p.SetTarget(b, x.ID, !on)
				b.tagReopen = true
			}
		}
		if len(h.Targets) > 1 {
			m.Separator()
			if m.Item(WhisperAll).ID("to:*").Checked(b.All).Chosen() {
				p.SetTarget(b, "", !b.All)
				b.tagReopen = true
			}
		}
	}, ui.PopoverMenuButtonOptions{Open: &b.tagOpen})
	if b.tagReopen {
		b.tagReopen, b.tagOpen = false, true
		c.Invalidate()
	}
}
