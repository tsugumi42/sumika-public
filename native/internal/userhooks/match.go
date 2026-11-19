package userhooks

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// toolAliases：Claude Code 的工具名对到 Sumika 的，搬配置时 matcher 不用改
var toolAliases = map[string][]string{
	"edit":       {"Edit", "MultiEdit"},
	"write":      {"Write"},
	"read":       {"Read"},
	"powershell": {"Bash", "PowerShell"},
	"grep":       {"Grep"},
	"find":       {"Glob"},
	"ls":         {"LS"},
	"web_fetch":  {"WebFetch"},
	"web_search": {"WebSearch"},
	"task":       {"Task", "Agent"},
	"todo_write": {"TodoWrite"},
}

// matcher 照 Codex engine/matcher.rs 和 Claude Code 文档：空和 * 是全部；只有字母数字下划线竖线时按名单比，否则按正则
type matcher struct {
	all   bool
	exact []string
	re    *regexp.Regexp
}

var plainMatcher = regexp.MustCompile(`^[A-Za-z0-9_|]+$`)

func newMatcher(pattern string) (matcher, error) {
	switch {
	case pattern == "" || pattern == "*":
		return matcher{all: true}, nil
	case plainMatcher.MatchString(pattern):
		return matcher{exact: strings.Split(pattern, "|")}, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return matcher{}, err
	}
	return matcher{re: re}, nil
}

func (m matcher) matches(tool string) bool {
	if m.all {
		return true
	}
	for _, name := range append([]string{tool}, toolAliases[tool]...) {
		for _, x := range m.exact {
			if x == name {
				return true
			}
		}
		if m.re != nil && m.re.MatchString(name) {
			return true
		}
	}
	return false
}

// ifRule 是 handler 上的 if："工具名(模式)" 或 "工具名"
type ifRule struct {
	tool    matcher
	pattern *regexp.Regexp // 比文件路径
	command *regexp.Regexp // 比整条命令
}

var ifForm = regexp.MustCompile(`^([A-Za-z0-9_]+)(?:\((.*)\))?$`)

func parseIf(s string) (*ifRule, error) {
	m := ifForm.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil, fmt.Errorf("if 只支持 工具名(模式) 这一种写法，这里是 %q", s)
	}
	tool, _ := newMatcher(m[1])
	r := &ifRule{tool: tool}
	if m[2] == "" {
		return r, nil
	}
	var err error
	if tool.matches("powershell") {
		r.command, err = commandGlob(m[2])
	} else {
		r.pattern, err = globRegexp(m[2])
	}
	if err != nil {
		return nil, fmt.Errorf("if 里的模式 %q 不合法：%v", m[2], err)
	}
	return r, nil
}

func (r *ifRule) allows(tool, file, command, root string) bool {
	if !r.tool.matches(tool) {
		return false
	}
	switch {
	case r.pattern != nil:
		return file != "" && pathMatches([]*regexp.Regexp{r.pattern}, file, root)
	case r.command != nil:
		return r.command.MatchString(strings.TrimSpace(command))
	}
	return true
}

// commandGlob：命令模式只有 * 是通配（git * 比整条命令）
func commandGlob(p string) (*regexp.Regexp, error) {
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.Compile(`(?is)^` + strings.Join(parts, ".*") + `$`)
}

// globRegexp 把路径 glob 变成正则。不带 / 的比文件名（编译时包一层"任意目录"），带 / 的从工作区根比（或者比绝对路径）。
// 支持 * ? ** [...] {a,b}；不分大小写（Windows 的路径不分）。
func globRegexp(p string) (*regexp.Regexp, error) {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if p == "" {
		return nil, fmt.Errorf("是空的")
	}
	anchored := strings.Contains(p, "/")
	p = strings.TrimPrefix(p, "./")
	var b strings.Builder
	b.WriteString(`(?i)^`)
	if !anchored {
		b.WriteString(`(?:.*/)?`)
	}
	depth := 0
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				i++
				if i+1 < len(rs) && rs[i+1] == '/' {
					i++
					b.WriteString(`(?:.*/)?`)
				} else {
					b.WriteString(`.*`)
				}
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		case '{':
			depth++
			b.WriteString(`(?:`)
		case '}':
			if depth == 0 {
				return nil, fmt.Errorf("大括号没配对")
			}
			depth--
			b.WriteString(`)`)
		case ',':
			if depth > 0 {
				b.WriteString(`|`)
			} else {
				b.WriteString(`,`)
			}
		case '[':
			end := i + 1
			for end < len(rs) && rs[end] != ']' {
				end++
			}
			if end == len(rs) {
				return nil, fmt.Errorf("方括号没配对")
			}
			set := strings.ReplaceAll(string(rs[i+1:end]), `\`, `\\`)
			if strings.HasPrefix(set, "!") { // shell 的 [!a-z] 写法
				set = "^" + set[1:]
			}
			if set == "" || set == "^" {
				return nil, fmt.Errorf("方括号里是空的")
			}
			b.WriteString(`[` + set + `]`)
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("大括号没配对")
	}
	b.WriteString(`$`)
	return regexp.Compile(b.String())
}

// pathMatches：file 是绝对路径。在工作区里的比相对路径，外面的比绝对路径。
func pathMatches(globs []*regexp.Regexp, file, root string) bool {
	target := filepath.ToSlash(file)
	if root != "" {
		if rel, err := filepath.Rel(root, file); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			target = filepath.ToSlash(rel)
		}
	}
	for _, re := range globs {
		if re.MatchString(target) {
			return true
		}
	}
	return false
}
