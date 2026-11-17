// Package textclean 是模型回复的后处理，照 daemon/util/tail-junk.ts（句尾乱码）和 daemon/util/clean-text.ts（句尾多吐的"】【"）。
// 认字用 unicode_gen.go 里从 Bun 导出的表（scripts/gen-go-unicode.ts），和 Bun 版是同一份 Unicode；
// 两边跑同一份测试向量（testdata/vectors.json，scripts/go-golden-clean.test.ts 导出），规则、为什么见 TS 版的注释和 docs/notes/句尾乱码规则-2026-10-19.md
package textclean

import (
	"fmt"
	"sort"
	"strings"
)

type table [][2]rune

func (t table) has(r rune) bool {
	i := sort.Search(len(t), func(i int) bool { return t[i][1] >= r })
	return i < len(t) && t[i][0] <= r
}

// IsJSSpace：JS 的 \s（带 u 标志）、String.prototype.trim 去掉的字
func IsJSSpace(r rune) bool { return jsSpace.has(r) }

// IsEmojiPresentation：Unicode 的 Emoji_Presentation=Yes，不带 VS16 也默认画成彩色表情（✨、😀）；
// ㊗、❤ 这类不在里面，要跟 U+FE0F 才是彩色表情。链接卡片用它判断哪些字换成 twemoji。
func IsEmojiPresentation(r rune) bool { return emojiPresentation.has(r) }

// Trim 和 JS 的 String.prototype.trim 一样（Go 的 strings.TrimSpace 多去 U+0085、少去 U+FEFF）
func Trim(s string) string { return strings.TrimFunc(s, IsJSSpace) }

// TrimEnd 和 s.replace(/\s+$/u, ”) 一样
func TrimEnd(s string) string { return strings.TrimRightFunc(s, IsJSSpace) }

// Hit 是删掉的、或者像乱码只记数的一处
type Hit struct {
	Kind string // 哪一类
	S    string // 是哪几个字
	Cps  string // 码位写法："U+5FFFF U+0020"
}

// Codepoints："U+5FFFF U+0020"
func Codepoints(s string) string {
	parts := make([]string, 0, len(s))
	for _, r := range s {
		parts = append(parts, fmt.Sprintf("U+%04X", r))
	}
	return strings.Join(parts, " ")
}

func hit(kind, s string) Hit { return Hit{Kind: kind, S: s, Cps: Codepoints(s)} }

// 苹果 logo（U+F8FF）：私用区里，但苹果设备打得出来，不删
const appleLogo = 0xf8ff

// 私用区（苹果 logo 除外）、第 4～13 平面：永远不会是她要说的
func isJunkCodepoint(r rune) bool {
	if r == appleLogo {
		return false
	}
	return co.has(r) || (r >= 0x40000 && r < 0xe0000)
}

// 别的未分配码位：可能是还不认识的新字符，只记数
func isOtherUnassigned(r rune) bool { return cn.has(r) && !isJunkCodepoint(r) }

// 看不见的格式字符；旗帜 emoji 结尾的标签字符不算
func isInvisible(r rune) bool {
	return cf.has(r) && !(r == 0xe0001 || (r >= 0xe0020 && r <= 0xe007f))
}

func isForeign(r rune) bool { return letterOrMark.has(r) && !allowedScript.has(r) }

// 末尾落单的右引号"”"：前面没有没合上的左引号（“ 或半角 "）
func strayQuote(cs []rune) bool {
	if len(cs) == 0 || cs[len(cs)-1] != '”' {
		return false
	}
	open, closed := 0, 0
	for _, c := range cs[:len(cs)-1] {
		switch c {
		case '“', '"':
			open++
		case '”':
			closed++
		}
	}
	return open <= closed
}

// Options：紧挨着汉字的外文碎片删不删（TS 版 TailOptions.dropForeign；只有 QQ 群删）
type Options struct {
	DropForeign bool
}

// Junk 是 DropTailJunk 的结果
type Junk struct {
	Text    string // 清过的话：什么都没删时原样返回（连末尾的换行），删过时去掉末尾空白
	Dropped []Hit  // 删掉的，从最末尾往里排
	Suspect []Hit  // 像乱码但没删的
}

// DropTailJunk 剥掉整段话末尾的乱码，照 tail-junk.ts 的 dropTailJunk
func DropTailJunk(text string, o Options) Junk {
	var dropped, suspect []Hit
	body := text
	for guard := 0; guard < 32; guard++ {
		cs := []rune(TrimEnd(body))
		if len(cs) == 0 {
			break
		}
		last := cs[len(cs)-1]
		n, kind := 0, ""
		switch {
		case isJunkCodepoint(last):
			for n < len(cs) && isJunkCodepoint(cs[len(cs)-1-n]) {
				n++
			}
			kind = "私用区 / 未分配码位"
		case isInvisible(last):
			for n < len(cs) && isInvisible(cs[len(cs)-1-n]) {
				n++
			}
			kind = "零宽字符"
		case isForeign(last):
			k := 0
			for k < len(cs) && isForeign(cs[len(cs)-1-k]) {
				k++
			}
			pre := cs[:len(cs)-k]
			if !anyRune(pre, isForeign) {
				if len(pre) > 0 && han.has(pre[len(pre)-1]) {
					if o.DropForeign {
						n, kind = k, "外文碎片"
					} else {
						suspect = append(suspect, hit("外文碎片（不在群里，没删）", string(cs[len(cs)-k:])))
					}
				} else if len(pre) > 0 && IsJSSpace(pre[len(pre)-1]) {
					if t := []rune(TrimEnd(string(pre))); len(t) > 0 && han.has(t[len(t)-1]) {
						suspect = append(suspect, hit("外文碎片（前面隔着空格，没删）", string(cs[len(cs)-k:])))
					}
				}
			}
		case last == '”' && strayQuote(cs):
			n, kind = 1, "落单的右引号"
		case last == '、':
			if len(cs) >= 2 && cs[len(cs)-2] == '、' {
				k := 0
				for k < len(cs) && cs[len(cs)-1-k] == '、' {
					k++
				}
				suspect = append(suspect, hit("句尾顿号（连着几个，没删）", strings.Repeat("、", k)))
			} else {
				n, kind = 1, "句尾顿号"
			}
		}
		if n == 0 {
			break
		}
		dropped = append(dropped, hit(kind, string(cs[len(cs)-n:])))
		body = string(cs[:len(cs)-n])
	}
	out := text
	if len(dropped) > 0 {
		out = TrimEnd(body)
	}
	return Junk{Text: out, Dropped: dropped, Suspect: append(suspect, suspects(out)...)}
}

