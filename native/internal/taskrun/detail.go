package taskrun

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxDetailBytes 是 task_runs.detail 的字节上限
const MaxDetailBytes = 500

// 要整段略去原文的类别：它们的错误原文就是模型输出（"输出不是合法 JSON：[...]"），规格第三节第 5 条不许存模型回复原文
var omitBody = map[string]bool{"parse_failed": true, "loop_detected": true}

// 常见令牌的前缀（服务商 key、代码托管、云厂商）
var keyPrefixes = []string{"sk", "pk", "ghp", "gho", "xox[a-z]", "AKIA", "AIza"}

var (
	reURL     = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,15}://[^\s"'<>)\]}，。]+|\bwww\.[^\s"'<>)]+`)
	reEmail   = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
	reConv    = regexp.MustCompile(`\b[a-z][a-z0-9_-]*:(?:group|private|channel|local|dm)[:\w.-]*`)
	reBearer  = regexp.MustCompile(`(?i)\b(bearer|token|apikey|api[_-]key|secret|password|authorization)\b[\s:="']+(?:bearer\s+)?[^\s"',;]+`)
	reKeyLike = regexp.MustCompile(`\b(?:` + strings.Join(keyPrefixes, "|") + `)[-_A-Za-z0-9]{8,}`)
	reLongTok = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{24,}`)
	reIPv4    = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d{1,5})?\b`)
	reDigits  = regexp.MustCompile(`\d{5,}`)
	reWinPath = regexp.MustCompile(`(?i)\b[a-z]:[/\x5c][^\s"'<>|*?]*`)
	reCtrl    = regexp.MustCompile(`[\x00-\x1f\x7f]+`)
)

// Detail 把一段错误文字变成能写进 task_runs.detail 的样子：遮掉 URL、邮箱、会话名、令牌、号码、IP、本机路径，
// 控制字符并成空格，截到 MaxDetailBytes 以内（不切断汉字）。清两遍等于清一遍。
// class 是 parse_failed、loop_detected 时错误原文是模型输出，只留第一个"："之前的前缀，加"（原文已略）"。
// 装配处另有一层 app.RedactLog（Degrader.Redact），这一层是兜底：不依赖调用方记得传。
func Detail(class, s string) string {
	s = strings.ToValidUTF8(s, "?")
	if omitBody[class] {
		for _, sep := range []string{"：", ":"} {
			if i := strings.Index(s, sep); i >= 0 {
				s = s[:i]
				break
			}
		}
		s = strings.TrimSpace(s)
		if !strings.HasSuffix(s, "（原文已略）") {
			s += "（原文已略）"
		}
	}
	s = reCtrl.ReplaceAllString(s, " ")
	s = reURL.ReplaceAllString(s, "<url>")
	s = reEmail.ReplaceAllString(s, "<邮箱>")
	s = reConv.ReplaceAllString(s, "<会话>")
	s = reBearer.ReplaceAllString(s, "<令牌>")
	s = reKeyLike.ReplaceAllString(s, "<令牌>")
	s = reWinPath.ReplaceAllString(s, "<路径>")
	s = reIPv4.ReplaceAllString(s, "<ip>")
	s = reLongTok.ReplaceAllString(s, "<令牌>")
	s = reDigits.ReplaceAllString(s, "<数字>")
	return truncateBytes(strings.TrimSpace(s), MaxDetailBytes)
}

// truncateBytes 截到最多 n 字节，不切断一个字；截了就在末尾加"…"（算在 n 以内）
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ell = "…"
	cut := n - len(ell)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ell
}
