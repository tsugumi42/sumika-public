// Package jstext 放开发工具里照搬旧 TS 脚本时要和 JS 对齐的几个文本小函数：空白字符集、trim、按 WHATWG 规则解码 UTF-8。
// 旧脚本读 git 输出用的是 Bun 的 toString()：坏字节变成 U+FFFD，输出再编回 UTF-8；trim、trimEnd 认的空白比 Go 的 unicode.IsSpace 多（含 U+FEFF）。
package jstext

import (
	"strings"
	"unicode/utf8"
)

// IsSpace 是 JS 的 \s、String.prototype.trim 认的空白（WhiteSpace 加 LineTerminator）。
func IsSpace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0d, r == ' ', r == 0xa0, r == 0x1680, r >= 0x2000 && r <= 0x200a,
		r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000, r == 0xfeff:
		return true
	}
	return false
}

// Trim 等价 JS 的 trim。
func Trim(s string) string { return strings.TrimFunc(s, IsSpace) }

// TrimEnd 等价 JS 的 trimEnd。
func TrimEnd(s string) string { return strings.TrimRightFunc(s, IsSpace) }

// DecodeLossy 把字节按 WHATWG 的 UTF-8 规则解码：每段不合法的字节（最长的合法前缀算一段）换成一个 U+FFFD。
// Go 自己的 string([]rune(…)) 是逐字节换，遇到被截断的多字节序列会比 JS 多出几个 U+FFFD。
func DecodeLossy(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		if b[i] < 0x80 {
			sb.WriteByte(b[i])
			i++
			continue
		}
		if r, n := utf8.DecodeRune(b[i:]); r != utf8.RuneError || n > 1 {
			sb.Write(b[i : i+n])
			i += n
			continue
		}
		sb.WriteRune(utf8.RuneError)
		i += invalidLen(b[i:])
	}
	return sb.String()
}

// invalidLen 是 b 开头那段不合法序列的长度（至少 1）：lead 字节加上后面仍然合法的续字节。
func invalidLen(b []byte) int {
	lo, hi, need := byte(0x80), byte(0xbf), 0
	switch c := b[0]; {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c == 0xe0:
		lo, need = 0xa0, 2
	case c == 0xed:
		hi, need = 0x9f, 2
	case c >= 0xe1 && c <= 0xef:
		need = 2
	case c == 0xf0:
		lo, need = 0x90, 3
	case c == 0xf4:
		hi, need = 0x8f, 3
	case c >= 0xf1 && c <= 0xf3:
		need = 3
	default:
		return 1
	}
	n := 1
	for ; n <= need && n < len(b); n++ {
		if b[n] < lo || b[n] > hi {
			break
		}
		lo, hi = 0x80, 0xbf // 只有第一个续字节有特别范围
	}
	return n
}
