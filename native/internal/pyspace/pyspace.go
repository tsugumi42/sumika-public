// Package pyspace 是 Python 的 str.isspace() / str.strip() 认的空白集合。
//
// 本机嵌入服务（Python 版 server.py，和它的 Go 版 sumika-models）用 `not text.strip()` 判一句话是不是空，空的整批回 400；
// 记忆和守卫在把话送去嵌入之前切句、去首尾空白——切句的口径和服务判空的口径必须是同一个集合，
// 不然切出一句服务眼里的"空白"，整批算不出来（规格-Go本地模型 2.4 末条、记忆写入上线 W1a）。
// 所以集合只在这里写一遍，服务（internal/localmodels/engine）和切句（internal/memory）都调这里。
// 这个包只能依赖标准库：它被常驻程序和 sumika-models 同时链接，archcheck 不许 internal/localmodels 被常驻程序链接。
package pyspace

import (
	"strings"
	"unicode"
)

// IsSpace 是 Python 的 str.isspace() 认的空白：Go 的 unicode.IsSpace 再加 U+001C～U+001F（Python 把这四个控制字符也算空白）。
// 零宽字符（U+200B、U+200C、U+200D、U+FEFF 等）不在里面：只含零宽字符的输入 Python 照常前向，
// 所以"空"只按这个集合判，不能按"分词以后内容 id 为空"判。
// engine 的 TestPySpaceMatchesPython 对着 Python 导出的全部码点（engine/testdata/ref-whitespace.json）逐个比。
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// IsBlank 是 `not text.strip()`：空串或全是空白。
func IsBlank(s string) bool {
	for _, r := range s {
		if !IsSpace(r) {
			return false
		}
	}
	return true
}

// Trim 是 text.strip()：去掉首尾的空白。
func Trim(s string) string { return strings.TrimFunc(s, IsSpace) }
