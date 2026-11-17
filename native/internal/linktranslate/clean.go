package linktranslate

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	fenceRe  = regexp.MustCompile(`(?s)\A` + "```" + `[A-Za-z0-9_-]*[ \t]*\n(.*?)\n?` + "```" + `\z`)
	postRe   = regexp.MustCompile(`(?is)\A\s*<post>\s*(.*?)\s*</post>\s*\z`)
	openPost = regexp.MustCompile(`(?i)\A\s*<post>\s*`)
	endPost  = regexp.MustCompile(`(?i)\s*</post>\s*\z`)
	// 首行标签：译文：/ 翻译：/ Translation: / 【译文】
	labelRe = regexp.MustCompile(`(?i)\A\s*(?:【\s*(?:译文|翻译)\s*】|(?:译文|翻译结果|翻译|translation|translated text)\s*[:：])[ \t]*`)
)

var quotePairs = [][2]rune{{'"', '"'}, {'“', '”'}, {'「', '」'}, {'『', '』'}}

// Clean 清理模型输出，返回译文；ok 为假表示没有译文（空、哨兵、和原文一样、长得不像翻译）。
// 保证 Clean(Clean(x)) 等于 Clean(x)：几步单独成立的清理组合后不能留下半截东西。
func Clean(out, source string) (string, bool) {
	text, why := CleanWhy(out, source)
	return text, why == ""
}

// 清理结果为什么没有译文：模型明确判过不用翻（哨兵或和原文一样），还是模型出了问题（回空、超长）。
const (
	WhyNone     = ""               // 有译文
	WhyUnneeded = "no_translation" // 回了哨兵或和原文一样：结论可缓存
	WhyEmpty    = "empty"          // 回空：模型出了问题，不缓存
	WhyTooLong  = "too_long"       // 超长：模型出了问题，不缓存
)

// CleanWhy 同 Clean，没有译文时多返回原因。
func CleanWhy(out, source string) (string, string) {
	s := normalize(out)
	for range 3 { // 每轮剥一层；上限按最坏输入算，三层围栏标签前缀足够
		before := s
		if m := fenceRe.FindStringSubmatch(s); m != nil {
			s = strings.TrimSpace(m[1])
		}
		if m := postRe.FindStringSubmatch(s); m != nil {
			s = strings.TrimSpace(m[1])
		}
		s = openPost.ReplaceAllString(s, "")
		s = endPost.ReplaceAllString(s, "")
		s = labelRe.ReplaceAllString(s, "")
		s = unquote(s)
		s = strings.TrimSpace(s)
		if s == before {
			break
		}
	}
	sentinel := strings.Contains(s, NoTranslation)
	if sentinel {
		s = strings.TrimSpace(strings.ReplaceAll(s, NoTranslation, ""))
	}
	if s == "" && !sentinel {
		return "", WhyEmpty
	}
	if s == "" || same(s, source) {
		return "", WhyUnneeded
	}
	if utf8.RuneCountInString(s) > 3*utf8.RuneCountInString(source)+80 {
		return "", WhyTooLong
	}
	return s, WhyNone
}

func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\uFEFF', '\u200B', '\u200C', '\u200D', '\u2060', 0:
			return -1
		}
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// unquote 只去包住整段的一对引号，且里面不再出现同一种引号（免得把“甲”和“乙”误剥成 甲”和“乙）。
func unquote(s string) string {
	r := []rune(s)
	if len(r) < 2 {
		return s
	}
	for _, p := range quotePairs {
		if r[0] == p[0] && r[len(r)-1] == p[1] {
			inner := string(r[1 : len(r)-1])
			if !strings.ContainsRune(inner, p[0]) && !strings.ContainsRune(inner, p[1]) {
				return strings.TrimSpace(inner)
			}
		}
	}
	return s
}

func same(a, b string) bool {
	f := func(s string) string {
		return strings.ToLower(strings.Join(strings.Fields(s), ""))
	}
	return f(a) == f(b)
}
