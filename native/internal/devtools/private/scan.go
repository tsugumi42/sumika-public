// Package private 是隐私扫描（sumika-dev check private），照搬自 scripts/scan-private.ts（规格-提交前检查 第三节、规格-脚本迁Go 第 3 步）：
// 新增的行、文件名、提交说明和作者里有没有本机认识的真实号码、私人字符串（邮箱等）、像密钥的串、本机的真实昵称和群名。
//
// 旧版用了三处正则前后查找，Go 的 RE2 没有，都改成显式边界判断（每处都有对照测试，同一批字符串新旧结论一致）：
//   - 5～12 位的号码：(?<!\d)(\d{5,12})(?!\d) 等价于“最长的一段连续数字，长度 5～12”，先切出数字段再判长度；
//   - 像号码的数字、11 位手机号：前面不是 数字 . - / # 字母，后面不是 数字 或 “.数字”：同样先切数字段，再看前一个字节和后两个字节；
//   - sk- 密钥：前面不是字母数字：逐个找 “sk-”，前面是字母数字就跳到下一个位置再找（不能先全局匹配再过滤，会吃掉后面真正的那个）。
//
// 带 /i 的规则（上下文词、赋值的密钥）旧版在 JS 里只认 ASCII 大小写，这里先把文本按 ASCII 转小写再匹配，不用 Go 的 (?i)（它会让开尔文符号冒充 k）。
// 所有名单只在内存里；输出只有打了码的样子和位置，位置里出现认识的号码或名字也打码。
package private

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Line 是要扫的一行；File 为空表示没有文件（提交说明、作者）。
type Line struct{ Where, Text, File string }

// Hit 是一处命中。
type Hit struct {
	Kind, Value, Where, File string
	Block                    bool
}

// Known 是本机认识的号码、名字、私人字符串（私人字符串已转小写并去掉 +tag）。
type Known struct {
	numbers *oset
	names   *oset
	secrets *oset
}

// NewKnown 给测试和调用方造名单。
func NewKnown(numbers, names, secrets []string) *Known {
	k := &Known{numbers: newSet(), names: newSet(), secrets: newSet()}
	for _, v := range numbers {
		k.numbers.add(v)
	}
	for _, v := range names {
		k.names.add(v)
	}
	for _, v := range secrets {
		k.secrets.add(v)
	}
	return k
}

var (
	reLockFile = regexp.MustCompile(`(?:^|/)(?:bun\.lock|go\.sum|hutch\.lock)$`)
	reDate8    = regexp.MustCompile(`^20[2-3][0-9](0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])$`)
	reTS10     = regexp.MustCompile(`^17[89]`)

	reGitHub  = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)
	reBearer  = regexp.MustCompile(`Bearer[` + jsSpace + `]+[A-Za-z0-9._-]{20,}`)
	reAssign  = regexp.MustCompile(`(?:api[_-]?key|secret|token|password)[` + jsSpace + `]*[:=][` + jsSpace + `]*['"]?[A-Za-z0-9_\-./+]{16,}`)
	rePrivKey = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)

	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+(?:@|%40)[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	reHarm  = regexp.MustCompile(`noreply|no-reply|@(?:[a-z0-9-]+\.)*(?:example\.(?:com|org|net)|invalid|test|localhost|local)$|@anthropic\.com$`)
	rePlus  = regexp.MustCompile(`\+[^@]*@`)
)

// NormMail 是邮箱比较前的归一：小写、%40 变 @、去掉第一个 +tag。
func NormMail(e string) string {
	e = strings.ReplaceAll(jsLower(e), "%40", "@")
	if loc := rePlus.FindStringIndex(e); loc != nil {
		e = e[:loc[0]] + "@" + e[loc[1]:]
	}
	return e
}

// prevOK 是“前面不是 数字 . - / # 字母”（行首也算）。
func prevOK(text string, i int) bool {
	if i == 0 {
		return true
	}
	c := text[i-1]
	return !(isAlnum(c) || c == '.' || c == '-' || c == '/' || c == '#')
}

// dotDigitAfter 是“后面紧跟 .数字”。
func dotDigitAfter(text string, end int) bool {
	return end+1 < len(text) && text[end] == '.' && isDigit(text[end+1])
}

// 下面三个是“结尾锚定”的判断，旧版是 $ 结尾的正则。Go 的正则没有从后往前的优化，一行几十万字符（压缩过的 JSON）时每个数字都要从头扫一遍，
// 实测比旧版慢两个数量级，所以改成从后往前手写；语义和正则一字不差（scan_test.go 里留着原正则，随机串逐个对照）。

// skipSpaceBack 从 i 往前跳过 JS 的空白字符，返回新的位置。
func skipSpaceBack(s string, i int) int {
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:i])
		if !isJSSpace(r) {
			break
		}
		i -= n
	}
	return i
}

// urlBefore 是 /https?:\/\/\S*$/：最后一段不含空白的文字里出现 http:// 或 https://。
func urlBefore(s string) bool {
	i := len(s)
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:i])
		if isJSSpace(r) {
			break
		}
		i -= n
	}
	tok := s[i:]
	return strings.Contains(tok, "http://") || strings.Contains(tok, "https://")
}

