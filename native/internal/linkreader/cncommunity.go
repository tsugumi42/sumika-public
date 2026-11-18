package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

// 小黑盒和小红书的纯函数识别，形状见规格 4.15、4.16。
var (
	heyboxPostID = regexp.MustCompile(`^(?:[0-9]+|[0-9a-fA-F]{12})$`)
	xhsNoteID    = regexp.MustCompile(`^[0-9a-fA-F]{24}$`)
	xhsShortCode = regexp.MustCompile(`^/o/[A-Za-z0-9]+/?$`)
)

// recognizeCN 只识别规格列出的形状；其余路径一律返回 false，不猜 ID。
func recognizeCN(u *url.URL, raw string) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	path := strings.TrimSuffix(u.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	l := Link{Raw: raw}
	switch host {
	case "xiaoheihe.cn", "www.xiaoheihe.cn", "api.xiaoheihe.cn":
		l.Platform = "xiaoheihe"
		switch {
		case len(parts) == 5 && parts[0] == "app" && parts[1] == "topic" && parts[2] == "game" && parts[3] == "pc" && positiveID(parts[4]):
			l.Kind, l.ID = "game", parts[4]
			path = "/app/topic/game/pc/" + l.ID
		case path == "/game/share_game_detail" && positiveID(uniqueQuery(u, "appid")):
			l.Kind, l.ID = "game", uniqueQuery(u, "appid")
			path = "/app/topic/game/pc/" + l.ID
		case len(parts) == 4 && parts[0] == "app" && parts[1] == "bbs" && parts[2] == "link" && heyboxPostID.MatchString(parts[3]):
			l.Kind, l.ID = "post", parts[3]
			path = "/app/bbs/link/" + l.ID
		case path == "/bbs/post_share" && heyboxPostID.MatchString(uniqueQuery(u, "link_id")):
			l.Kind, l.ID = "post", uniqueQuery(u, "link_id")
			path = "/app/bbs/link/" + l.ID
		case host == "api.xiaoheihe.cn" && path == "/v3/bbs/app/api/web/share" && heyboxPostID.MatchString(uniqueQuery(u, "link_id")):
			l.Kind, l.ID = "post", uniqueQuery(u, "link_id")
			path = "/app/bbs/link/" + l.ID
		case len(parts) == 4 && parts[0] == "app" && parts[1] == "user" && parts[2] == "profile" && positiveID(parts[3]):
			l.Kind, l.ID = "user", parts[3]
			path = "/app/user/profile/" + l.ID
		default:
			return Link{}, false
		}
		host = "www.xiaoheihe.cn"
	case "xiaohongshu.com", "www.xiaohongshu.com":
		l.Platform = "xiaohongshu"
		switch {
		case len(parts) == 2 && parts[0] == "explore" && xhsNoteID.MatchString(parts[1]):
			l.Kind, l.ID = "note", strings.ToLower(parts[1])
		case len(parts) == 3 && parts[0] == "discovery" && parts[1] == "item" && xhsNoteID.MatchString(parts[2]):
			l.Kind, l.ID = "note", strings.ToLower(parts[2])
		case len(parts) == 3 && parts[0] == "user" && parts[1] == "profile" && xhsNoteID.MatchString(parts[2]):
			l.Kind, l.ID = "user", strings.ToLower(parts[2])
			path = "/user/profile/" + l.ID
		default:
			return Link{}, false
		}
		if l.Kind == "note" {
			path = "/explore/" + l.ID
		}
		host = "www.xiaohongshu.com"
	case "xhslink.com", "www.xhslink.com", "xhslink.cn", "www.xhslink.cn":
		if !xhsShortCode.MatchString(u.Path) {
			return Link{}, false
		}
		l.Platform, l.Kind = "xiaohongshu", "short"
		path = strings.TrimSuffix(u.Path, "/")
		host = strings.TrimPrefix(host, "www.")
	default:
		return Link{}, false
	}
	query, ok := stripTracking(u.Hostname(), u.EscapedPath(), u.RawQuery)
	if !ok {
		return Link{}, false
	}
	if l.Platform == "xiaoheihe" {
		// 小黑盒的身份全在路径里；分享用的查询参数（appid、link_id）已经折进路径，不保留。
		query = ""
	}
	u.Scheme, u.Host, u.Path, u.RawPath = "https", host, path, ""
	u.RawQuery, u.ForceQuery = query, false
	l.Clean = u.String()
	return l, true
}
