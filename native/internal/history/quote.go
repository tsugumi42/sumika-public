package history

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 本地私聊里主人的一条消息由有序的"部分"组成（规格-引用）：
// 引用回复块 = 被引用的出处和片段 + 针对这一段的回复文字 + 自己的附件；普通文字块 = 不属于任何引用的话（也可带附件）。
// 存档在主人那条事件的 segments_json.local_parts（"parts"这个键已被 QQ 出站计划占用）；给模型的文字只由这份存档生成，
// 所以实例重建后和发送当时逐字一样。
const (
	QuoteMaxRunes      = 1000 // 片段上限（字符数）；超过截断并标明
	QuoteFallbackRunes = 200  // 选中的文字在原文里找不到时，退回原文开头这么多字
)

// Quote 是一个引用回复块的出处。
type Quote struct {
	ID        string `json:"id"`                  // 被引用事件的 id（同会话）
	Name      string `json:"name"`                // 引用当时被引用者的显示名
	At        int64  `json:"at"`                  // 被引用消息的时间（Unix 毫秒）
	Clock     string `json:"clock"`               // 引用当时按本机时区排好的 HH:MM，给模型的行直接用，重建时不随时区变
	Text      string `json:"text"`                // 片段：整条引用时是原文，可能被截断
	Start     int    `json:"start"`               // 片段在原文里的起点（字符偏移，含）；整条或退回开头时为 0
	End       int    `json:"end"`                 // 终点（不含）；整条或退回开头时为 0，表示没有定位
	Whole     bool   `json:"whole,omitempty"`     // 引用整条
	Fallback  bool   `json:"fallback,omitempty"`  // 选中的文字不在原文里，已退回原文开头
	Truncated bool   `json:"truncated,omitempty"` // 片段超长被截断
	Owner     bool   `json:"owner,omitempty"`     // 被引用的是主人自己说的话（按事件的 ActorKind=owner 认，不按名字）；只有悄悄话变体用
}

// Attachment 是块上的附件。现在只有数据结构：图片给模型时先按占位写（见 Said），真图以后接在 attachmentLine。
type Attachment struct {
	Kind         string `json:"kind"`                   // image | file
	Name         string `json:"name,omitempty"`         // 显示名或识图描述
	Path         string `json:"path,omitempty"`         // 本地文件路径
	Hash         string `json:"hash,omitempty"`         // 媒体库里的图（64 位小写十六进制）
	Conversation string `json:"conversation,omitempty"` // 媒体凭据的来源事件所在会话
	Event        string `json:"event,omitempty"`        // 媒体凭据的来源事件 id
	Width        int    `json:"width,omitempty"`        // 图片的像素宽高：发送时量好存下，界面按它占位；老记录没有，读回时补量
	Height       int    `json:"height,omitempty"`
}

