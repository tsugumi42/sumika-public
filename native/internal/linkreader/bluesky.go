package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

// Bluesky 只认 bsky.app 的单帖与主页，路由整段匹配，不做前缀或包含判断。

var (
	bskyHandle = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	bskyDID    = regexp.MustCompile(`^did:(plc:[a-z2-7]{24}|web:[A-Za-z0-9.-]{1,253})$`)
	bskyRKey   = regexp.MustCompile(`^[2-7a-z]{13}$`)
)

// bskyActor 把 handle 归一为小写；DID 原样保留，因为 DID 区分大小写。
func bskyActor(s string) (string, bool) {
	if strings.HasPrefix(s, "did:") {
		return s, bskyDID.MatchString(s)
	}
	h := strings.ToLower(s)
	return h, len(h) <= 253 && bskyHandle.MatchString(h)
}

// recognizeBluesky 只在 host 精确匹配后调用；尾段必须整段匹配，不做前缀或包含判断。
func recognizeBluesky(u *url.URL, raw string) (Link, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSuffix(u.Path, "/"), "/"), "/")
	if len(parts) < 2 || parts[0] != "profile" {
		return Link{}, false
	}
	actor, ok := bskyActor(parts[1])
	if !ok {
		return Link{}, false
	}
	l := Link{Platform: "bluesky", Raw: raw}
	path := "/profile/" + actor
	switch {
	case len(parts) == 2:
		l.Kind, l.ID = "profile", actor
	case len(parts) == 4 && parts[2] == "post" && bskyRKey.MatchString(parts[3]):
		l.Kind, l.ID = "post", actor+"/"+parts[3]
		path += "/post/" + parts[3]
	default:
		return Link{}, false
	}
	query, ok := stripTracking("bsky.app", u.EscapedPath(), u.RawQuery)
	if !ok {
		return Link{}, false
	}
	u.Scheme, u.Host, u.Path, u.RawPath = "https", "bsky.app", path, ""
	u.RawQuery, u.ForceQuery = query, false
	l.Clean = u.String()
	return l, true
}
