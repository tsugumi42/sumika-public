package linkreader

import (
	"net/url"
	"regexp"
	"strings"
)

// pixiv 的网址形态：正站、手机站（touch）、嵌入修复站（phixiv、ppxiv）、图片直链（i.pximg.net、pixiv.cat）、
// 旧式 member_illust.php、小说短链 /n/ID，以及两种系列页。识别后一律改写成 www.pixiv.net 的干净链接。
var (
	// 图片直链：.../img/年/月/日/时/分/秒/<作品ID>_p<页>...（也认 _ugoira<帧>）。
	pixivImagePath = regexp.MustCompile(`^/(?:c/[^/]+/)?img-(?:original|master)/img/\d{4}/\d{2}/\d{2}/\d{2}/\d{2}/\d{2}/([0-9]+)_(?:p|ugoira)[0-9]+`)
	// pixiv.cat 是图片反代：/ID.png、/ID-2.jpg。
	pixivCatPath = regexp.MustCompile(`^/([0-9]+)(?:-[0-9]+)?\.(?:png|jpe?g|gif|webp)$`)
)

// pixivIDMax 是作品和系列编号的位数上限；超过的不是真编号，不白打一次请求。
const pixivIDMax = 12

func pixivID(s string) bool { return positiveID(s) && len(s) <= pixivIDMax }

func recognizePixiv(u *url.URL, raw string) (Link, bool) {
	host := strings.ToLower(u.Hostname())
	path := strings.TrimSuffix(u.Path, "/")
	l := Link{Raw: raw}
	mirror := host != "pixiv.net" && host != "www.pixiv.net"
	var id, uid string
	kind := "artwork"
	dropQuery, short := false, false
	switch host {
	case "i.pximg.net":
		m := pixivImagePath.FindStringSubmatch(path)
		if m == nil {
			return Link{}, false
		}
		id, dropQuery = m[1], true
	case "pixiv.cat", "www.pixiv.cat":
		m := pixivCatPath.FindStringSubmatch(path)
		if m == nil {
			return Link{}, false
		}
		id, dropQuery = m[1], true
	default:
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(parts) >= 2 {
			switch parts[0] {
			case "en", "ja", "zh", "zh_tw", "ko":
				parts = parts[1:]
			}
		}
		switch {
		case len(parts) == 2 && (parts[0] == "artworks" || parts[0] == "i"):
			id = parts[1]
		case len(parts) == 3 && parts[0] == "artworks" && mirror && digits(parts[2]):
			// phixiv 的 /artworks/ID/页码：只认作品，不选页。
			id = parts[1]
		case len(parts) == 1 && parts[0] == "member_illust.php":
			switch uniqueQuery(u, "mode") {
			case "medium", "manga", "manga_big":
				id = uniqueQuery(u, "illust_id")
			}
		case len(parts) == 2 && parts[0] == "novel" && parts[1] == "show.php":
			id, kind = uniqueQuery(u, "id"), "novel"
		case len(parts) == 2 && parts[0] == "n":
			id, kind, short = parts[1], "novel", true
		case len(parts) == 4 && parts[0] == "user" && positiveID(parts[1]) && parts[2] == "series":
			id, kind, uid = parts[3], "series", parts[1]
		case len(parts) == 3 && parts[0] == "novel" && parts[1] == "series":
			id, kind = parts[2], "novel_series"
		}
	}
	if !pixivID(id) {
		return Link{}, false
	}
	l.Platform, l.Kind, l.ID = "pixiv", kind, id
	var cleanPath string
	switch kind {
	case "novel":
		cleanPath = "/novel/show.php"
		if short {
			// 短链没有 id 参数：补成标准的 show.php?id=。
			u.RawQuery = "id=" + id + queryTail(u.RawQuery)
		}
	case "series":
		cleanPath = "/user/" + uid + "/series/" + id
	case "novel_series":
		cleanPath = "/novel/series/" + id
	default:
		cleanPath = "/artworks/" + id
	}
	if dropQuery {
		u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	}
	if strings.HasSuffix(path, "member_illust.php") {
		var ok bool
		u.RawQuery, ok = dropQueryKeys(u.RawQuery, "mode", "illust_id", "page")
		if !ok {
			return Link{}, false
		}
	}
	query, ok := stripTracking(u.Hostname(), u.EscapedPath(), u.RawQuery)
	if !ok {
		return Link{}, false
	}
	u.Scheme, u.Host, u.Path, u.RawPath = "https", "www.pixiv.net", cleanPath, ""
	u.RawQuery, u.ForceQuery = query, false
	l.Clean = u.String()
	return l, true
}
