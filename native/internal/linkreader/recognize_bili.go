package linkreader

import (
	"net/url"
	"strings"
)

// B 站的页面形态很多：这里放 recognize.go 主流程之外的那批（专栏、番剧媒体页、课程、音频、合集、小店商品、
// 活动页和播放器嵌入页、手机端和旧域名跳转、b23.tv 里不会跳转的短码）。形态来源和实测见
// docs/规格-链接页面类型和样式.md 第九节。能直接算出正式页面的，不再多发一次请求去追跳转。
//
// 返回值：handled 说这个主机和路径归这里管（无论认不认得出），false 交回主流程；ok 说认得出。
// 认得出的链接 Clean 都是这里拼出来的正式写法，不带追踪参数，也不带无关查询。

func biliLink(kind, id, clean, raw string) Link {
	return Link{Platform: "bilibili", Kind: kind, ID: id, Clean: clean, Raw: raw}
}

// prefixedID 认 "cv123"、"ep123" 这种“两个小写字母加数字”的编号，返回数字部分。
func prefixedID(s, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok || !positiveID(rest) || len(rest) > 20 {
		return "", false
	}
	return rest, true
}

// queryOnly 解析查询串；解析不了（非法转义）就当没认出来。
func queryOnly(u *url.URL) (url.Values, bool) {
	q, err := url.ParseQuery(u.RawQuery)
	return q, err == nil
}

func recognizeBiliExtra(host string, u *url.URL, raw string) (l Link, ok, handled bool) {
	path := u.EscapedPath()
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch host {
	case "b23.tv", "bili2233.cn":
		// b23.tv/cv123、/md123 不会跳转（返回 200 页面），ep、ss 能跳但没必要多走一次：都直接换成正式页面。
		if len(parts) != 1 {
			return Link{}, false, false
		}
		for _, p := range []struct{ prefix, kind string }{{"cv", "article"}, {"md", "media"}, {"ep", "episode"}, {"ss", "season"}} {
			if n, found := prefixedID(parts[0], p.prefix); found {
				return withLocator(biliPage(p.kind, p.prefix+n, raw))(biliLocator(u, "t"))
			}
		}
	case "bilibili.com", "www.bilibili.com", "m.bilibili.com", "mobile.bilibili.com":
		switch {
		case len(parts) >= 2 && parts[0] == "toy":
			// 小游戏页：/toy/<slug>/index.html，index.html 可省；查询串（spm_id_from 之类）一概不留。
			// slug 对不对要取数时核对（外壳页对乱接的路径也回 200），这里只管写法。
			if len(parts) <= 3 && toySlug.MatchString(parts[1]) && (len(parts) == 2 || parts[2] == "index.html") {
				return biliLink("toy", parts[1], "https://www.bilibili.com/toy/"+parts[1]+"/index.html", raw), true, true
			}
			return Link{}, false, true
		case len(parts) == 3 && parts[0] == "read" && parts[1] == "mobile":
			if !digits(parts[2]) {
				return Link{}, false, true
			}
			return biliPage("article", "cv"+parts[2], raw)
		case len(parts) == 2 && parts[0] == "read" && parts[1] == "mobile":
			q, good := queryOnly(u)
			if ids := q["id"]; good && len(ids) == 1 && digits(ids[0]) {
				return biliPage("article", "cv"+ids[0], raw)
			}
			return Link{}, false, true
		case len(parts) == 3 && parts[0] == "bangumi" && parts[1] == "media":
			if n, found := prefixedID(parts[2], "md"); found {
				return biliPage("media", "md"+n, raw)
			}
			return Link{}, false, true
		case len(parts) == 3 && parts[0] == "cheese" && parts[1] == "play":
			for _, prefix := range []string{"ss", "ep"} {
				if n, found := prefixedID(parts[2], prefix); found {
					return biliPage("course", prefix+n, raw)
				}
			}
			return Link{}, false, true
		case len(parts) == 2 && parts[0] == "audio":
			if n, found := prefixedID(parts[1], "au"); found {
				return biliPage("audio", "au"+n, raw)
			}
			return Link{}, false, true
		case len(parts) == 2 && parts[0] == "festival":
			// 活动页里的视频：只抽 bvid，换成普通视频链接。
			q, good := queryOnly(u)
			if ids := q["bvid"]; good && len(ids) == 1 {
				return withLocator(biliVideoLink(ids[0], raw))(biliLocator(u, "p", "t", "start_progress"))
			}
			return Link{}, false, true
		case len(parts) == 2 && parts[0] == "space" && host == "m.bilibili.com" && digits(parts[1]):
			return biliPage("user", parts[1], raw)
		}
	case "player.bilibili.com":
		if path != "/player.html" {
			return Link{}, false, true
		}
		q, good := queryOnly(u)
		if !good {
			return Link{}, false, true
		}
		if ids := q["bvid"]; len(ids) == 1 {
			return withLocator(biliVideoLink(ids[0], raw))(biliLocator(u, "p:page", "t"))
		}
		if ids := q["aid"]; len(ids) == 1 && positiveID(ids[0]) {
			return withLocator(biliVideoLink("av"+ids[0], raw))(biliLocator(u, "p:page", "t"))
		}
		return Link{}, false, true
	case "bangumi.bilibili.com":
		// 旧域名：anime 后面的号就是 md 号（实测 301 到 bangumi/media/md 同一个数）。
		if len(parts) == 2 && parts[0] == "anime" && positiveID(parts[1]) {
			return biliPage("media", "md"+parts[1], raw)
		}
		return Link{}, false, true
	case "gf.bilibili.com":
		if len(parts) == 3 && parts[0] == "item" && parts[1] == "detail" && positiveID(parts[2]) {
			return biliWorkshop(parts[2], raw)
		}
		return Link{}, false, true
	case "show.bilibili.com":
		// 会员购和小店的商品详情共用这个入口；取数时先查工房，没有再查会员购（fetchWorkshop）。
		if path == "/platform/malldetail.html" {
			q, good := queryOnly(u)
			if ids := q["itemsId"]; good && len(ids) == 1 && positiveID(ids[0]) && len(ids[0]) <= 20 {
				return biliWorkshop(ids[0], raw)
			}
		}
		return Link{}, false, true
	case "live.bilibili.com":
		if len(parts) == 2 && (parts[0] == "blanc" || parts[0] == "h5") && digits(parts[1]) {
			return biliPage("live", parts[1], raw)
		}
	case "space.bilibili.com":
		return recognizeBiliSpace(u, parts, raw)
	}
	return Link{}, false, false
}

