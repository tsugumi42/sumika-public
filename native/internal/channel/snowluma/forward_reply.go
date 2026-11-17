package snowluma

import (
	"encoding/json"
	"strings"
	"unicode"
)

// SnowLuma 的转发节点带内嵌 reply.message；只读直接文字，不递归展开不可信引用。
func replyPreview(raw json.RawMessage) string {
	var parts []struct {
		Type string `json:"type"`
		Data object `json:"data"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			out.WriteString(onlyStr(p.Data, "text"))
		}
	}
	return clip(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\ufffc' {
			return -1
		}
		return r
	}, out.String()))
}
