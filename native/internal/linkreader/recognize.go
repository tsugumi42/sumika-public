package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

// Link 的 Raw 是原输入；ID 不做数字转换，避免长 ID 丢精度。
type Link struct {
	Platform string
	Kind     string // post、video、dynamic、opus、article、live、episode、season、mall、short
	ID       string
	Clean    string
	Raw      string
}

var (
	// 形状参照 N 2.7.1 parsers/bilibili/__init__.py:49-96、
	// A v1.6.1 core/parsers/twitter.py:45-57；镜像域名按任务补齐。
	// /i/web/status/ID 是网页分享里常见的无账号形态；/statuses/ID 是旧式链接（有无账号两种）。
	xPath      = regexp.MustCompile(`^/(i/web|[A-Za-z0-9_]+)/status/([0-9]+)(?:/(?:photo|video)/[0-9]+)?/?$`)
	xOldPath   = regexp.MustCompile(`^/(?:([A-Za-z0-9_]+)/)?statuses/([0-9]+)/?$`)
	videoPath  = regexp.MustCompile(`^/(?:video/)?((?i:BV)[A-Za-z0-9]{10}|(?i:av)[0-9]+)/?$`)
	numberPath = regexp.MustCompile(`^/([0-9]+)/?$`)
	shortPath  = regexp.MustCompile(`^/[A-Za-z0-9_-]+/?$`)
)

// Recognize 只识别完整链接或裸域名，不从非法 URL 中截取合法子串。
func Recognize(raw string) (Link, bool) {
	s := foldWidth(strings.TrimSpace(raw))
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" {
		return Link{}, false
	}
	host := strings.ToLower(u.Hostname())
	if u.Host == "" || strings.ContainsAny(s, "\\\r\n\t ") {
		return Link{}, false
	}
	if port := u.Port(); port != "" && !((u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")) {
		return Link{}, false
	}
	// 空端口也拒绝；否则第二遍会无声改变输入的 authority。
	if strings.HasSuffix(u.Host, ":") {
		return Link{}, false
	}
	l := Link{Raw: raw}
	path := u.EscapedPath()
	if link, ok, handled := recognizeBiliExtra(host, u, raw); handled {
		return link, ok
	}
	if strings.HasPrefix(path, "/s/video/") && (host == "www.bilibili.com" || host == "bilibili.com" || host == "m.bilibili.com") {
		path = path[2:] // 微信、小程序旧分享：/s/video/BV… 就是 /video/BV…
	}
	switch host {
	case "x.com", "www.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com", "mobile.x.com", "m.twitter.com", "m.x.com",
		"fxtwitter.com", "vxtwitter.com", "fixupx.com", "fixvx.com", "d.fxtwitter.com":
		account, id, ok := xStatus(path)
		if !ok {
			return Link{}, false
		}
		l.Platform, l.Kind, l.ID = "x", "post", id
		host, path = "x.com", "/"+account+"/status/"+id
	case "t.co":
		if !shortPath.MatchString(path) {
			return Link{}, false
		}
		l.Platform, l.Kind = "x", "short"
	case "b23.tv", "bili2233.cn":
		if !shortPath.MatchString(path) {
			return Link{}, false
		}
		l.Platform, l.Kind = "bilibili", "short"
	case "mall.bilibili.com":
		return recognizeMall(u, raw)
	case "bilibili.com", "www.bilibili.com", "m.bilibili.com", "mobile.bilibili.com":
		l.Platform = "bilibili"
		host = "www.bilibili.com"
		if m := videoPath.FindStringSubmatch(path); m != nil {
			l.Kind, l.ID = "video", m[1]
			if strings.EqualFold(l.ID[:2], "bv") {
				l.ID = "BV" + l.ID[2:]
			} else {
				l.ID = "av" + l.ID[2:]
			}
			path = "/video/" + l.ID
		} else {
			// 路径整段匹配，避免把带额外尾巴的页面认作同一内容。
			parts := strings.Split(strings.Trim(path, "/"), "/")
			switch {
			case len(parts) == 2 && (parts[0] == "dynamic" || parts[0] == "opus") && digits(parts[1]):
				l.Kind, l.ID = parts[0], parts[1]
				if l.Kind == "dynamic" {
					host, path = "t.bilibili.com", "/"+l.ID
				} else {
					path = "/opus/" + l.ID
				}
			case len(parts) == 2 && parts[0] == "read" && strings.HasPrefix(parts[1], "cv") && digits(parts[1][2:]):
				l.Kind, l.ID, path = "article", parts[1], "/read/"+parts[1]
			case len(parts) == 3 && parts[0] == "bangumi" && parts[1] == "play" && len(parts[2]) > 2 && digits(parts[2][2:]) && (parts[2][:2] == "ep" || parts[2][:2] == "ss"):
				l.Kind, l.ID = "episode", parts[2]
				if parts[2][:2] == "ss" {
					l.Kind = "season"
				}
				path = "/bangumi/play/" + l.ID
			default:
				return Link{}, false
			}
		}
	case "t.bilibili.com", "live.bilibili.com":
		m := numberPath.FindStringSubmatch(path)
		if m == nil {
			return Link{}, false
		}
		l.Platform, l.Kind, l.ID = "bilibili", "dynamic", m[1]
		if host == "live.bilibili.com" {
			l.Kind = "live"
		}
		path = "/" + l.ID
	default:
		return recognizeMore(u, raw)
	}
	query, ok := stripTracking(u.Hostname(), u.EscapedPath(), u.RawQuery)
	if !ok {
		return Link{}, false
	}
	u.Scheme, u.Host, u.Path, u.RawPath = "https", host, path, ""
	u.RawQuery, u.ForceQuery = query, false
	l.Clean = u.String()
	return l, true
}

// xStatus 从路径里取出账号和推文 ID；无账号的形态（/i/web/status、/statuses）账号记成 i，和 /i/status 一样。
func xStatus(path string) (account, id string, ok bool) {
	if m := xPath.FindStringSubmatch(path); m != nil {
		account = m[1]
		if account == "i/web" {
			account = "i"
		}
		return account, m[2], true
	}
	if m := xOldPath.FindStringSubmatch(path); m != nil {
		account = m[1]
		if account == "" {
			account = "i"
		}
		return account, m[2], true
	}
	return "", "", false
}

// Clean 返回干净链接；不支持或非法输入返回空串。
func Clean(raw string) string {
	l, ok := Recognize(raw)
	if !ok {
		return ""
	}
	return l.Clean
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// 只折叠地址部分；查询值和片段可能是签名，不能做 Unicode 清理。
func foldWidth(s string) string {
	end := strings.IndexAny(s, "?#？＃")
	if end < 0 {
		end = len(s)
	} else if s[end] < 0x80 {
		end++
	} else {
		end += 3 // 全角问号、井号都是三个 UTF-8 字节。
	}
	head := strings.Map(func(r rune) rune {
		if r >= '！' && r <= '～' {
			return r - 0xfee0
		}
		return r
	}, s[:end])
	if end == len(s) {
		return head
	}
	if head == s[:end] {
		return s
	}
	return head + s[end:]
}
