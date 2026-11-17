package app

// 醒来行后面跟的“最近主动说过什么”（规格-主动联系 第四节“醒来行”）：让她自己避开重复。
// 数据只来自 proactive_sends 里真发给主人本地私聊的行，以及发过话的线收尾时的结论；
// 这段字跟醒来行一起存成线里的 system 事件，只出现在线的会话里，追加在线的历史末尾，不动固定前缀。

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/proactive"
)

// 醒来时带回最近说过的话的上限。依据：醒来行本身约 60 字，每次唤醒都要进提示词；
// 一条发送摘要最长 40 字（BeginSend 已截），结论最长 200 字、这里截 60；
// 总共不超过 wakeRecallRunes 个字，超了先丢最旧的话，再丢最旧的结论。
// 4 条话能盖住一周里每天一次的节奏，2 条结论盖住最近两件事的走向。
const (
	wakeRecallSends       = 4
	wakeRecallConclusions = 2
	wakeRecallConclRunes  = 60
	wakeRecallRunes       = 220
)

type recallItem struct {
	at   time.Time
	text string
}

// recallClean 把一段文字压成一行，去掉会破坏醒来行结构的括号和引号
func recallClean(s string, limit int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case strings.ContainsRune("「」（）()【】[]", r):
			continue
		case unicode.IsSpace(r) || unicode.IsControl(r):
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return proactive.ClipNote(b.String(), limit)
}

// recallAgo 是“多久以前”，两天以内按小时，之后按天
func recallAgo(at, now time.Time) string {
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d/time.Hour))
	}
	return fmt.Sprintf("%d 天前", int(d/(24*time.Hour)))
}

// wakeRecallLine 拼“最近说过什么”一段；没有记录返回空串（调用方不加换行、不留占位）。sends、concl 都是新的在前
func wakeRecallLine(sends []proactive.Send, concl []proactive.Thread, now time.Time) string {
	var said, done []recallItem
	for _, s := range sends {
		if t := recallClean(s.Summary, 40); t != "" {
			said = append(said, recallItem{s.At, t})
		}
	}
	for _, c := range concl {
		if t := recallClean(c.Conclusion, wakeRecallConclRunes); t != "" {
			done = append(done, recallItem{c.ClosedAt, t})
		}
	}
	if len(said) > wakeRecallSends {
		said = said[:wakeRecallSends]
	}
	if len(done) > wakeRecallConclusions {
		done = done[:wakeRecallConclusions]
	}
	total := func() (n int) {
		for _, it := range said {
			n += utf8.RuneCountInString(it.text)
		}
		for _, it := range done {
			n += utf8.RuneCountInString(it.text)
		}
		return
	}
	for total() > wakeRecallRunes && len(said)+len(done) > 1 {
		if len(said) > 0 {
			said = said[:len(said)-1]
		} else {
			done = done[:len(done)-1]
		}
	}
	if len(said)+len(done) == 0 {
		return ""
	}
	var parts []string
	if len(said) > 0 {
		var xs []string
		for _, it := range said {
			xs = append(xs, recallAgo(it.at, now)+"「"+it.text+"」")
		}
		parts = append(parts, "你之前主动对他说过："+strings.Join(xs, "；"))
	}
	if len(done) > 0 {
		var xs []string
		for _, it := range done {
			xs = append(xs, recallAgo(it.at, now)+"收的线："+it.text)
		}
		parts = append(parts, strings.Join(xs, "；"))
	}
	return "（导演：" + strings.Join(parts, "。") + "。别重复说同一件事。）"
}

// recallFor 读这个角色最近发给主人本地私聊的话和收了的线，拼成醒来行后面的一段；读失败只记日志、按没有记录处理（不为这个拦住醒来）
func (h *proactiveHost) recallFor(character string, now time.Time) string {
	sends, err := h.mgr.RecentSentTo(character, localConvOf(character), wakeRecallSends)
	if err != nil {
		h.a.logf("主动联系：读最近说过的话失败：%v", err)
		return ""
	}
	concl, err := h.mgr.RecentConcluded(character, wakeRecallConclusions)
	if err != nil {
		h.a.logf("主动联系：读最近收的线失败：%v", err)
		return ""
	}
	return wakeRecallLine(sends, concl, now)
}
