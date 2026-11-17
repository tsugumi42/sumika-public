package linkreader

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Found struct {
	Start, End       int
	Raw, Open, Clean string
}

var urlStart = regexp.MustCompile(`(?i)https?://|ｈｔｔｐｓ?：／／`)

// FindURLs 的位置始终指向原文；超长候选整条丢弃，不能截成另一个可打开的地址。
func FindURLs(text string) []Found {
	scan := text
	if len(scan) > 64*1024 {
		scan = scan[:64*1024]
		for !utf8.ValidString(scan) {
			scan = scan[:len(scan)-1]
		}
	}
	var out []Found
	for offset := 0; offset < len(scan) && len(out) < 20; {
		match := urlStart.FindStringIndex(scan[offset:])
		if match == nil {
			break
		}
		start, end := offset+match[0], offset+match[1]
		end = scanURLEnd(scan, end)
		offset = end
		if end == len(scan) && len(scan) < len(text) {
			break
		}
		if start > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:start])
			if isURLPrefix(prev) {
				continue
			}
		}
		raw := strings.TrimRight(text[start:end], ".,;:!")
		raw = strings.TrimSuffix(raw, "?")
		open := foldWidth(raw)
		if _, err := parseAny(open); err != nil {
			continue
		}
		clean, err := CleanAny(open)
		if err != nil {
			clean = open
		}
		out = append(out, Found{Start: start, End: start + len(raw), Raw: raw, Open: open, Clean: clean})
	}
	return out
}

// scanURLEnd 从链接开头之后的位置 from 往后扫，返回这条链接在 text 里的结束位置（不含结尾标点的修剪）。
func scanURLEnd(text string, from int) int {
	end, depth := from, 0
	for end < len(text) {
		r, n := utf8.DecodeRuneInString(text[end:])
		if r == '(' {
			depth++
		} else if r == ')' {
			if depth == 0 {
				break
			}
			depth--
		} else if boundary(r) || unicode.Is(unicode.Han, r) || r == '？' || r == '＃' || unicode.IsControl(r) {
			break
		}
		end += n
	}
	return end
}

// URLSpans 返回 text 里每一段“长得像链接”的字节区间 [start,end)：只要以 http(s):// 开头就护住，不校验是否合法，
// 和 SafeSplit 用的是同一条扫描规则。整段文字都扫，不受 FindURLs 的 64 KB / 20 条上限影响。
// 调用方在长文里一次算好、反复查切点，避免每个候选切点都从头扫一遍。
func URLSpans(text string) [][2]int {
	var out [][2]int
	for off := 0; off < len(text); {
		m := urlStart.FindStringIndex(text[off:])
		if m == nil {
			break
		}
		start := off + m[0]
		end := scanURLEnd(text, off+m[1])
		if end <= start {
			end = start + 1
		}
		if end > len(text) {
			end = len(text)
		}
		out = append(out, [2]int{start, end})
		off = max(end, start+1)
	}
	return out
}

// SafeSplit 把 text 在字节位置 at 附近切开，保证不切在链接中间：at 落在某条链接里，就退到这条链接开头（整条链接留给后一段）；
// 链接从 0 开头且一直跨过 at，退不了就进到链接结尾（整条留在前一段）。at 不在链接里原样返回。
// 不看链接是否合法：只要长得像（http(s):// 开头）就护住，宁可少切。
func SafeSplit(text string, at int) int {
	if at <= 0 || at >= len(text) {
		return min(max(at, 0), len(text))
	}
	for off := 0; off < at; {
		m := urlStart.FindStringIndex(text[off:])
		if m == nil {
			break
		}
		start := off + m[0]
		if start >= at {
			break
		}
		end := scanURLEnd(text, off+m[1])
		if end > at {
			if start > 0 {
				return start
			}
			return end
		}
		off = max(end, start+1)
	}
	return at
}

// CutRunes 取 s 的前 n 个字，不切开链接（见 SafeSplit）；返回的 cut 为真表示确实少了后面的内容。
func CutRunes(s string, n int) (head string, cut bool) {
	if n < 0 || utf8.RuneCountInString(s) <= n {
		return s, false
	}
	at := 0
	for i := 0; i < n; i++ {
		_, w := utf8.DecodeRuneInString(s[at:])
		at += w
	}
	at = SafeSplit(s, at)
	return s[:at], at < len(s)
}

func parseAny(raw string) (*url.URL, error) {
	if len(raw) > 2048 || strings.ContainsAny(raw, `\`) || strings.ContainsFunc(raw, unicode.IsControl) {
		return nil, errors.New("非法链接")
	}
	u, err := url.Parse(raw)
	// 非法 query 转义不影响打开，Path 的非法转义仍由 url.Parse 拒绝。
	if err != nil {
		return nil, err
	}
	host := strings.ToLower(u.Hostname())
	if u.User != nil || host == "" || !(strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) || !(strings.Contains(host, ".") || host == "localhost" || net.ParseIP(host) != nil) {
		return nil, errors.New("非法链接")
	}
	return u, nil
}

func CleanAny(raw string) (string, error) {
	raw = foldWidth(raw)
	u, err := parseAny(raw)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Hostname())
	if host == "mall.bilibili.com" {
		if l, ok := Recognize(raw); ok {
			return l.Clean, nil
		}
	}
	// 网易云把资源路由放在 fragment；这里也复用同一张追踪表。
	if (host == "music.163.com" || host == "y.music.163.com") && strings.HasPrefix(u.Fragment, "/") {
		if l, ok := Recognize(raw); ok {
			return l.Clean, nil
		}
	}
	if host == "t.co" || host == "xhslink.com" || host == "v.douyin.com" {
		host = ""
	}
	query, ok := stripTracking(host, u.EscapedPath(), u.RawQuery)
	if !ok {
		return raw, nil
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery, u.ForceQuery = query, false
	return u.String(), nil
}
