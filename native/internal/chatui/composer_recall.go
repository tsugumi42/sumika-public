package chatui

import (
	"slices"
	"strings"
)

// 输入框的历史回忆（清单 2.4"↑ 回忆历史"，照 OBF 的做法：光标在开头按 ↑ 往前翻，翻之前把草稿存起来，
// 翻回最新时还给草稿）。历史只在内存里按会话存，来源是本次运行发出的话加上列表里已有的自己的消息。

const maxSent = 200 // 一个会话最多记多少条发出的话

// recall 是翻历史的状态：on 为真时正在翻，list 是开始翻时定下的快照（翻的过程中列表变了也不乱），
// at 是现在显示第几条（len(list) 表示还没翻到任何一条），shown 是它的原文，draft 是翻之前正在打的话。
type recall struct {
	on    bool
	list  []string
	at    int
	shown string
	draft string
}

// Remember 记下刚发出去的话，下一次 ↑ 就能找回。空话和只有空白的不记。
func (p *Composer) Remember(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	p.sent = append(p.sent, text)
	if len(p.sent) > maxSent {
		p.sent = slices.Clone(p.sent[len(p.sent)-maxSent:])
	}
	p.recall = recall{}
}

// history 是翻历史用的完整列表（旧到新）：列表里已有的加本次发出的；同一句话只留最新的一次，
// 这样刚发出的话等真消息出现在列表里以后不会翻到两遍。
func (p *Composer) history(h ComposerHost) []string {
	var all []string
	if h.Past != nil {
		all = append(all, h.Past()...)
	}
	all = append(all, p.sent...)
	seen := map[string]bool{}
	out := make([]string, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		t := strings.TrimSpace(all[i])
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	slices.Reverse(out)
	return out
}

// step 处理一次 ↑（up 为真）或 ↓。text 是现在的文字，sel 是光标（rune 偏移）。
// 返回要换成的文字、要不要换（changed）、键有没有被它收下（ok；没收下就让输入框照常移动光标）。
// 规则：
//   - 有选区不动；多行文字中间（光标不在第一行 / 最后一行）按 ↑↓ 仍是移动光标；
//   - 还没开始翻时，只有输入框为空或光标在最开头才开始翻，不劫持打到一半的草稿；
//   - 翻出来的文字被改过，就当成新的草稿，不再回到旧草稿，免得覆盖用户的改动。
func (r *recall) step(up bool, text string, sel [2]int, history func() []string) (string, bool, bool) {
	if r.on && text != r.shown {
		*r = recall{}
	}
	if sel[0] != sel[1] {
		return "", false, false
	}
	runes := []rune(text)
	caret := min(max(sel[0], 0), len(runes))
	if up {
		if !r.on {
			if caret != 0 {
				return "", false, false
			}
			list := history()
			if len(list) == 0 {
				return "", false, false
			}
			*r = recall{on: true, list: list, at: len(list), draft: text}
		} else if slices.Contains(runes[:caret], '\n') {
			return "", false, false
		}
		if r.at == 0 {
			return "", false, true // 已经是最早的一条：收下键，不动
		}
		r.at--
		r.shown = r.list[r.at]
		return r.shown, true, true
	}
	if !r.on || slices.Contains(runes[caret:], '\n') {
		return "", false, false
	}
	if r.at+1 >= len(r.list) { // 回到最新：还草稿
		draft := r.draft
		*r = recall{}
		return draft, true, true
	}
	r.at++
	r.shown = r.list[r.at]
	return r.shown, true, true
}

// end 是 Esc 退出回忆：还回草稿。没在翻时返回 false，Esc 照旧用。
func (r *recall) end() (string, bool) {
	if !r.on {
		return "", false
	}
	draft := r.draft
	*r = recall{}
	return draft, true
}