var numberContextWords = []string{"qq", "uin", "userid", "user_id", "groupid", "group_id", "gid", "uid", "群", "号", "账号", "好友"}

// numberContext 是 /(?:qq|uin|user_?id|group_?id|gid|uid|群|号|账号|好友)\s*[:：=＝]?\s*["'`]?\s*$/i（s 已按 ASCII 转小写）。
func numberContext(s string) bool {
	i := skipSpaceBack(s, len(s))
	if i > 0 && (s[i-1] == '"' || s[i-1] == '\'' || s[i-1] == '`') {
		i--
	}
	i = skipSpaceBack(s, i)
	if i > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:i])
		if r == ':' || r == '：' || r == '=' || r == '＝' {
			i -= n
		}
	}
	i = skipSpaceBack(s, i)
	for _, w := range numberContextWords {
		if strings.HasSuffix(s[:i], w) {
			return true
		}
	}
	return false
}

var notNumberWords = []string{"bug", "issue", "issues", "crbug", "pr", "max-age=", "port", "0x", "v"}

// notNumberBefore 是 /(?:bug|issue|issues|crbug|pr|cve-\d{4}-|max-age=|port|0x|v)\s*[#:=]?\s*$/i（s 已按 ASCII 转小写）。
func notNumberBefore(s string) bool {
	i := skipSpaceBack(s, len(s))
	// [#:=]? 可有可无：max-age= 自己就以 = 结尾，所以不取标点和取了标点两种结尾都要试
	if notNumberHead(s[:i]) {
		return true
	}
	if i > 0 && (s[i-1] == '#' || s[i-1] == ':' || s[i-1] == '=') {
		return notNumberHead(s[:skipSpaceBack(s, i-1)])
	}
	return false
}

func notNumberHead(head string) bool {
	for _, w := range notNumberWords {
		if strings.HasSuffix(head, w) {
			return true
		}
	}
	if len(head) >= 9 && strings.HasPrefix(head[len(head)-9:], "cve-") && head[len(head)-1] == '-' {
		y := head[len(head)-5 : len(head)-1]
		return isDigit(y[0]) && isDigit(y[1]) && isDigit(y[2]) && isDigit(y[3])
	}
	return false
}

// wholeWord：英文名字要整词对上，中文名字照旧按包含算（边界情况清单 ⑮）。
func wholeWord(text, name string) bool {
	hasAlnum := false
	for i := 0; i < len(name); i++ {
		if isAlnum(name[i]) {
			hasAlnum = true
			break
		}
	}
	if !hasAlnum {
		return strings.Contains(text, name)
	}
	first, last := name[0], name[len(name)-1]
	for i := indexFrom(text, name, 0); i >= 0; i = indexFrom(text, name, i+1) {
		leftBad := isWordByte(first) && i > 0 && isWordByte(text[i-1])
		end := i + len(name)
		rightBad := isWordByte(last) && end < len(text) && isWordByte(text[end])
		if !leftBad && !rightBad {
			return true
		}
	}
	return false
}

func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	i := strings.Index(s[from:], sub)
	if i < 0 {
		return -1
	}
	return from + i
}

// skMatches 找 sk- 密钥：前面不是字母数字，后面连续 16 个以上 [A-Za-z0-9_-]。
func skMatches(text string) []string {
	var out []string
	for i := 0; i < len(text); {
		k := indexFrom(text, "sk-", i)
		if k < 0 {
			break
		}
		if k > 0 && isAlnum(text[k-1]) {
			i = k + 1
			continue
		}
		j := k + 3
		for j < len(text) && (isAlnum(text[j]) || text[j] == '_' || text[j] == '-') {
			j++
		}
		if j-(k+3) >= 16 {
			out = append(out, text[k:j])
			i = j
		} else {
			i = k + 1
		}
	}
	return out
}

