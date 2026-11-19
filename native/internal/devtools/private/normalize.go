package private

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
)

// 扫描前的文本归一：同一行文字换个写法就能躲过按连续 ASCII 比较的规则（UTF-16 里数字之间隔着 NUL、全角数字和字母、
// 零宽字符、不间断空格）。Scan 对每行扫“原文”和下面这些归一后的副本，副本和原文一样就不重复扫。

// isZeroWidth 零宽字符和软连字符：看不见，夹在号码或名字里就能让按字节的比较对不上。
func isZeroWidth(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF, 0x00AD, 0x180E:
		return true
	}
	return false
}

// foldWidth 去掉零宽字符；全角 ASCII（U+FF01～U+FF5E）换成半角；各类空白（不间断空格、全角空格等）换成一个普通空格（ASCII 的制表符不动，不连续的空格也不合并）。
// 没有要改的就原样返回同一个字符串。
func foldWidth(s string) string {
	need := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			need = true
			break
		}
	}
	if !need {
		return s
	}
	// 先只看有没有要改的：绝大多数非 ASCII 的行（中文文档）什么都不用改，不要为它们建缓冲
	needFold := false
	for _, r := range s {
		if isZeroWidth(r) || (r >= 0xFF01 && r <= 0xFF5E) || (r != ' ' && r != '\t' && unicode.IsSpace(r)) {
			needFold = true
			break
		}
	}
	if !needFold {
		return s
	}
	var b strings.Builder
	changed := false
	for _, r := range s {
		switch {
		case isZeroWidth(r):
			changed = true
		case r >= 0xFF01 && r <= 0xFF5E:
			b.WriteRune(r - 0xFEE0)
			changed = true
		case r != ' ' && r != '\t' && unicode.IsSpace(r):
			b.WriteByte(' ')
			changed = true
		default:
			b.WriteRune(r)
		}
	}
	if !changed {
		return s
	}
	return b.String()
}

// collapseSpaces 连续的空格合并成一个（名字里的空格数量不同也要对得上）。
func collapseSpaces(s string) string {
	if !strings.Contains(s, "  ") {
		return s
	}
	var b strings.Builder
	prev := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			if prev {
				continue
			}
			prev = true
		} else {
			prev = false
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// decodeUTF16 把字节串当 UTF-16 解（奇数长度丢掉最后一个字节）。
func decodeUTF16(s string, bigEndian bool) string {
	n := len(s) / 2
	if n == 0 {
		return ""
	}
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		a, b := uint16(s[2*i]), uint16(s[2*i+1])
		if bigEndian {
			u[i] = a<<8 | b
		} else {
			u[i] = b<<8 | a
		}
	}
	return string(utf16.Decode(u))
}

// variants 返回一行要扫的各个写法：原文；含 NUL 的行（UTF-16 文件按行切开后的样子）再加去掉 NUL 的副本和按 UTF-16 小端、大端各个对齐方式解码的副本
// （去 NUL 对数字、邮箱、密钥够用，解码对中文名字用）；每一个再加一份 foldWidth 后的副本。去重。
func variants(text string) []string {
	base := []string{text}
	// UTF-16 文件按行切开：换行 0A 00（小端）或 00 0A（大端）里的 NUL 留在行首或行尾，第一行靠开头的 BOM
	if strings.IndexByte(text, 0) >= 0 || strings.HasPrefix(text, "\xff\xfe") || strings.HasPrefix(text, "\xfe\xff") {
		base = append(base, strings.ReplaceAll(text, "\x00", ""))
		for off := 0; off < 2 && off < len(text); off++ {
			for _, be := range []bool{false, true} {
				if d := decodeUTF16(text[off:], be); d != "" {
					base = append(base, d)
				}
			}
		}
	}
	out := make([]string, 0, len(base)*2)
	add := func(v string) {
		for _, o := range out {
			if o == v {
				return
			}
		}
		out = append(out, v)
	}
	for _, b := range base {
		add(b)
		if f := foldWidth(b); f != b {
			add(f)
		}
	}
	return out
}

// looksUTF16 判断一个文件开头是不是 UTF-16 文本（--tree 扫目录时，真正的二进制才跳过）：有 BOM，或者 NUL 很多且几乎都落在同一个奇偶位置上。
func looksUTF16(head []byte) bool {
	if len(head) >= 2 && ((head[0] == 0xFF && head[1] == 0xFE) || (head[0] == 0xFE && head[1] == 0xFF)) {
		return true
	}
	var even, odd int
	for i, c := range head {
		if c == 0 {
			if i%2 == 0 {
				even++
			} else {
				odd++
			}
		}
	}
	total := even + odd
	if total < 8 || total*5 < len(head) { // NUL 至少占五分之一
		return false
	}
	major := even
	if odd > major {
		major = odd
	}
	return major*10 >= total*9
}

// 名单里的名字和长号码，扫描前归一一次（各个 Scan 调用共用）。
type foldedName struct{ orig, key string }

type derived struct {
	once       sync.Once
	names      []foldedName
	spaceNames bool
	longNums   []string
}

func (k *Known) derive() *derived {
	k.d.once.Do(func() {
		for _, n := range k.names.ord {
			key := asciiLower(collapseSpaces(foldWidth(n)))
			k.d.names = append(k.d.names, foldedName{orig: n, key: key})
			if strings.Contains(key, " ") {
				k.d.spaceNames = true
			}
		}
		for _, n := range k.numbers.ord {
			if len(n) >= 8 {
				k.d.longNums = append(k.d.longNums, n)
			}
		}
	})
	return &k.d
}
