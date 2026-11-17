package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	steamVanity = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)
	// SteamID64 是 17 位十进制，个人账号都以 765611 开头；只做形状检查，不推断账号类型。
	steamID64 = regexp.MustCompile(`^765611[0-9]{11}$`)
	// 商店里的子页；带这些名字的 /app/<id>/<名字> 不是游戏页，拒绝而不是归一成游戏页。
	steamSubpages = map[string]bool{
		"reviews": true, "dlc": true, "achievements": true, "discussions": true, "announcements": true,
		"videos": true, "screenshots": true, "workshop": true, "news": true, "guides": true,
		"recommendations": true, "related": true, "bundles": true, "curators": true, "partners": true,
	}
)

// SteamAppLink 供别的平台复用（小黑盒游戏页用的就是 Steam AppID）：只接受正整数，返回规范的应用链接；
// 之后调用 Fetcher.Fetch 走同一条匿名取数路径。
func SteamAppLink(appid string) (Link, bool) {
	if !positiveID(appid) {
		return Link{}, false
	}
	return Recognize("https://store.steampowered.com/app/" + appid)
}

// recognizeSteam 只处理 store.steampowered.com 与 steamcommunity.com 的精确主机。
// 路径关键字只认小写，大写路径按不认识处理；slug 只用来识别，规范化后不保留。
func recognizeSteam(u *url.URL, raw string) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	path := strings.TrimPrefix(u.Path, "/")
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	switch host {
	case "store.steampowered.com":
		return recognizeSteamStore(u, raw, parts)
	case "steamcommunity.com", "www.steamcommunity.com":
		return recognizeSteamCommunity(u, raw, parts)
	}
	return Link{}, false
}

func recognizeSteamStore(u *url.URL, raw string, parts []string) (Link, bool) {
	if len(parts) > 0 && parts[0] == "agecheck" {
		parts = parts[1:]
		if len(parts) == 0 || parts[0] != "app" {
			return Link{}, false
		}
	}
	if len(parts) != 2 && len(parts) != 3 {
		return Link{}, false
	}
	kind := ""
	switch parts[0] {
	case "app":
		kind = "app"
	case "sub":
		kind = "package"
	default:
		return Link{}, false
	}
	if !positiveID(parts[1]) {
		return Link{}, false
	}
	if len(parts) == 3 && (parts[2] == "" || steamSubpages[strings.ToLower(parts[2])]) {
		return Link{}, false
	}
	seg := "app"
	if kind == "package" {
		seg = "sub"
	}
	return steamLink(u, raw, kind, parts[1], "store.steampowered.com", "/"+seg+"/"+parts[1])
}

func recognizeSteamCommunity(u *url.URL, raw string, parts []string) (Link, bool) {
	switch {
	case len(parts) == 2 && (parts[0] == "sharedfiles" || parts[0] == "workshop") && parts[1] == "filedetails":
		id := uniqueQuery(u, "id")
		if !positiveID(id) {
			return Link{}, false
		}
		return steamLink(u, raw, "sharedfile", id, "steamcommunity.com", "/sharedfiles/filedetails/")
	case len(parts) == 2 && parts[0] == "id" && steamVanity.MatchString(parts[1]):
		name := strings.ToLower(parts[1])
		return steamLink(u, raw, "profile", "id/"+name, "steamcommunity.com", "/id/"+name)
	case len(parts) == 2 && parts[0] == "profiles" && steamID64.MatchString(parts[1]):
		return steamLink(u, raw, "profile", "profiles/"+parts[1], "steamcommunity.com", "/profiles/"+parts[1])
	}
	return Link{}, false
}

// steamLink 统一做追踪清洗与规范主机、路径；查询里的未知参数照旧保留。
func steamLink(u *url.URL, raw, kind, id, host, path string) (Link, bool) {
	query, ok := stripTracking(host, u.EscapedPath(), u.RawQuery)
	if !ok {
		return Link{}, false
	}
	u.Scheme, u.Host, u.Path, u.RawPath = "https", host, path, ""
	u.RawQuery, u.ForceQuery = query, false
	return Link{Platform: "steam", Kind: kind, ID: id, Clean: u.String(), Raw: raw}, true
}
