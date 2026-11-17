// Package jsstr 是照 JS 字符串和正则的规矩做的几件小事：Bun 版的判定靠 JS 的 trim、\s、/i，Go 自带的写法在边上不一样
package jsstr

import "strings"

// IsSpace 是 JS 的 trim 和正则 \s 认的空白（WhiteSpace 和 LineTerminator）：和 Go 的 unicode.IsSpace 不一样，JS 算 U+FEFF、不算 U+0085；
// Go 正则的 \s 只有 ASCII 的几个
func IsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// Trim 像 JS 的 String.prototype.trim
func Trim(s string) string { return strings.TrimFunc(s, IsSpace) }

// TrimEnd 像 JS 的 trimEnd
func TrimEnd(s string) string { return strings.TrimRightFunc(s, IsSpace) }

// S 是 JS 正则里 \s 的那一组字符，拼进 Go 的正则字符类里用（[` + S + `]，取反写 [^` + S + `]）
const S = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// LowerASCII 只把 A-Z 换成 a-z，别的字原样（长度不变，位置对得上）。JS 不带 u 的 /i 只在 ASCII 字母之间不分大小写
// （ſ 不当 s、开尔文符号 K 不当 k；Go 的 (?i) 会当），所以 Go 里先换成小写、再用小写的正则匹配
func LowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