// Part 是主人这条消息里的一块；Quote 为 nil 就是普通文字块。
type Part struct {
	Quote       *Quote       `json:"quote,omitempty"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	// Mentions、All 只有悄悄话的引用块用（规格-群聊引用和路由 5.1）：对象的角色 ID，All 表示“所有人”（Mentions 存发送时展开的结果）。
	Mentions []string `json:"mentions,omitempty"`
	All      bool     `json:"all,omitempty"`
}

// JoinText 是各块文字按顺序用换行连起来：events.text 存它，记忆、搜索看到的是干净的话。单个普通块就是原话。
func JoinText(parts []Part) string {
	var ts []string
	for _, p := range parts {
		if p.Text != "" {
			ts = append(ts, p.Text)
		}
	}
	return strings.Join(ts, "\n")
}

// escapeQuoteFrame：被引用的片段和名字里的「」【】换成『』〔〕。框架用这两对括号分段，
// 别人的话里带「」【】就能提前关上引号、伪造出“【回复 2】”或“主人对此说”（2026-11-11 Bun 复核两边都有）。
var quoteFrameEscaper = strings.NewReplacer("「", "『", "」", "』", "【", "〔", "】", "〕")

func escapeQuoteFrame(s string) string { return quoteFrameEscaper.Replace(s) }

// 结构化写法的固定措辞；改了就等于改了所有带引用的消息，要走 intended 登记。
const partsHeader = "（所有者这条消息分了 %d 个部分，按顺序排列。每个【回复 N】只针对它自己引用的那一段，【另外】的话不针对任何引用。）"

// Said 是主人这条消息给模型看的样子。parts 为空、或只有一个没有引用的块：就是普通消息原样（有附件的在后面按行加占位），
// 不加任何标记，旧对话和缓存不变。其余用结构化写法，例如：
//
//	（主人这条消息分了 3 个部分，按顺序排列。……）
//	【回复 1】引用了 示例角色 在 12:00 说的：「第二句在那」
//	主人对此说：这句是什么意思 [图片：截图]
//	【另外】顺便问下，晚上吃什么
func Said(parts []Part, text string) string {
	if len(parts) == 0 {
		return text
	}
	if len(parts) == 1 && parts[0].Quote == nil {
		return text + attachmentLines(parts[0].Attachments, "\n")
	}
	var b strings.Builder
	fmt.Fprintf(&b, partsHeader, len(parts))
	n := 0
	for _, p := range parts {
		b.WriteString("\n")
		if q := p.Quote; q != nil {
			n++
			name := strings.Join(strings.Fields(q.Name), " ")
			if name == "" {
				name = "对方"
			}
			snip := escapeQuoteFrame(EscapeUserText(q.Text))
			if q.Truncated {
				snip += "…（已截断）"
			}
			who := "引用了 " + escapeQuoteFrame(EscapeUserText(name))
			if q.Clock != "" {
				who += " 在 " + q.Clock + " 说的"
			}
			b.WriteString("【回复 " + strconv.Itoa(n) + "】" + who + "：「" + snip + "」\n所有者对此说：")
			if p.Text == "" && len(p.Attachments) == 0 {
				b.WriteString("（没有写字）")
			}
		} else {
			b.WriteString("【另外】")
		}
		b.WriteString(p.Text)
		b.WriteString(attachmentLines(p.Attachments, " "))
	}
	return b.String()
}

// attachmentLines 把附件按顺序写成占位，每个前面加 sep。真图直给（feat/real-images）接这里：
// 模型能看图时，这里改成交给模型的图片段，文字里只留一个编号占位；现在只写文字占位。
func attachmentLines(as []Attachment, sep string) string {
	var b strings.Builder
	for _, a := range as {
		b.WriteString(sep)
		label := "图片"
		if a.Kind == "file" {
			label = "文件"
		}
		detail := a.Name
		if a.Kind == "file" && a.Path != "" {
			detail = a.Path
		}
		if detail == "" {
			b.WriteString("[" + label + "]")
		} else {
			b.WriteString("[" + label + "：" + EscapeUserText(detail) + "]")
		}
	}
	return b.String()
}

// Select 按主人框选的结果决定要引用原文的哪一段。orig 是被引用消息的原文，sel 是选中的文字（空白 = 整条），
// start、end 是主人传来的字符偏移（可选，end <= start 当作没给）。
// 位置对得上就用，对不上再在原文里找 sel 的第一处；都找不到退回原文开头 QuoteFallbackRunes 字并标 Fallback，由调用方记日志。
func Select(orig, sel string, start, end int) Quote {
	r := []rune(orig)
	if strings.TrimSpace(sel) == "" {
		return clip(Quote{Text: orig, Whole: true})
	}
	sr := []rune(sel)
	if end > start && start >= 0 && end <= len(r) && string(r[start:end]) == sel {
		return clip(Quote{Text: sel, Start: start, End: end})
	}
	if i := strings.Index(orig, sel); i >= 0 {
		s := utf8.RuneCountInString(orig[:i])
		return clip(Quote{Text: sel, Start: s, End: s + len(sr)})
	}
	if len(r) > QuoteFallbackRunes {
		r = r[:QuoteFallbackRunes]
	}
	return Quote{Text: string(r), Fallback: true}
}

func clip(q Quote) Quote {
	if r := []rune(q.Text); len(r) > QuoteMaxRunes {
		q.Text, q.Truncated = string(r[:QuoteMaxRunes]), true
		if q.End > q.Start {
			q.End = q.Start + QuoteMaxRunes
		}
	}
	return q
}
