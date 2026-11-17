package approvalrelay

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
)

// Verdict 是对主人一条私聊回复的解读。只有 VerdictApprove、VerdictDeny 会结算一张单。
type Verdict int

const (
	VerdictNone      Verdict = iota // 不是对审批的回答，照普通聊天走
	VerdictApprove                  // 整句就是“同意”（可带 4 位单号）
	VerdictDeny                     // 整句就是“拒绝”（可带 4 位单号）
	VerdictWeak                     // “好的”“可以”这类短答：不结算，只提示要明确回
	VerdictInvisible                // 去掉看不见的字符后是同意或拒绝：不结算，只提示
	VerdictMixed                    // 带着图、转发、@ 等别的内容：不结算，照普通聊天走
)

// Reply 是解析结果。ID 是回复里写的单号（可空）；Quote 是引用的平台消息号（可空）。
type Reply struct {
	Verdict Verdict
	ID      string
	Quote   string
}

// maxReplyBytes 是能算决定的回复长度上限，超了一律不算。
const maxReplyBytes = 1024

var (
	approveWords = map[string]bool{"同意": true, "批准": true, "允许": true}
	denyWords    = map[string]bool{"拒绝": true, "不同意": true, "否决": true, "不批": true}
	weakWords    = map[string]bool{"好": true, "好的": true, "好吧": true, "可以": true, "行": true, "嗯": true, "ok": true, "okay": true, "y": true, "yes": true,
		"不行": true, "不要": true, "算了": true, "n": true, "no": true}
)

// ParseReply 把主人私聊的一条消息解读成对审批的回答。
//
// 规矩（规格-主人QQ私聊审批 第四、五节）：
//   - 只认文字段和引用段；带别的段（图片、转发、@、表情、卡片）就是 VerdictMixed，转发和引用里写的同意不会被读到；
//   - 整句精确匹配，不做子串匹配；前后空白可以去，中间不剥；
//   - 零宽、双向、控制字符一律不算：先看去掉它们以后像不像决定，像就是 VerdictInvisible（只提示），不像就当没有；
//   - 单号只认 4 位 ASCII 数字，写在词后面（“同意 1234”“同意1234”）。
func ParseReply(segs []types.Seg) Reply {
	var text strings.Builder
	var quote string
	for _, s := range segs {
		switch s.Kind {
		case types.SegText:
			text.WriteString(s.Text)
		case types.SegReply:
			if quote != "" {
				return Reply{Verdict: VerdictMixed}
			}
			quote = s.Target
		default:
			return Reply{Verdict: VerdictMixed}
		}
	}
	raw := text.String()
	if len(raw) > maxReplyBytes || !utf8.ValidString(raw) {
		return Reply{Verdict: VerdictNone}
	}
	hidden := false
	var clean strings.Builder
	for _, r := range raw {
		if invisible(r) {
			hidden = true
			continue
		}
		clean.WriteRune(r)
	}
	v, id := classify(strings.TrimFunc(clean.String(), unicode.IsSpace))
	if v == VerdictNone {
		return Reply{Verdict: VerdictNone, Quote: quote}
	}
	if hidden && (v == VerdictApprove || v == VerdictDeny) {
		return Reply{Verdict: VerdictInvisible, ID: id, Quote: quote}
	}
	if hidden {
		return Reply{Verdict: VerdictNone, Quote: quote}
	}
	return Reply{Verdict: v, ID: id, Quote: quote}
}

// invisible：看不见或会改变显示顺序的字符。普通空白（空格、换行、制表）不算，两端去掉就行。
func invisible(r rune) bool {
	if unicode.IsSpace(r) {
		return false
	}
	return unicode.In(r, unicode.Cf, unicode.Cc, unicode.Co, unicode.Cs, unicode.Mn, unicode.Me)
}

func classify(s string) (Verdict, string) {
	if s == "" {
		return VerdictNone, ""
	}
	word, id := s, ""
	if n := len(s); n > 4 && isDigits(s[n-4:]) {
		word, id = strings.TrimFunc(s[:n-4], unicode.IsSpace), s[n-4:]
	}
	switch {
	case approveWords[word]:
		return VerdictApprove, id
	case denyWords[word]:
		return VerdictDeny, id
	}
	if id == "" && weakWords[strings.ToLower(s)] {
		return VerdictWeak, ""
	}
	return VerdictNone, ""
}

func isDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// escapeInvisible 把看不见的字符写成 \uXXXX 让主人在卡片里看得见（命令里夹零宽字符、双向字符不能让主人看不出来）。
func escapeInvisible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteRune(r)
		case r == utf8.RuneError || invisible(r):
			if r > 0xffff {
				b.WriteString(`\U` + hex(uint32(r), 8))
			} else {
				b.WriteString(`\u` + hex(uint32(r), 4))
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hex(v uint32, width int) string {
	const digits = "0123456789abcdef"
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = digits[v&0xf]
		v >>= 4
	}
	return string(out)
}
