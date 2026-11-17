package memory

import (
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// ParseOutboundConfirmation 让线上守卫和评测只认同一个完整答案；不从解释里捞 JSON，也不修补坏 JSON。
func ParseOutboundConfirmation(text string) (leaked bool, reason string) {
	text = outboundConfirmationBody(text)
	if !json.Valid([]byte(text)) {
		return false, "model_bad_json"
	}
	dec := json.NewDecoder(strings.NewReader(text))
	token, _ := dec.Token()
	if token != json.Delim('{') || !dec.More() {
		return false, "model_bad_answer"
	}
	key, _ := dec.Token()
	var answer string
	if key != "leaked" || dec.Decode(&answer) != nil || (answer != "yes" && answer != "no") || dec.More() {
		return false, "model_bad_answer"
	}
	_, _ = dec.Token()
	if _, err := dec.Token(); err != io.EOF {
		return false, "model_bad_json"
	}
	return answer == "yes", ""
}

// 只剥完整的一层围栏；围栏外的解释仍交给严格解析拒绝。
func outboundConfirmationBody(text string) string {
	text = strings.TrimSpace(text)
	first, rest, found := strings.Cut(text, "\n")
	if found && (strings.TrimSuffix(first, "\r") == "```json" || strings.TrimSuffix(first, "\r") == "```") && strings.HasSuffix(rest, "\n```") {
		return strings.TrimSpace(strings.TrimSuffix(rest, "\n```"))
	}
	return text
}

func (m *Memory) logConfirmationFailure(reason, text string) {
	count := utf8.RuneCountInString(text)
	if _, bad := ParseOutboundConfirmation(text); bad != "" {
		// 模型会复述资料；仅对通过完整 yes/no 结构检查的原始回复开放预览。
		m.logf("记忆守卫确认：reason=%s reply_chars=%d", reason, count)
		return
	}
	n := 0
	for i := range text {
		if n == 200 {
			text = text[:i]
			break
		}
		n++
	}
	m.logf("记忆守卫确认：reason=%s reply_chars=%d reply=%q", reason, count, text)
}
