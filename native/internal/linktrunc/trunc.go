// Package linktrunc 是链接展开里“正文太长要截”的唯一写法（规格-链接发送改版 B10，用户 2026-11-13）：
// 在上限处往前找最近的标点（中文句读、英文句号逗号等），截在标点之后，补“……”；正文和简介再另起一行写“完整内容见原文”。
// 文字版的简介、小说节选，卡片上的推文正文都走这里，不各写各的。
package linktrunc

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/linkreader"
)

const (
	// Ellipsis 是截断处补的省略号。
	Ellipsis = "……"
	// Notice 是正文被截断后另起一行写的话（行内的短字段只补省略号，不写这一句）。
	Notice = "完整内容见原文"
)

// window 是往前找标点最多退多少个字：上限的四分之一，最少 10、最多 80。
// 再往前就退得太多（200 字的简介只剩一半），这时不找标点，照上限处截。
func window(n int) int { return min(max(n/4, 10), 80) }

// stops 是全角的句读，ASCII 的 . , ; : ! ? 要后面跟空白或非 ASCII 字才算（3.5、example.com 不算）。
const stops = "。！？；，、：…．｡︰！？；，"

// closers 紧跟在句读后面时一起留下（”」』）】》〉）。
const closers = "”’」』）)】》〉]"

func isStop(r rune, next rune, hasNext bool) bool {
	if strings.ContainsRune(stops, r) {
		return true
	}
	switch r {
	case '.', ',', ';', ':', '!', '?':
		return !hasNext || unicode.IsSpace(next) || next > unicode.MaxASCII
	}
	return false
}

// byteAt 返回 s 里第 n 个字（从 0 数）的起始字节位置；n 超过字数返回 len(s)。
func byteAt(s string, n int) int {
	at := 0
	for i := 0; i < n && at < len(s); i++ {
		_, w := utf8.DecodeRuneInString(s[at:])
		at += w
	}
	return at
}

// cutPoint 返回 s 里最终的截点（字节位置）：从上限 at（字节）往前最多 win 个字找最近的标点，截在它后面；
// 找不到：落在拉丁字母的词中间就退到前面的空格（同样不超过 win 个字），再不行照 at 截。
// 截点不落在链接中间（linkreader.SafeSplit）；截完去掉末尾的空白和“│”“|”。
func cutPoint(s string, at, win int) int {
	at = linkreader.SafeSplit(s, at)
	lo := at
	for i := 0; i < win && lo > 0; i++ {
		_, w := utf8.DecodeLastRuneInString(s[:lo])
		lo -= w
	}
	best := -1
	for i := lo; i < at; {
		r, w := utf8.DecodeRuneInString(s[i:])
		next, _ := utf8.DecodeRuneInString(s[i+w:])
		hasNext := i+w < len(s)
		switch {
		case r == '\n':
			if i > lo {
				best = i
			}
		case isStop(r, next, hasNext):
			e := i + w
			for e < at {
				c, cw := utf8.DecodeRuneInString(s[e:])
				if !strings.ContainsRune(closers, c) && !strings.ContainsRune(stops, c) {
					break
				}
				e += cw
			}
			best = e
			i = e
			continue
		}
		i += w
	}
	switch {
	case best > 0:
		at = best
	default:
		last, _ := utf8.DecodeLastRuneInString(s[:at])
		next, _ := utf8.DecodeRuneInString(s[at:])
		if latinWord(last) && latinWord(next) {
			if i := strings.LastIndexFunc(s[lo:at], unicode.IsSpace); i >= 0 {
				at = lo + i
			}
		}
	}
	return len(strings.TrimRightFunc(s[:at], func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("│|｜", r) }))
}

// latinWord 说这个字是不是拉丁字母或数字（词中间不该截断的那类）；中日文不算，在哪截都行。
func latinWord(r rune) bool {
	return r < 0x250 && (unicode.IsLetter(r) || unicode.IsDigit(r)) || r == '\''
}

// Cut 把 s 截到最多 n 个字（含标点，不含补上的省略号）；cut 为真说明确实少了后面的内容。n 小于 0 不截。
func Cut(s string, n int) (head string, cut bool) {
	if n < 0 || utf8.RuneCountInString(s) <= n {
		return s, false
	}
	return s[:cutPoint(s, byteAt(s, n), window(n))], true
}

func ellipsize(head string) string {
	if strings.HasSuffix(head, "…") || strings.HasSuffix(head, "...") {
		return head
	}
	return head + Ellipsis
}

// Inline 是行内短字段（标签、标题、列表里的一行）的写法：截断后补“……”，不另起一行。
func Inline(s string, n int) string {
	head, cut := Cut(s, n)
	if !cut {
		return s
	}
	return ellipsize(head)
}

// Mark 给一段已经截好（或本来就只是开头）的文字补上“……”和“完整内容见原文”。
func Mark(head string) string { return ellipsize(head) + "\n" + Notice }

// Body 是正文、简介的写法：截断后补“……”，下一行写“完整内容见原文”。
func Body(s string, n int) string {
	head, cut := Cut(s, n)
	if !cut {
		return s
	}
	return Mark(head)
}

// FitBody 是卡片这类按“排得下排不下”而不是字数截的写法：fits 说一段显示出来的文字（含省略号和最后一行）排不排得进去。
// 全文排得下原样返回；排不下就找排得下的最长前缀，再往前找标点，写法同 Body。
func FitBody(s string, fits func(display string) bool) (display string, cut bool) {
	if fits(s) {
		return s, false
	}
	show := Mark
	total := utf8.RuneCountInString(s)
	lo, hi := 0, total // 找最大的 k，使前 k 个字加省略号和提示排得下；0 个字也排不下时退到 0
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(show(strings.TrimRightFunc(s[:byteAt(s, mid)], unicode.IsSpace))) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for {
		head := s[:cutPoint(s, byteAt(s, lo), window(lo))]
		display = show(head)
		// 往前找标点以后，补上省略号和提示的整段还得满足 fits（二分只验过上限处的前缀）；不满足再往前收一个字
		if lo == 0 || fits(display) {
			return display, true
		}
		lo = min(lo, utf8.RuneCountInString(head)) - 1
	}
}

// FitBodyLines 是 FitBody 的按行数版：lines 说一段文字排出来几行，最多排 limit 行。
// 比 FitBody 多一条（X-15 样张）：补的“……”不许单独占一行——“……”接在最后一个字后面排不下、被挤到下一行时，
// 这一行只有两个点，难看，就再往前收几个字，让它跟在字后面。
func FitBodyLines(s string, limit int, lines func(display string) int) (display string, cut bool) {
	return FitBody(s, func(d string) bool {
		if lines(d) > limit {
			return false
		}
		head, ok := strings.CutSuffix(d, "\n"+Notice)
		if !ok {
			return true // 全文，没有补省略号
		}
		bare, ok := strings.CutSuffix(head, Ellipsis)
		if !ok {
			return true
		}
		return lines(bare+"\n"+Notice) >= lines(d) // 补上“……”没有多出一行
	})
}
