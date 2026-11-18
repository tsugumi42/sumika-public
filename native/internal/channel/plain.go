package channel

import (
	"strings"
	"unicode"

	"sumika-native/internal/channel/types"
)

// PlainText 是一条消息的纯文本（events.text 那一列）：从段里派生，段才是准的（规格 第三节"纯文本"一行）。
// 文字以外的段写成方括号里的几个字，照 Bun 的 plainText（daemon/qq/segments.ts）：界面的消息列表、记忆的原话、搜索用它。
// @ 后面是号，不是名字：名字是平台的缓存，会变，第 1 步不查；给模型看的那一行（带名字）是第 3 步拼
func PlainText(segs []types.Seg) string {
	var b strings.Builder
	for i, s := range segs {
		switch s.Kind {
		case types.SegText:
			b.WriteString(s.Text)
		case types.SegAt:
			if s.Target == types.AtAll {
				b.WriteString("@全体成员")
			} else {
				b.WriteString("@" + s.Target)
			}
			// @ 和后面的字之间留一个空格，除非后面的字本来就以空白开头（atGap）
			if !(i+1 < len(segs) && segs[i+1].Kind == types.SegText && startsWithSpace(segs[i+1].Text)) {
				b.WriteByte(' ')
			}
		case types.SegReply:
			// 引用不是文字：被引用的是哪条记在 reply_to 里
		case types.SegImage:
			b.WriteString("[图片]")
		case types.SegEmoji:
			b.WriteString("[表情包]")
		case types.SegFace:
			b.WriteString("[表情]")
		case types.SegSuperface:
			b.WriteString("[超级表情]")
		case types.SegVoice:
			b.WriteString("[语音]")
		case types.SegVideo:
			b.WriteString("[视频]")
		case types.SegFile:
			b.WriteString("[文件]")
		case types.SegForward:
			b.WriteString("[合并转发]")
		case types.SegCard:
			b.WriteString("[卡片]")
		default:
			b.WriteString("[未知消息]")
		}
	}
	return strings.TrimSpace(b.String())
}

func startsWithSpace(s string) bool {
	for _, r := range s {
		return unicode.IsSpace(r)
	}
	return false
}
