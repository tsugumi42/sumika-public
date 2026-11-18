package plugintrace

// 清洗（照 server/src/plugins/util.ts 的 cleanField 和 tracelog.ts 的 cleanUrl、maskPath）。

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

var (
	// ansi 是终端转义序列：ESC [ … 终结字节，ESC ] … BEL/ST（OSC），ESC 加一个字符（0x40～0x5F，不含 [）
	ansi = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\x5c)|[@-Z\x5c-_])`)
	// lineBreaks 是换行类字符：\r \n、垂直制表、换页、NEL、行分隔符、段分隔符
	lineBreaks = regexp.MustCompile(`[\r\n\x0b\x0c\x{85}\x{2028}\x{2029}]+`)
	// controls 是除换行外的控制字符（含 DEL 和 C1）
	controls = regexp.MustCompile(`[\x00-\x08\x0e-\x1f\x{7f}-\x{9f}\t]`)
)

// CleanField 是日志里的一个文字字段：去终端转义和控制字符，各种换行（和制表符）并成空格，按字（码点）截断
func CleanField(s string, max int) string {
	t := ansi.ReplaceAllString(s, "")
	t = lineBreaks.ReplaceAllString(t, " ")
	t = strings.ReplaceAll(t, "\t", " ")
	t = controls.ReplaceAllString(t, "")
	return clipRunes(t, max)
}

// tokenish 是路径里像令牌的段：24 个字符以上、只由字母数字和 -_+=.~% 组成；还要字母和数字都有（长十六进制、base64 一类）
var tokenish = regexp.MustCompile(`^[A-Za-z0-9_\-+=.~%]{24,}$`)

// MaskPath 把路径里像令牌的段换成 ***；作品号（BV 号、推文号）短于 24 或全是数字，不受影响
func MaskPath(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if tokenish.MatchString(s) && strings.ContainsFunc(s, isASCIILetter) && strings.ContainsFunc(s, isASCIIDigit) {
			segs[i] = "***"
		}
	}
	return strings.Join(segs, "/")
}

func isASCIILetter(r rune) bool { return r < 0x80 && (r|0x20) >= 'a' && (r|0x20) <= 'z' }
func isASCIIDigit(r rune) bool  { return r >= '0' && r <= '9' }

// isJSSpace 是 String.prototype.trim 去掉的字符
func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff }

var hostIDNA = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.CheckHyphens(false),
	idna.StrictDomainName(false), idna.VerifyDNSLength(false), idna.BidiRule())

// CleanURL 只留协议、主机、端口和路径（去掉查询串、片段、用户名密码）；不是 http(s) 网址写 NotAURL，最长 URLMax 个字。
// 照浏览器的 URL 解析（WHATWG）写常见的部分：主机小写和国际化域名转 ASCII、去掉默认端口、反斜杠当斜杠、
// 解 . 和 .. 段、路径里的空格和非 ASCII 等按百分号编码；更偏的写法（数字主机名的变体、IPv4 映射的 IPv6）不保证和 Bun 一字不差
func CleanURL(raw string) string {
	s := strings.TrimFunc(raw, isJSSpace)
	s = strings.TrimFunc(s, func(r rune) bool { return r <= 0x20 })
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
	colon := strings.IndexByte(s, ':')
	if colon <= 0 || !validScheme(s[:colon]) {
		return NotAURL
	}
	scheme := strings.ToLower(s[:colon])
	if scheme != "http" && scheme != "https" {
		return NotAURL
	}
	rest := strings.TrimLeft(s[colon+1:], `/\`)
	end := strings.IndexAny(rest, `/\?#`)
	if end < 0 {
		end = len(rest)
	}
	authority, after := rest[:end], rest[end:]
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	host, port, ok := splitHostPort(authority)
	if !ok {
		return NotAURL
	}
	host, ok = cleanHost(host)
	if !ok {
		return NotAURL
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n > 65535 {
			return NotAURL
		}
		if !(scheme == "http" && n == 80 || scheme == "https" && n == 443) {
			host += ":" + strconv.Itoa(n)
		}
	}
	if q := strings.IndexAny(after, "?#"); q >= 0 {
		after = after[:q]
	}
	return clipRunes(scheme+"://"+host+MaskPath(cleanPath(after)), URLMax)
}

func validScheme(s string) bool {
	for i, r := range s {
		switch {
		case isASCIILetter(r):
		case i > 0 && (isASCIIDigit(r) || r == '+' || r == '-' || r == '.'):
		default:
			return false
		}
	}
	return s != ""
}

// splitHostPort 拆主机和端口：方括号里的是 IPv6；端口只能是数字（可以空）
func splitHostPort(a string) (host, port string, ok bool) {
	if strings.HasPrefix(a, "[") {
		end := strings.IndexByte(a, ']')
		if end < 0 {
			return "", "", false
		}
		var rest string
		host, rest = a[:end+1], a[end+1:]
		if rest == "" {
			return host, "", true
		}
		if rest[0] != ':' {
			return "", "", false
		}
		port = rest[1:]
	} else if i := strings.IndexByte(a, ':'); i >= 0 {
		host, port = a[:i], a[i+1:]
	} else {
		host = a
	}
	for _, r := range port {
		if !isASCIIDigit(r) {
			return "", "", false
		}
	}
	return host, port, host != ""
}

// cleanHost 是主机名的浏览器写法：IPv6 规范化、百分号解码后转 ASCII 小写；带禁用字符的不是网址
func cleanHost(h string) (string, bool) {
	if strings.HasPrefix(h, "[") {
		a, err := netip.ParseAddr(h[1 : len(h)-1])
		if err != nil || !a.Is6() || a.Zone() != "" {
			return "", false
		}
		return "[" + a.String() + "]", true
	}
	if strings.Contains(h, "%") {
		d, err := pctDecode(h)
		if err != nil {
			return "", false
		}
		h = d
	}
	a, err := hostIDNA.ToASCII(h)
	if err != nil || a == "" {
		return "", false
	}
	if strings.ContainsFunc(a, func(r rune) bool { return r <= 0x20 || r == 0x7f || strings.ContainsRune("#%/:<>?@[\\]^|", r) }) {
		return "", false
	}
	return strings.ToLower(a), true
}

func pctDecode(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			n, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b.WriteByte(byte(n))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || (c|0x20) >= 'a' && (c|0x20) <= 'f' }

// cleanPath 是路径的浏览器写法：反斜杠当斜杠，解 . 和 .. 段，按路径的编码集百分号编码（已有的 %xx 原样留）。
// 编码集照 Bun 实测：比 WHATWG 的路径编码集多一个 ^
func cleanPath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if p == "" {
		return "/"
	}
	var out []string
	parts := strings.Split(p[1:], "/")
	for i, seg := range parts {
		last := i == len(parts)-1
		switch strings.ToLower(seg) {
		case "..", ".%2e", "%2e.", "%2e%2e":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		case ".", "%2e":
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, encodePathSeg(seg))
		}
	}
	return "/" + strings.Join(out, "/")
}

const upperHex = "0123456789ABCDEF"

func encodePathSeg(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f || strings.IndexByte("\"#<>?^`{}", c) >= 0 {
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&15])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
