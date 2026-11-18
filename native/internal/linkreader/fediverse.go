package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

// Misskey 与 Mastodon 只认下面两个写死的实例；同款软件的其他站不是 fediverse 服务器，不在这里识别。
// 远程用户的 @user@domain 形式拒绝，不改写成本站路径。
var (
	misskeyNoteID   = regexp.MustCompile(`^[a-z0-9]{6,32}$`)
	misskeyUserName = regexp.MustCompile(`^[A-Za-z0-9_]{1,20}$`)
	mastodonUser    = regexp.MustCompile(`^[A-Za-z0-9_]{1,30}$`)
)

// recognizeFediverse 只在 recognizeMore 的主机分支中调用，主机已由调用方转成小写。
func recognizeFediverse(u *url.URL, raw string) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	// 用编码形式切段：解码后的 %2F 会变成分隔符。路径里的合法字符都是 ASCII 且不含 %，含 % 的一律拒绝。
	path := strings.TrimSuffix(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if strings.Contains(path, "%") {
		return Link{}, false
	}
	parts := strings.Split(path, "/")
	l := Link{Raw: raw}
	switch host {
	case "misskey.io":
		switch {
		case len(parts) == 2 && parts[0] == "notes" && misskeyNoteID.MatchString(parts[1]):
			l.Platform, l.Kind, l.ID = "misskey", "note", "misskey.io/"+parts[1]
			path = "/notes/" + parts[1]
		case len(parts) == 1 && strings.HasPrefix(parts[0], "@") && misskeyUserName.MatchString(parts[0][1:]):
			name := parts[0][1:]
			l.Platform, l.Kind, l.ID = "misskey", "user", "misskey.io/"+strings.ToLower(name)
			path = "/@" + name
		default:
			return Link{}, false
		}
	case "mastodon.social":
		// 网页版带 /web 前缀，ActivityPub 形式是 /users/<名>/statuses/<id>，都归一为 /@名/<id>。
		if len(parts) > 0 && parts[0] == "web" {
			parts = parts[1:]
		}
		var name, id string
		switch {
		case len(parts) == 2 && strings.HasPrefix(parts[0], "@"):
			name, id = parts[0][1:], parts[1]
		case len(parts) == 4 && parts[0] == "users" && parts[2] == "statuses":
			name, id = parts[1], parts[3]
		case len(parts) == 1 && strings.HasPrefix(parts[0], "@"):
			name = parts[0][1:]
		default:
			return Link{}, false
		}
		if !mastodonUser.MatchString(name) {
			return Link{}, false
		}
		if id == "" {
			l.Platform, l.Kind, l.ID = "mastodon", "account", "mastodon.social/"+strings.ToLower(name)
			path = "/@" + name
		} else {
			if !positiveID(id) {
				return Link{}, false
			}
			l.Platform, l.Kind, l.ID = "mastodon", "status", "mastodon.social/"+id
			path = "/@" + name + "/" + id
		}
	default:
		return Link{}, false
	}
	query, ok := stripTracking(u.Hostname(), u.EscapedPath(), u.RawQuery)
	if !ok {
		return Link{}, false
	}
	clean := &url.URL{Scheme: "https", Host: host, Path: path, RawQuery: query, Fragment: u.Fragment, RawFragment: u.RawFragment}
	l.Clean = clean.String()
	return l, true
}
