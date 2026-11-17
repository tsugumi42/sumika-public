// Package linktranslate 是链接展开的翻译（规格-链接翻译）：本地判断要不要翻、提示词、输出清理、缓存。
// 它不认识任何站点，也不直接调模型；模型调用由宿主以 Caller 注入。
package linktranslate

import (
	"regexp"
	"strings"
	"unicode"
)

// 不翻的原因类别（运行页记录用）。
const (
	SkipTooShort      = "too_short"
	SkipAlreadyTarget = "already_target"
	SkipTooLong       = "too_long"
)

// MinLetters 是剥掉链接、账号、话题、表情后至少要有的字数；短文本的语种判断不可靠。
const MinLetters = 4

// MinLatin 是“拉丁字母为主”时至少要有的字母数。
const MinLatin = 8

// MaxRunes 是一次模型调用的原文上限；更长的按段落分段翻译（最多 MaxSegments 段，ADR-391）。
const MaxRunes = 4000

// MaxSegments 是一篇长文最多分几段：再多就整篇不翻（记 too_long），不无限拆。
const MaxSegments = 8

var (
	urlRe     = regexp.MustCompile(`(?i)(?:https?://|www\.)\S+`)
	mentionRe = regexp.MustCompile(`@[\p{L}\p{N}_]+`)
	tagRe     = regexp.MustCompile(`[#＃][\p{L}\p{N}_]+`)
)

type family int

const (
	famOther family = iota
	famZh
	famJa
	famKo
)

func targetFamily(target string) family {
	t := strings.ToLower(target)
	switch {
	case strings.Contains(t, "中") || strings.Contains(t, "chinese") || t == "zh" || strings.HasPrefix(t, "zh-") || strings.HasPrefix(t, "zh_"):
		return famZh
	case strings.Contains(t, "日") || strings.Contains(t, "japanese") || t == "ja" || strings.HasPrefix(t, "ja-"):
		return famJa
	case strings.Contains(t, "韩") || strings.Contains(t, "朝鲜") || strings.Contains(t, "korean") || t == "ko" || strings.HasPrefix(t, "ko-"):
		return famKo
	}
	return famOther
}

// scriptCount 是各种文字的字数；trad 是繁体独有字，shared 是和日文同形的繁体字，jaHan 是日文特征汉字（新字体、和制汉字、日式词覆盖的字）。
type scriptCount struct{ han, trad, shared, jaHan, kana, hangul, latin, other, total int }

func count(text string) scriptCount {
	text = urlRe.ReplaceAllString(text, " ")
	text = mentionRe.ReplaceAllString(text, " ")
	text = tagRe.ReplaceAllString(text, " ")
	var c scriptCount
	c.jaHan = jaWordChars(text)
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			c.han++
			switch {
			case strings.ContainsRune(traditionalOnly, r):
				c.trad++
			case strings.ContainsRune(sharedWithJapanese, r):
				c.shared++
			case strings.ContainsRune(japaneseOnly, r):
				c.jaHan++
			}
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			c.kana++
		case unicode.Is(unicode.Hangul, r):
			c.hangul++
		case unicode.Is(unicode.Latin, r):
			c.latin++
		case unicode.IsLetter(r):
			c.other++
		default:
			continue
		}
		c.total++
	}
	return c
}

// isJapanese：假名至少 2 个或占日文字符的 10% 以上；没有假名时看日文特征汉字（駅 発 売、込 働、開催 受付……，trad.go）。
func (c scriptCount) isJapanese() bool {
	return c.kana >= 2 || (c.kana > 0 && c.kana*10 >= c.han+c.kana) || c.hanJapanese()
}

