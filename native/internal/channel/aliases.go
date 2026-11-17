package channel

import (
	"strings"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
)

// aliasHit 只认主人填的至少两字别名；短英文数字按 ASCII 词边界，避免 DS 被 ADS、DSL 命中。
// 边界照 qq-dsh-bridge evaluateWakeTriggerV2 的短词规则，不在每条消息上编译正则。
func aliasHit(aliases []string, text string) bool {
	for _, alias := range aliases {
		if utf8.RuneCountInString(alias) < 2 {
			continue
		}
		short := len(alias) <= 4
		for i := 0; short && i < len(alias); i++ {
			c := alias[i]
			short = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		}
		if !short {
			if strings.Contains(text, alias) {
				return true
			}
			continue
		}
		for i := 0; i+len(alias) <= len(text); i++ {
			end := i + len(alias)
			if strings.EqualFold(text[i:end], alias) && (i == 0 || !wordByte(text[i-1])) && (end == len(text) || !wordByte(text[end])) {
				return true
			}
		}
	}
	return false
}

func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// aliasInSegs 不检查引用、转发、卡片、发送人名字；那些不是这一条自己说的字。
func aliasInSegs(aliases func(string) []string, character string, segs []types.Seg) bool {
	if aliases == nil {
		return false
	}
	names := aliases(character)
	if len(names) == 0 {
		return false
	}
	for start := 0; start < len(segs); {
		if segs[start].Kind != types.SegText {
			start++
			continue
		}
		end, size := start+1, len(segs[start].Text)
		for end < len(segs) && segs[end].Kind == types.SegText {
			size += len(segs[end].Text)
			end++
		}
		if end == start+1 {
			if aliasHit(names, segs[start].Text) {
				return true
			}
		} else {
			var text strings.Builder
			text.Grow(size)
			for _, seg := range segs[start:end] {
				text.WriteString(seg.Text)
			}
			if aliasHit(names, text.String()) {
				return true
			}
		}
		start = end
	}
	return false
}
