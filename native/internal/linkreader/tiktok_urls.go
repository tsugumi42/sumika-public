package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

// 形状依据：yt-dlp tiktok.py:709、1020、1512 与 gallery-dl tiktok.py:157-158（/photo/ 改写为 /video/）。
// 用户名 [\w.-]{1,64}；语言前缀和其他页面未见实测样本，一律拒绝。
var (
	tiktokContent = regexp.MustCompile(`^/@([A-Za-z0-9_.-]{1,64})/(?:video|photo)/([0-9]+)/?$`)
	tiktokProfile = regexp.MustCompile(`^/@([A-Za-z0-9_.-]{1,64})/?$`)
	tiktokShortT  = regexp.MustCompile(`^/t/([A-Za-z0-9_-]+)/?$`)
)

// recognizeTikTok 的 Clean 统一为 https://www.tiktok.com/@<用户>/video/<ID> 或 /@<用户>；
// 视频不由路径用户名决定身份（TikTok 按数字 ID 服务），用户名只进入 Clean 和缓存键。
func recognizeTikTok(u *url.URL, raw string) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()
	l := Link{Raw: raw, Platform: "tiktok"}
	switch {
	case host == "vm.tiktok.com" || host == "vt.tiktok.com":
		if !shortPath.MatchString(path) {
			return Link{}, false
		}
		l.Kind = "short"
	case host == "www.tiktok.com" && tiktokShortT.MatchString(path):
		l.Kind = "short"
	case host == "tiktok.com" || host == "www.tiktok.com" || host == "m.tiktok.com":
		if m := tiktokContent.FindStringSubmatch(path); m != nil {
			if !positiveID(m[2]) {
				return Link{}, false
			}
			l.Kind, l.ID = "video", m[2]
			path = "/@" + strings.ToLower(m[1]) + "/video/" + m[2]
		} else if m := tiktokProfile.FindStringSubmatch(path); m != nil {
			l.Kind, l.ID = "user", strings.ToLower(m[1])
			path = "/@" + l.ID
		} else {
			return Link{}, false
		}
		host = "www.tiktok.com"
	default:
		return Link{}, false
	}
	query, ok := stripTracking(u.Hostname(), u.EscapedPath(), u.RawQuery)
	if !ok {
		return Link{}, false
	}
	if l.Kind == "short" {
		// 短链保留原主机和路径；终点由 Resolve 逐跳重认。
		path = u.EscapedPath()
	}
	u.Scheme, u.Host, u.Path, u.RawPath = "https", host, path, ""
	u.RawQuery, u.ForceQuery = query, false
	l.Clean = u.String()
	return l, true
}