// Needs 说这段文字要不要翻成 target；不用翻时第二个返回值是原因类别。
// 只看文字种类：目标是中文、日文、韩文时能本地判断；别的目标语言（拉丁字母分不出英法西）本地只挡太短的，其余交给模型的哨兵。
// 长度不在这里挡：超过 MaxRunes 的由 Translate 分段（ADR-391）。
func Needs(text, target string) (bool, string) {
	c := count(text)
	if c.total < MinLetters {
		return false, SkipTooShort
	}
	latinMain := c.latin*2 > c.total && c.latin > 2*c.han && c.latin >= MinLatin // 中文里夹的英文名、术语不算拉丁为主
	isJa := c.isJapanese()
	isKo := c.hangul*10 >= c.total*3
	otherMain := c.other*2 > c.total
	switch targetFamily(target) {
	case famZh:
		if isJa || isKo || latinMain || otherMain {
			return true, ""
		}
		if c.notSimplified() && wantsSimplified(target) {
			return true, "" // 繁体中文也要转成简体（用户 2026-11-13：不是简体中文就只放译文）；没有假名的日文也在这里放行
		}
		if c.han > 0 {
			return false, SkipAlreadyTarget
		}
		return false, SkipTooShort // 只有几个拉丁字母，语种没法判
	case famJa:
		if latinMain || isKo || otherMain {
			return true, ""
		}
		if c.kana > 0 {
			return false, SkipAlreadyTarget
		}
		return true, ""
	case famKo:
		if latinMain || isJa || otherMain {
			return true, ""
		}
		if c.hangul > 0 {
			return false, SkipAlreadyTarget
		}
		return true, ""
	}
	return true, ""
}

// SourceLanguage 是这段原文的语种，用中文名写（日语、韩语、英语、中文）；认不出来是空串（调用方写“外语”）。
// 只看文字：假名、谚文、汉字一眼分得出；拉丁字母分不出英法西，只有英语的常用虚词够多才认英语，
// 别的（法语、西语、德语……）和西里尔字母、阿拉伯文这些一律不认，宁可写“外语”，不把别的语种标成英语。
// 推特卡片的“翻译自……”用它（用户 2026-11-13）；和 Needs 用同一套数字符的办法，译文来自哪段原文就用哪段原文来问。
func SourceLanguage(text string) string {
	c := count(text)
	if c.total == 0 {
		return ""
	}
	isKo := c.hangul*10 >= c.total*3
	switch {
	case isKo:
		return "韩语"
	case c.isJapanese():
		return "日语"
	case c.han*2 > c.total && c.isTraditional():
		return "繁体中文"
	case c.han*2 > c.total && c.notSimplified():
		return "" // 只有和日文同形的字（個 為 開 時……）、又没有日文特征：分不出是繁体还是日文，不猜，写“外语”
	case c.han*2 > c.total:
		return "中文"
	case c.latin*2 > c.total && looksEnglish(text):
		return "英语"
	}
	return ""
}

// englishStop 是只在英语里常见、别的拉丁字母语种里不撞车的虚词（a、in、on、i、do、no 这些别的语言也有，不放）。
var englishStop = map[string]bool{
	"the": true, "and": true, "of": true, "to": true, "is": true, "are": true, "was": true, "were": true, "that": true, "this": true,
	"with": true, "for": true, "you": true, "your": true, "it": true, "not": true, "but": true, "be": true, "my": true, "we": true,
	"they": true, "what": true, "just": true, "have": true, "has": true, "will": true, "about": true, "from": true, "by": true,
	"at": true, "if": true, "i'm": true, "it's": true, "don't": true, "can't": true, "its": true, "so": true, "all": true, "out": true,
}

// looksEnglish：把文字拆成拉丁词，英语虚词至少两个、至少占五分之一。
func looksEnglish(text string) bool {
	text = urlRe.ReplaceAllString(text, " ")
	text = mentionRe.ReplaceAllString(text, " ")
	text = tagRe.ReplaceAllString(text, " ")
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !(unicode.Is(unicode.Latin, r) || r == '\'' || r == '’') })
	hits := 0
	for _, w := range words {
		if englishStop[strings.ReplaceAll(w, "’", "'")] {
			hits++
		}
	}
	return len(words) >= 4 && hits >= 2 && hits*5 >= len(words)
}
