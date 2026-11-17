package memory

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// 记忆页要显示的字符串都可能是群友说的话变来的（规格-记忆页 3.5）：只当纯文本，控制字符、零宽字符、坏字节换成看得见的占位，
// 不解析 Markdown、不开链接。列表并成一行，详情保留换行。

// placeholder 是控制字符、零宽字符、坏字节的可见替身
const placeholder = '□'

// CleanText 把 s 清成可以放心画出来的样子，最多 max 个字（max <= 0 不截）；cut 是被截掉的字数。
// oneLine 为真时，换行、制表符、连续空白并成一个空格，首尾空白去掉（列表用）；为假时保留换行和制表符（详情用），\r\n 和单独的 \r 都当换行。
// 零宽连接符（U+200D）夹在两个符号之间（表情序列）时保留，其余格式控制字符（零宽、方向控制、BOM、软连字符）一律换占位
func CleanText(s string, oneLine bool, max int) (out string, cut int) {
	var b strings.Builder
	n, count := 0, 0
	rs := []rune(strings.ToValidUTF8(s, string(placeholder)))
	// ToValidUTF8 把一段连续的坏字节换成一个占位；这里不再区分坏字节的个数
	prevSpace := true // 开头的空白不要
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\r':
			if i+1 < len(rs) && rs[i+1] == '\n' {
				continue
			}
			r = '\n'
		}
		switch {
		case r == '\n' || r == '\t':
			if oneLine {
				r = ' '
			}
		case r == 0x200D:
			if !(i > 0 && i+1 < len(rs) && unicode.IsSymbol(rs[i-1]) && unicode.IsSymbol(rs[i+1])) {
				r = placeholder
			}
		case unicode.IsControl(r) || unicode.In(r, unicode.Cf) || r == utf8.RuneError || !unicode.IsPrint(r) && !unicode.IsSpace(r) && !unicode.IsMark(r):
			r = placeholder
		}
		if oneLine {
			if unicode.IsSpace(r) {
				if prevSpace {
					continue
				}
				prevSpace = true
				r = ' '
			} else {
				prevSpace = false
			}
		}
		count++
		if max > 0 && count > max {
			n++
			continue
		}
		b.WriteRune(r)
	}
	out = b.String()
	if oneLine {
		out = strings.TrimRight(out, " ")
	}
	return out, n
}

// CleanLine：一行短字符串（名字、关于谁、原因）的清法，超过 max 个字截断（不加省略号）
func CleanLine(s string, max int) string {
	out, _ := CleanText(s, true, max)
	return out
}