func biliWorkshop(id, raw string) (Link, bool, bool) {
	return biliLink("workshop", id, "https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail&itemsId="+id, raw), true, true
}

// biliVideoLink 把 BV 号或 av 号换成普通视频链接。
func biliVideoLink(id, raw string) (Link, bool, bool) {
	m := videoPath.FindStringSubmatch("/" + id)
	if m == nil {
		return Link{}, false, true
	}
	id = m[1]
	if strings.EqualFold(id[:2], "bv") {
		id = "BV" + id[2:]
	} else {
		id = "av" + id[2:]
	}
	return biliLink("video", id, "https://www.bilibili.com/video/"+id, raw), true, true
}

// biliPage 按种类拼正式页面。
func biliPage(kind, id, raw string) (Link, bool, bool) {
	var clean string
	switch kind {
	case "article":
		clean = "https://www.bilibili.com/read/" + id
	case "media":
		clean = "https://www.bilibili.com/bangumi/media/" + id
	case "episode", "season":
		clean = "https://www.bilibili.com/bangumi/play/" + id
	case "course":
		clean = "https://www.bilibili.com/cheese/play/" + id
	case "audio":
		clean = "https://www.bilibili.com/audio/" + id
	case "live":
		clean = "https://live.bilibili.com/" + id
	case "user":
		clean = "https://space.bilibili.com/" + id
	default:
		return Link{}, false, true
	}
	return biliLink(kind, id, clean, raw), true, true
}

// space.bilibili.com：合集和系列、个人主页的各个子页。
//
//	/<mid>/lists/<sid>?type=season|series（缺省按合集）、/<mid>/channel/collectiondetail?sid=、/<mid>/channel/seriesdetail?sid=
//	/<mid>、/<mid>/video|dynamic|audio|album|article|bangumi|opus|lists：都归到个人主页（名片）
//
// 关注、粉丝、收藏夹、充电榜是别人的关系和个人记录，不认。
func recognizeBiliSpace(u *url.URL, parts []string, raw string) (Link, bool, bool) {
	if len(parts) == 0 || !digits(parts[0]) || len(parts[0]) > 20 {
		return Link{}, false, true
	}
	mid := parts[0]
	collection := func(sid, typ string) (Link, bool, bool) {
		if !positiveID(sid) || len(sid) > 20 {
			return Link{}, false, true
		}
		return biliLink("collection", typ+":"+mid+":"+sid, "https://space.bilibili.com/"+mid+"/lists/"+sid+"?type="+typ, raw), true, true
	}
	q, good := queryOnly(u)
	if !good {
		return Link{}, false, true
	}
	switch {
	case len(parts) == 3 && parts[1] == "lists":
		typ := "season"
		if t := q["type"]; len(t) > 1 || (len(t) == 1 && t[0] != "season" && t[0] != "series") {
			return Link{}, false, true
		} else if len(t) == 1 {
			typ = t[0]
		}
		return collection(parts[2], typ)
	case len(parts) == 3 && parts[1] == "channel" && (parts[2] == "collectiondetail" || parts[2] == "seriesdetail"):
		sid := q["sid"]
		if len(sid) != 1 {
			return Link{}, false, true
		}
		return collection(sid[0], map[string]string{"collectiondetail": "season", "seriesdetail": "series"}[parts[2]])
	case len(parts) == 1:
		return biliPage("user", mid, raw)
	case len(parts) == 2:
		switch parts[1] {
		case "dynamic", "video", "audio", "album", "article", "bangumi", "opus", "lists":
			return biliPage("user", mid, raw)
		}
	}
	return Link{}, false, true
}
