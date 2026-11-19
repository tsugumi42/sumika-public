package hook

import (
	"regexp"
	"strings"
)

// globMatch 按 lefthook 的 doublestar 写法匹配仓库根起算、正斜杠的路径：
// `**/` 是零层或多层目录，`**` 跨目录，`*`、`?` 不跨 `/`，`{a,b}` 是几选一（可嵌套）。
// 带 (?s)：路径里有换行时 `.*` 也要能跨过去（Linux 上能建这种文件名），不能让对应检查被跳过。
// 标准库没有 `**`，而挑选规则要和原 lefthook.yml 的 glob 逐字照搬，所以自己写这一小段；不引第三方库（archcheck 白名单）。
func globMatch(pattern, name string) bool {
	re, err := regexp.Compile("(?s)^" + globRegexp(pattern) + "$")
	if err != nil {
		return false // 写坏的 glob 不匹配任何文件；表里的 glob 由测试逐条核对
	}
	return re.MatchString(name)
}

func globRegexp(p string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '*' && strings.HasPrefix(p[i:], "**/"):
			b.WriteString(`(?:.*/)?`)
			i += 2
		case c == '*' && strings.HasPrefix(p[i:], "**"):
			b.WriteString(`.*`)
			i++
		case c == '*':
			b.WriteString(`[^/]*`)
		case c == '?':
			b.WriteString(`[^/]`)
		case c == '{':
			depth++
			b.WriteString(`(?:`)
		case c == '}' && depth > 0:
			depth--
			b.WriteString(`)`)
		case c == ',' && depth > 0:
			b.WriteString(`|`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	for ; depth > 0; depth-- {
		b.WriteString(`)`)
	}
	return b.String()
}

// matchAny：globs 里有一个匹配就算。
func matchAny(globs []string, name string) bool {
	for _, g := range globs {
		if globMatch(g, name) {
			return true
		}
	}
	return false
}