// Scan 扫一批行，返回命中（每行里的顺序和旧版一致：号码、手机号、长数字、密钥、私人字符串、邮箱、名字）。
func Scan(lines []Line, k *Known) []Hit {
	var hits []Hit
	for _, ln := range lines {
		text := ln.Text
		if reLockFile.MatchString(ln.File) {
			continue // 锁文件里全是哈希
		}
		push := func(kind, value string, block bool) {
			hits = append(hits, Hit{Kind: kind, Value: value, Where: ln.Where, File: ln.File, Block: block})
		}
		runs := digitRuns(text)
		for _, r := range runs {
			if n := r.end - r.start; n >= 5 && n <= 12 && k.numbers.has(text[r.start:r.end]) {
				push("号码", text[r.start:r.end], true)
			}
		}
		flagged := map[string]bool{}
		for _, r := range runs {
			s := text[r.start:r.end]
			if len(s) != 11 || s[0] != '1' || s[1] < '3' || s[1] > '9' || !prevOK(text, r.start) || dotDigitAfter(text, r.end) {
				continue
			}
			if !k.numbers.has(s) && !urlBefore(text[:r.start]) {
				push("手机号", s, true)
				flagged[s] = true
			}
		}
		lowerText := asciiLower(text)
		for _, r := range runs {
			s := text[r.start:r.end]
			if len(s) < 8 || len(s) > 11 || s[0] == '0' || !prevOK(text, r.start) || dotDigitAfter(text, r.end) {
				continue
			}
			if k.numbers.has(s) || flagged[s] {
				continue // 上面已经拦了
			}
			if urlBefore(text[:r.start]) {
				continue // 网址里的
			}
			lowerBefore := lowerText[:r.start]
			if numberContext(lowerBefore) {
				push("像号码的数字", s, true) // qq: 12345678、群号 12345678
				continue
			}
			if notNumberBefore(lowerBefore) {
				continue // bug 1882408、max-age=31536000
			}
			if len(s) == 10 && reTS10.MatchString(s) {
				continue // 2026 年前后的秒级时间戳
			}
			if len(s) == 8 && reDate8.MatchString(s) {
				continue // 日期 20261026
			}
			push("没见过的长数字", s, false)
		}
		for _, m := range skMatches(text) {
			push("sk-", m, true)
		}
		for _, m := range reGitHub.FindAllString(text, -1) {
			push("GitHub 令牌", m, true)
		}
		for _, m := range reBearer.FindAllString(text, -1) {
			push("Bearer", m, true)
		}
		for _, loc := range reAssign.FindAllStringIndex(lowerText, -1) {
			push("赋值的密钥", text[loc[0]:loc[1]], true)
		}
		for _, m := range rePrivKey.FindAllString(text, -1) {
			push("私钥", m, true)
		}
		lower := jsLower(text)
		for _, s := range k.secrets.ord {
			if strings.Contains(lower, s) {
				push("私人字符串", s, true)
			}
		}
		for _, m := range reEmail.FindAllString(text, -1) {
			e := NormMail(m)
			if k.secrets.has(e) {
				if !strings.Contains(lower, e) {
					push("私人字符串", e, true) // +tag、%40 写法：原文里找不到整串时补拦
				}
				continue
			}
			if reHarm.MatchString(e) {
				continue
			}
			push("邮箱", e, false)
		}
		for _, n := range k.names.ord {
			if wholeWord(text, n) {
				push("真实名字", n, true)
			}
		}
	}
	return hits
}

func perFile(kind string) bool { return kind == "号码" || kind == "私人字符串" }

// AllowKey 是误报登记用的键（只存哈希）：认识的号码、私人字符串只按“值 + 文件”登记，不全局放行；其余按值登记。
func AllowKey(h Hit) string {
	if perFile(h.Kind) {
		return "file:" + hash16(h.Kind+"|"+h.Value+"|"+h.File)
	}
	return hash16(h.Value)
}

// MaskWhere：输出的位置里也可能有号码或名字（文件名带号码时），打码再打印。
func MaskWhere(where string, k *Known) string {
	var b strings.Builder
	last := 0
	for _, r := range digitRuns(where) {
		if n := r.end - r.start; n >= 5 && n <= 12 && k.numbers.has(where[r.start:r.end]) {
			b.WriteString(where[last:r.start])
			b.WriteString(mask(where[r.start:r.end], 2))
			last = r.end
		}
	}
	b.WriteString(where[last:])
	w := b.String()
	for _, n := range k.names.ord {
		if strings.Contains(w, n) {
			w = strings.Join(strings.Split(w, n), mask(n, 1))
		}
	}
	return w
}

var reHunk = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)`)

// Collect 把 git diff 读成要扫的行。“+++ ”只在每个文件的块头（“diff --git”之后、第一个“@@”之前）算文件名；
// 正文里以“++ ”开头的新增行照样扫。文件名本身也作为一行扫。
func Collect(diff string, label func(file string, n int) string) []Line {
	var out []Line
	file := ""
	n := 0
	inHeader := false
	for _, l := range strings.Split(diff, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if strings.HasPrefix(l, "diff --git ") {
			inHeader = true
			file = ""
			continue
		}
		if inHeader {
			if strings.HasPrefix(l, "+++ ") {
				file = ""
				if strings.HasPrefix(l, "+++ b/") {
					file = l[6:]
				}
				if file != "" {
					w := strings.TrimSuffix(label(file, 0), ":0")
					out = append(out, Line{Where: w + " 文件名", Text: file, File: file})
				}
				continue
			}
			if strings.HasPrefix(l, "rename to ") {
				file = l[10:]
				out = append(out, Line{Where: file + " 文件名", Text: file, File: file})
				continue
			}
			if !strings.HasPrefix(l, "@@") {
				continue
			}
			inHeader = false
		}
		if h := reHunk.FindStringSubmatch(l); h != nil {
			v := 0
			for _, c := range h[1] {
				v = v*10 + int(c-'0')
			}
			n = v - 1
			continue
		}
		if strings.HasPrefix(l, "+") {
			n++
			out = append(out, Line{Where: label(file, n), Text: l[1:], File: file})
		} else if !strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "\\") {
			n++
		}
	}
	return out
}
