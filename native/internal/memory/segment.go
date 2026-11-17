package memory

import (
	"strings"
	"unicode"
)

// 两字一组切分（规格-记忆 3.6 那条调研建议、规格-记忆-v2 第七节第 3 步，ADR-159 第 3 条）：trigram 对一两个字的词查不到，
// 另建一张按"两个字一组"切好、空格隔开存进 unicode61 的全文表。切之前按文字类型分段，不跨文字、不跨标点；
// 搜索用的副本做全角转半角、英文小写，原文不动。只有一个字的汉字段就存那一个字（"草"这类单字消息也能对上）

type script int

const (
	scriptOther script = iota
	scriptHan
	scriptKana
	scriptHangul
	scriptLatin // 字母和数字算一段：gpt5、v4 这种
)

func scriptOf(r rune) script {
	switch {
	case unicode.Is(unicode.Han, r):
		return scriptHan
	case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || r == 'ー':
		return scriptKana
	case unicode.Is(unicode.Hangul, r):
		return scriptHangul
	case unicode.IsLetter(r) || unicode.IsDigit(r):
		return scriptLatin
	default:
		return scriptOther
	}
}

// fold 是搜索用的副本：全角英数转半角、英文小写
func fold(r rune) rune {
	if r >= 0xFF01 && r <= 0xFF5E {
		r -= 0xFEE0
	}
	return unicode.ToLower(r)
}

// Bigrams 把一段文字切成搜索用的词：汉字、假名、谚文按两个字一组重叠着切（一个字的段就是它自己），英文和数字整个一词。去重、保持出现顺序
func Bigrams(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	var run []rune
	cur := scriptOther
	flush := func() {
		switch {
		case len(run) == 0:
		case cur == scriptLatin:
			add(string(run))
		case len(run) == 1:
			add(string(run))
		default:
			for i := 0; i+2 <= len(run); i++ {
				add(string(run[i : i+2]))
			}
		}
		run = run[:0]
	}
	for _, r := range text {
		r = fold(r)
		s := scriptOf(r)
		if s != cur {
			flush()
			cur = s
		}
		if s != scriptOther {
			run = append(run, r)
		}
	}
	flush()
	return out
}

// bigramDoc 是写进两字全文表的那一栏：切好的词用空格隔开
func bigramDoc(text string) string { return strings.Join(Bigrams(text), " ") }

// bigramQuery 是查两字全文表的 MATCH 表达式：每个词加引号、OR 连起来；最多 40 个（照 trigram 那边的 ftsQuery）
func bigramQuery(text string) string {
	ts := Bigrams(text)
	if len(ts) > 40 {
		ts = ts[:40]
	}
	for i, t := range ts {
		ts[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(ts, " OR ")
}

// trigramQuery 照 memory.ts 的 ftsQuery：去掉标点空白分词，每个词里取连续三个字，OR 连起来，最多 40 个；不够三个字的返回空
func trigramQuery(text string) string {
	clean := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`"'*()^:{}[]-+,.!?，。！？、；;：…~`, r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, text)
	var grams []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(clean) {
		rs := []rune(w)
		for i := 0; i+3 <= len(rs); i++ {
			g := string(rs[i : i+3])
			if !seen[g] {
				seen[g] = true
				grams = append(grams, `"`+strings.ReplaceAll(g, `"`, `""`)+`"`)
			}
		}
	}
	if len(grams) > 40 {
		grams = grams[:40]
	}
	return strings.Join(grams, " OR ")
}
