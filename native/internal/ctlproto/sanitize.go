package ctlproto

import (
	"strings"
	"unicode/utf8"
)

// 控制接口的回复里有外面来的文字：群名（群管理员随便改）、提醒的标题和正文、连接状态、日志行、错误原因。
// 这些文字会原样打在终端里（sumika-ctl 的中文输出），也会进 Agent 的上下文。里面的终端控制序列（ESC 开头的清屏、改窗口标题、
// 写剪贴板）、回车、换行不能原样过去：前者会被终端执行，后者能伪造出整行假的输出。
//
// 服务端出去前过一遍（internal/ctlserver），客户端打印前再过一遍（cmd/sumika-ctl 的 render）：两道各自独立，互不依赖对方。

// Clean 把一段外来文字里的控制字符换掉，其余原样保留：
//   - 换行、回车、U+2028、U+2029 换成空格（单行字段里不该有换行；日志行本来就是一行一行分开交的）；
//   - 制表符保留；
//   - 其余 C0 控制字符（含 ESC）、DEL、C1 控制字符（含单字节的 CSI）、文字方向覆盖符换成 ?；
//   - 不是合法 UTF-8 的字节换成 U+FFFD（8 位终端会把单个字节当控制码）。
//
// 清两遍等于清一遍。
func Clean(s string) string {
	if !needsClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029:
			b.WriteByte(' ')
		case r == '\t':
			b.WriteByte('\t')
		case isControl(r):
			b.WriteByte('?')
		default:
			b.WriteRune(r) // 非法字节在 range 里是 U+FFFD，这里写出来的就是合法的 U+FFFD
		}
	}
	return b.String()
}

func needsClean(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 || (r != '\t' && isControl(r)) {
			return true
		}
	}
	return false
}

// isControl 终端会当成控制码、或者能让显示和实际内容对不上的字符
func isControl(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f: // C0、DEL、C1
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069: // 文字方向覆盖和隔离符
		return true
	}
	return false
}

// MaxGroupNameRunes 是 gate list 里群名的字数上限：群名是群管理员随便写的，太长的一整段文字（比如一段"指令"）不该原样进终端和 Agent 的上下文
const MaxGroupNameRunes = 20

// Truncate 最多留 max 个字（按码点算），多出来的换成一个省略号。已经够短的原样返回
func Truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// CleanText 是多行文字（模型的回复）用的 Clean：保留换行和制表符（回车一律并成换行），其余控制字符、文字方向覆盖符、
// 非法字节照 Clean 处理。清两遍等于清一遍
func CleanText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == 0x2028 || r == 0x2029:
			b.WriteByte('\n')
		case isControl(r):
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
