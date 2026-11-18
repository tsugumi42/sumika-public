package private

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// jsSpace 是 JS 正则里 \s、String.prototype.trim 认的空白字符集（放进方括号用，和 docs 包同一份写法）。
const jsSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

func isJSSpace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0d, r == ' ', r == 0xa0, r == 0x1680, r >= 0x2000 && r <= 0x200a,
		r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000, r == 0xfeff:
		return true
	}
	return false
}

// jsTrim 等价 JS 的 String.prototype.trim。
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// asciiLower 只把 A-Z 变成 a-z，其余字节原样、长度不变。
// 旧版的 /i 在 JS 里只对 ASCII 生效（开尔文符号、长 s 不会冒充 k、s）；Go 的 (?i) 会，所以带 i 的规则都先走这个再匹配无 i 的正则。
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// jsLower 等价 JS 的 toLowerCase 在本工具用到的范围：Unicode 小写，加上两处 JS 的特殊规则：
//   - İ 变成两个码点（i + 组合点）；
//   - 词尾的 Σ 变成 ς（前面隔着“可忽略字符”是有大小写的字母，后面隔着可忽略字符不是有大小写的字母），其余 Σ 变 σ；Go 的 ToLower 一律变 σ，
//     名单里写 ς 结尾的私人字符串，旧版能拦大写的文本，新版不补这条就放过了。
func jsLower(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch r {
		case 'İ':
			b.WriteString("i̇")
		case 'Σ':
			if finalSigma(rs, i) {
				b.WriteRune('ς')
			} else {
				b.WriteRune('σ')
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isCased(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }

// isCaseIgnorable 是 Unicode 的 Case_Ignorable：Mn、Me、Cf、Lm、Sk，加上 ' . : · ^ ` ¨ 这几个单独列出的字符（U+00AD 在 Cf 里）。
func isCaseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) || strings.ContainsRune("'.:·’‘․‧︓﹒﹕＇．：^`¨", r)
}

// finalSigma 是 Final_Sigma 条件：rs[i] 前面（跳过可忽略字符）是有大小写的字母，后面（跳过可忽略字符）不是有大小写的字母。
func finalSigma(rs []rune, i int) bool {
	j := i - 1
	for j >= 0 && isCaseIgnorable(rs[j]) {
		j--
	}
	if j < 0 || !isCased(rs[j]) {
		return false
	}
	k := i + 1
	for k < len(rs) && isCaseIgnorable(rs[k]) {
		k++
	}
	return k >= len(rs) || !isCased(rs[k])
}

func hash16(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

// mask 照旧版：前 keep 个 UTF-16 码元原样（半个代理对会变成替换字符），后面每个码点一个“＊”，至少一个。
func mask(s string, keep int) string {
	u := utf16.Encode([]rune(s))
	if keep > len(u) {
		keep = len(u)
	}
	n := utf8.RuneCountInString(s) - keep
	if n < 1 {
		n = 1
	}
	return string(utf16.Decode(u[:keep])) + strings.Repeat("＊", n)
}

// jsLen 是 [...s].length（码点数）。
func jsLen(s string) int { return utf8.RuneCountInString(s) }

// jsUTF16Len 是 s.length（UTF-16 码元数）。
func jsUTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// jsNum 是 String(number) 在整数和普通小数上的写法。
func jsNum(f float64) string {
	switch {
	case f != f:
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0" // JS 的 String(-0) 也是 "0"
	}
	a := f
	if a < 0 {
		a = -a
	}
	if a == 0 || (a >= 1e-6 && a < 1e21) {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(s, "e") // Go 的 e+06 写成 JS 的 e+6
	sign := exp[:1]
	exp = strings.TrimLeft(exp[1:], "0")
	if exp == "" {
		exp = "0"
	}
	return mant + "e" + sign + exp
}

// oset 是有序集合：JS 的 Set 按插入顺序遍历，输出顺序（同组里谁先出现）靠它。
type oset struct {
	m   map[string]struct{}
	ord []string
}

func newSet() *oset { return &oset{m: map[string]struct{}{}} }
func (s *oset) add(v string) {
	if _, ok := s.m[v]; !ok {
		s.m[v] = struct{}{}
		s.ord = append(s.ord, v)
	}
}
func (s *oset) has(v string) bool { _, ok := s.m[v]; return ok }
func (s *oset) size() int         { return len(s.ord) }

// digitRun 是文本里一段最长的连续 ASCII 数字（JS 的 \d 只认 ASCII）。
type digitRun struct{ start, end int }

func digitRuns(s string) []digitRun {
	var out []digitRun
	for i := 0; i < len(s); {
		if s[i] < '0' || s[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		out = append(out, digitRun{i, j})
		i = j
	}
	return out
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isAlnum(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
func isWordByte(b byte) bool { return isAlnum(b) || b == '_' }

// reAllDigits 是 /^\d+$/。
var reAllDigits = regexp.MustCompile(`^[0-9]+$`)
