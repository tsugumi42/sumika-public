package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

// 形状见规格-链接解析 4.13：语言前缀和 embed 前缀可选，ID 固定 22 位。
var spotifyPath = regexp.MustCompile(`^/(?:embed/)?(?:intl-[a-z]{2}(?:-[a-z0-9]{2,8})?/)?(track|album|playlist|artist|episode|show)/([A-Za-z0-9]{22})/?$`)

// recognizeSpotify 只接受固定路径形状；show 能识别，但 Fetch 不取数。
func recognizeSpotify(u *url.URL, raw string) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	l := Link{Raw: raw, Platform: "spotify"}
	path := u.EscapedPath()
	if host == "spotify.link" {
		if !shortPath.MatchString(path) {
			return Link{}, false
		}
		l.Kind = "short"
	} else {
		m := spotifyPath.FindStringSubmatch(path)
		if m == nil {
			return Link{}, false
		}
		l.Kind, l.ID = m[1], m[2]
		host, path = "open.spotify.com", "/"+l.Kind+"/"+l.ID
	}
	query, ok := stripTracking(host, u.EscapedPath(), u.RawQuery)
	if !ok {
		return Link{}, false
	}
	u.Scheme, u.Host, u.Path, u.RawPath = "https", host, path, ""
	u.RawQuery, u.ForceQuery = query, false
	l.Clean = u.String()
	return l, true
}