func anyRune(rs []rune, f func(rune) bool) bool {
	for _, r := range rs {
		if f(r) {
			return true
		}
	}
	return false
}

var spam = []string{"购彩", "乐彩", "彩在线", "超碰", "国产成人", "AV片", "熟妇", "不中返", "计划群"}
var oddHan = []string{"巜"}

const symbolRun = 4

// 末尾的"**"：正好两个，并且整句里两个一组的星号是单数
func strayBold(t string) bool {
	if !strings.HasSuffix(t, "**") || strings.HasSuffix(t, "***") {
		return false
	}
	pairs, run := 0, 0
	for _, r := range t + "\x00" {
		if r == '*' {
			run++
			continue
		}
		if run == 2 {
			pairs++
		}
		run = 0
	}
	return pairs%2 == 1
}

func isASCIILetter(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }

// 只记数：清完以后的结尾还像乱码的几种（tail-junk.ts suspects）
func suspects(text string) []Hit {
	t := TrimEnd(text)
	cs := []rune(t)
	var hits []Hit
	if len(cs) == 0 {
		return hits
	}
	last := cs[len(cs)-1]
	if isOtherUnassigned(last) {
		hits = append(hits, hit("未分配码位（可能是新字符，没删）", string(last)))
	}
	// 最后一个分句：按空白、标点、～ ~ 切开的最后一段（t.split(/[\s\p{P}～~]/u).at(-1)）
	clause := t
	for i := len(cs) - 1; i >= 0; i-- {
		if r := cs[i]; IsJSSpace(r) || punct.has(r) || r == '～' || r == '~' {
			clause = string(cs[i+1:])
			break
		}
	}
	for _, w := range spam {
		if strings.Contains(clause, w) {
			hits = append(hits, hit("垃圾词", clause))
			break
		}
	}
	for _, w := range oddHan {
		if strings.HasSuffix(t, w) {
			hits = append(hits, hit("怪字", string(last)))
			break
		}
	}
	// 英文串：/(?<![A-Za-z0-9])([A-Za-z]+)[?!.]?$/，前面是汉字、别处没有拉丁字母
	end := len(cs)
	if end > 0 && (cs[end-1] == '?' || cs[end-1] == '!' || cs[end-1] == '.') {
		end--
	}
	start := end
	for start > 0 && isASCIILetter(cs[start-1]) {
		start--
	}
	if start < end && (start == 0 || !(isASCIILetter(cs[start-1]) || cs[start-1] >= '0' && cs[start-1] <= '9')) {
		before := cs[:start]
		if anyRune(before, han.has) && !anyRune(before, isASCIILetter) {
			hits = append(hits, hit("英文串", string(cs[start:])))
		}
	}
	if so.has(last) && !emojiPresentation.has(last) {
		k := 0
		for k < len(cs) && cs[len(cs)-1-k] == last {
			k++
		}
		if k >= symbolRun {
			hits = append(hits, hit("符号串", string(cs[len(cs)-k:])))
		}
	}
	if strayBold(t) {
		hits = append(hits, hit("**", "**"))
	}
	return hits
}

// "】【"后面跟着这些（或者到了结尾）才算乱码
func afterBracket(r rune) bool {
	return strings.ContainsRune("。！？…，、；：.!?,;~～\n\r\"”」』）)", r)
}

// Brackets 是 DropStrayBrackets 的结果
type Brackets struct {
	Text    string
	Dropped int // 删了几处"】【"
	Lone    int // 落单的括号（没动）
}

// DropStrayBrackets 只删前面没有没合上的"【"、后面紧跟标点 / 换行 / 结尾的"】【"，照 clean-text.ts
func DropStrayBrackets(text string) Brackets {
	if !strings.Contains(text, "】") {
		return Brackets{Text: text, Lone: strings.Count(text, "【")}
	}
	cs := []rune(text)
	drop := map[int]bool{}
	depth, lone := 0, 0
	for i := 0; i < len(cs); i++ {
		switch c := cs[i]; {
		case c == '【':
			depth++
		case c == '】':
			if depth > 0 {
				depth--
			} else if i+1 < len(cs) && cs[i+1] == '【' && (i+2 == len(cs) || afterBracket(cs[i+2])) {
				drop[i], drop[i+1] = true, true
				i++
			} else {
				lone++
			}
		}
	}
	lone += depth
	if len(drop) == 0 {
		return Brackets{Text: text, Lone: lone}
	}
	var b strings.Builder
	for i, c := range cs {
		if !drop[i] {
			b.WriteRune(c)
		}
	}
	return Brackets{Text: b.String(), Dropped: len(drop) / 2, Lone: lone}
}
