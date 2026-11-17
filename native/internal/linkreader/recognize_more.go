package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	githubOwner = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	githubRepo  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	gistID      = regexp.MustCompile(`^[a-fA-F0-9]{5,40}$`)
	youtubeID   = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
)

// 新平台单独放置，避免与同时补 X 的分支争用既有路径规则。
func recognizeMore(u *url.URL, raw string) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	path := strings.TrimSuffix(u.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	l := Link{Raw: raw}
	switch host {
	case "github.com", "www.github.com":
		if len(parts) < 2 || !githubOwner.MatchString(parts[0]) || !githubRepo.MatchString(parts[1]) {
			return Link{}, false
		}
		switch strings.ToLower(parts[0]) {
		case "apps", "collections", "enterprise", "features", "login", "marketplace", "organizations", "orgs", "search", "settings", "site", "sponsors", "topics", "users":
			return Link{}, false
		}
		repo := strings.TrimSuffix(parts[1], ".git")
		if repo == "" || repo == "." || repo == ".." {
			return Link{}, false
		}
		base := strings.ToLower(parts[0] + "/" + repo)
		l.Platform, l.Kind, l.ID = "github", "repository", base
		path = "/" + base
		switch {
		case len(parts) == 2:
		case len(parts) == 4 && (parts[2] == "issues" || parts[2] == "pull") && positiveID(parts[3]):
			l.Kind = "issue"
			if parts[2] == "pull" {
				l.Kind = "pull_request"
			}
			l.ID += "/" + parts[2] + "/" + parts[3]
			path = "/" + l.ID
		case len(parts) == 4 && parts[2] == "releases" && parts[3] == "latest":
			l.Kind, l.ID = "release", base+"/releases/latest"
			path = "/" + l.ID
		case len(parts) >= 5 && parts[2] == "releases" && parts[3] == "tag":
			tag := strings.Join(parts[4:], "/")
			if tag == "" || strings.Contains(tag, "\\") {
				return Link{}, false
			}
			l.Kind, l.ID = "release", base+"/releases/tag/"+tag
			path = "/" + l.ID
		case len(parts) == 3 && parts[2] == "releases":
			return Link{}, false
		default:
			return Link{}, false
		}
		// ID 和干净链接都是小写的（去重、缓存、核对身份都按 Clean，大小写不同的同一个仓库只取一次）；
		// 发出去的链接由 githubDisplay 换回用户写的大小写，不把 AstrBotDevs/AstrBot 改成全小写
		path = "/" + l.ID
		host = "github.com"
	case "gist.github.com":
		id := parts[len(parts)-1]
		if (len(parts) != 1 && len(parts) != 2) || !gistID.MatchString(id) || (len(parts) == 2 && !githubOwner.MatchString(parts[0])) {
			return Link{}, false
		}
		l.Platform, l.Kind, l.ID = "github", "gist", strings.ToLower(id)
		path = "/" + l.ID
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "youtu.be", "www.youtu.be", "youtube-nocookie.com", "www.youtube-nocookie.com":
		var id string
		switch {
		case host == "youtu.be" || host == "www.youtu.be":
			if len(parts) != 1 || len(u.Query()["v"]) != 0 {
				return Link{}, false
			}
			id = parts[0]
			u.RawQuery = "v=" + id + queryTail(u.RawQuery)
		case path == "/watch" && !strings.Contains(host, "nocookie"):
			id = uniqueQuery(u, "v")
		case len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live"):
			if strings.Contains(host, "nocookie") && parts[0] != "embed" {
				return Link{}, false
			}
			if len(u.Query()["v"]) != 0 {
				return Link{}, false
			}
			id = parts[1]
			u.RawQuery = "v=" + id + queryTail(u.RawQuery)
		default:
			return Link{}, false
		}
		if !youtubeID.MatchString(id) {
			return Link{}, false
		}
		l.Platform, l.Kind, l.ID = "youtube", "video", id
		host, path = "www.youtube.com", "/watch"
	case "v.douyin.com", "jx.douyin.com", "163cn.tv":
		if !shortPath.MatchString(u.EscapedPath()) {
			return Link{}, false
		}
		l.Platform, l.Kind = "douyin", "short"
		if host == "163cn.tv" {
			l.Platform = "netease"
		}
		path = u.Path
	case "douyin.com", "www.douyin.com", "m.douyin.com", "iesdouyin.com", "www.iesdouyin.com", "jingxuan.douyin.com":
		if len(parts) == 3 && (parts[0] == "share" || parts[0] == "m") {
			parts = parts[1:]
		}
		if len(parts) != 2 || (parts[0] != "video" && parts[0] != "note" && parts[0] != "slides") || !positiveID(parts[1]) {
			return Link{}, false
		}
		l.Platform, l.Kind, l.ID = "douyin", "video", parts[1]
		if parts[0] != "video" {
			l.Kind = "note"
		}
		host, path = "www.douyin.com", "/"+l.Kind+"/"+l.ID
	case "tiktok.com", "www.tiktok.com", "m.tiktok.com", "vm.tiktok.com", "vt.tiktok.com":
		return recognizeTikTok(u, raw)
	case "music.163.com", "y.music.163.com":
		if path == "" && strings.HasPrefix(u.Fragment, "/") {
			fragment, err := url.Parse(u.EscapedFragment())
			if err != nil || fragment.Host != "" || fragment.Fragment != "" {
				return Link{}, false
			}
			path = fragment.Path
			if fragment.RawQuery != "" {
				u.RawQuery += queryTail(fragment.RawQuery)
				u.RawQuery = strings.TrimPrefix(u.RawQuery, "&")
			}
			u.Fragment, u.RawFragment = "", ""
		}
		path = strings.TrimPrefix(path, "/m")
		if path != "/song" && path != "/playlist" && path != "/album" {
			return Link{}, false
		}
		id := uniqueQuery(u, "id")
		if !positiveID(id) {
			return Link{}, false
		}
		l.Platform, l.Kind, l.ID = "netease", strings.TrimPrefix(path, "/"), id
		host = "music.163.com"
	case "misskey.io", "mastodon.social":
		return recognizeFediverse(u, raw)
	case "open.spotify.com", "spotify.link":
		return recognizeSpotify(u, raw)
	case "pixiv.net", "www.pixiv.net", "touch.pixiv.net", "phixiv.net", "www.phixiv.net", "ppxiv.net", "www.ppxiv.net", "i.pximg.net", "pixiv.cat", "www.pixiv.cat":
		return recognizePixiv(u, raw)
	case "bsky.app", "www.bsky.app":
		return recognizeBluesky(u, raw)
	case "t.me", "telegram.me":
		return recognizeTelegram(u, raw)
	case "afdian.com", "www.afdian.com", "afdian.net", "www.afdian.net", "ifdian.net", "www.ifdian.net":
		// 爱发电的查询参数不参与内容选择，干净链接整体丢弃；转义的斜杠会改变段数，直接拒绝。
		if strings.Contains(u.EscapedPath(), "%") || !recognizeAfdian(parts, &l, &host, &path) {
			return Link{}, false
		}
		u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	default:
		// 剩下的主机依次问 BOOTH、Steam、小黑盒和小红书；各自按精确主机认，认不出返回 false。
		// Steam 拿副本：它认不出时不能留下改了一半的 u 给下一家。
		if !hostSuffix(host, "booth.pm") {
			su := *u
			if sl, ok := recognizeSteam(&su, raw); ok {
				return sl, true
			}
			return recognizeCN(u, raw)
		}
		lang, bid, ok := recognizeBooth(u, host)
		if !ok {
			return Link{}, false
		}
		l.Platform, l.Kind, l.ID = "booth", "item", bid
		host, path = "booth.pm", "/"+lang+"/items/"+bid
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

// githubDisplay 是 GitHub 链接发出去的写法：干净链接里小写的 owner/repo 换回用户原文里的大小写。
// 只换这一段（issue 号、tag 本来就照原文）；不是 GitHub 仓库类链接、原文找不到、本来就是小写时返回空串。
func githubDisplay(raw string, l Link) string {
	if l.Platform != "github" || l.Kind == "gist" {
		return ""
	}
	base := l.ID
	if i := strings.Index(base, "/"); i >= 0 {
		if j := strings.Index(base[i+1:], "/"); j >= 0 {
			base = base[:i+1+j]
		}
	}
	key := "github.com/" + base
	i := strings.Index(asciiLower(raw), key)
	if i < 0 {
		return ""
	}
	orig := raw[i+len("github.com/") : i+len(key)]
	if orig == base {
		return ""
	}
	return strings.Replace(l.Clean, key, "github.com/"+orig, 1)
}

// asciiLower 只把 ASCII 字母转小写，字节位置不变（strings.ToLower 遇到个别非 ASCII 字符会改变长度）
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func positiveID(s string) bool { return digits(s) && s[0] != '0' }
func uniqueQuery(u *url.URL, key string) string {
	values := u.Query()[key]
	if len(values) != 1 {
		return ""
	}
	return values[0]
}
func queryTail(s string) string {
	if s == "" {
		return ""
	}
	return "&" + s
}
func dropQueryKeys(raw string, keys ...string) (string, bool) {
	var out []string
	for part := range strings.SplitSeq(raw, "&") {
		key, value, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(key)
		if err != nil {
			return "", false
		}
		if _, err := url.QueryUnescape(value); err != nil {
			return "", false
		}
		drop := false
		for _, k := range keys {
			if key == k {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, part)
		}
	}
	return strings.Join(out, "&"), true
}
