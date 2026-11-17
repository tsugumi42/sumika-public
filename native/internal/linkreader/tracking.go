package linkreader

import (
	"net/url"
	"strings"
)

// 清链接的三层规则，优先级从高到低（ADR-214：表外参数一律保留，不做白名单）：
//  1. 保留表 isLocator：时间点、分 P、播放列表位置，任何追踪表都不能删它们；
//  2. 追踪表：通用表 commonTracking 加各平台的表（X、B 站在本文件，其余在 tracking_more.go）；
//  3. 表外的一切参数、重复值、空值、次序和编码，照原样留下。
//
// 每个参数的出处写在表里；标“ClearURLs”的取自其公开规则库 rules2.clearurls.xyz/data.minify.json
// （2026-11-09 取回核对），标“实样”的见 docs/规格-链接解析.md 清链接一节。
var commonTracking = map[string]struct{}{
	"fbclid": {}, // Facebook 点击标识。
	"gclid":  {}, // Google 点击标识。
	"gclsrc": {}, "dclid": {}, "gbraid": {}, "wbraid": {}, "msclkid": {},
	"yclid": {}, "twclid": {}, "ttclid": {}, "li_fat_id": {}, "mc_eid": {}, "mc_cid": {},
	"_hsenc": {}, "_hsmi": {}, "mkt_tok": {}, "igshid": {},
	// 下面几项是各家广告和邮件系统的通用点击标识，参数名带平台前缀，不会和内容参数撞名。
	"igsh": {}, "mibextid": {}, "gad_source": {}, "gad_campaignid": {}, "_ga": {}, "_gl": {},
	"vero_id": {}, "vero_conv": {}, "oly_enc_id": {}, "oly_anon_id": {}, "_openstat": {},
}

// 保留表：这些参数决定“看哪一段、哪一集、哪一项”，不是追踪。
// t 是时间点（YouTube、B 站、Spotify 等），只有 X 把 t 当分享追踪，由 isTracking 里的 xHost 单独排除。
var locatorKeys = map[string]struct{}{
	"t": {}, "start": {}, "end": {}, "time_continue": {}, // YouTube：t、start、time_continue、end 是时间点。
	"start_progress": {},                                     // B 站：从第几毫秒开始。
	"p":              {}, "index": {}, "list": {}, "lid": {}, // B 站分 P；YouTube 播放列表和序号；lid 是列表定位。
}

// X 的 s、t 是分享追踪（t 是随机串，不是时间点），只在 X 的 status 和 t.co 上删。
var xTracking = map[string]struct{}{
	"a":       {}, // 2026-11-03 指定 X 分享样本的分享参数；仅 status 与 t.co。
	"s":       {}, // ADR-214 第 2 条；ClearURLs twitter。
	"t":       {}, // ADR-214 第 2 条；ClearURLs twitter；仅 X，B 站的 t 必须保留。
	"ref_src": {}, // ClearURLs twitter。
	"ref_url": {}, // ClearURLs twitter。
	"cn":      {}, // ClearURLs twitter。
	"spm":     {}, // 规格要求 Extract 行为不变；旧通用项收窄到 X status 和阿里、B 站。
}

// B 站：ClearURLs bilibili 系列（bilibili.com、m.、live.、space.）与 ADR-214、规格-群聊插件第三节的并集；
// 以 share_ 开头的一律是分享追踪（见 isTracking 的前缀）。mid、callback 在 ClearURLs 里也列了，
// 但 mid 会被个别页面当内容参数，不进表。
var biliTracking = map[string]struct{}{
	"spm_id_from":      {}, // ADR-214；ClearURLs。
	"share_source":     {}, // ADR-214；ClearURLs。
	"vd_source":        {}, // ADR-214；ClearURLs。
	"share_medium":     {}, // ClearURLs。
	"share_plat":       {}, // ClearURLs。
	"share_session_id": {}, // ClearURLs。
	"share_tag":        {}, // ClearURLs。
	"share_from":       {}, // ClearURLs。
	"bbid":             {}, // ClearURLs m.bilibili.com。
	"ts":               {}, // ClearURLs m.bilibili.com。
	"timestamp":        {}, // ClearURLs。
	"unique_k":         {}, // ClearURLs。
	"from_spmid":       {}, // 手机端分享。
	"plat_id":          {}, // ClearURLs。
	"buvid":            {}, // ClearURLs。
	"up_id":            {}, // ClearURLs。
	"spm":              {},
	"share_mid":        {}, // 会员购分享人账号，不进入派生链接。
	"channel":          {},
	"from":             {}, // ClearURLs。
	"msource":          {}, // ClearURLs。
	"from_source":      {}, // ClearURLs。
	"seid":             {}, // ClearURLs。
	"refer_from":       {}, // ClearURLs。
	"is_story_h5":      {}, // ClearURLs。
	"session_id":       {}, // ClearURLs live.bilibili.com。
	"visit_id":         {}, // ClearURLs live.bilibili.com。
	"broadcast_type":   {}, // ClearURLs live.bilibili.com。
	"is_room_feed":     {}, // ClearURLs live.bilibili.com。
	"launch_id":        {}, // App 拉起统计。
	"spmid":            {}, // 同 spm_id_from，部分页面写成 spmid。
	"jumpfrom":         {}, // 页面跳转来源（jumpFrom，键已转小写）。
	"track_id":         {}, // 推荐位追踪。
	"mid":              {}, // QQ 小程序分享带的加密用户标识；主页走路径，不用这个参数。
	"-arouter":         {}, // App 内路由名（-Arouter=story 等，键已转小写），对网页没用。
}

func isTracking(key, host, path string) bool {
	key = strings.ToLower(key)
	host = strings.ToLower(host)
	xHost := isXTrackingHost(host, path)
	if _, ok := locatorKeys[key]; ok && !(key == "t" && xHost) {
		return false
	}
	if strings.HasPrefix(key, "utm_") {
		return true
	}
	if _, ok := commonTracking[key]; ok {
		return true
	}
	if xHost {
		_, ok := xTracking[key]
		return ok
	}
	if hostSuffix(host, "bilibili.com", "b23.tv", "bili2233.cn") {
		if strings.HasPrefix(key, "share_") {
			return true
		}
		_, ok := biliTracking[key]
		return ok
	}
	if hostSuffix(host, "youtube.com", "youtu.be", "youtube-nocookie.com") {
		switch key {
		case "si", "pp", "feature", "ab_channel", "app", "kw", "embeds_referring_euri", "source_ve_path":
			return true
		}
		return false
	}
	if hostSuffix(host, "xiaohongshu.com", "xhslink.com", "xhslink.cn") {
		switch key {
		case "xhsshare", "shareredid", "share_id", "apptime", "app_platform", "app_version", "appuid", "author_share", "share_from_user_hidden", "track_code", "source", "exsource", "ignoreengage":
			return true
		}
		return false
	}
	return moreTracking(key, host)
}

// isBiliFirstPart：p=1 和不写一样，QQ 小程序连单 P 视频都带。去掉后多 P 视频按“没指定哪一 P”显示。
func isBiliFirstPart(key, value, host, raw string) bool {
	if !strings.EqualFold(key, "p") || value != "1" || !hostSuffix(strings.ToLower(host), "bilibili.com", "b23.tv", "bili2233.cn") {
		return false
	}
	// 写了好几个 p 的是坏链接，交给取数报错，不替它选
	n := 0
	for part := range strings.SplitSeq(raw, "&") {
		if k, _, _ := strings.Cut(part, "="); strings.EqualFold(k, "p") {
			n++
		}
	}
	return n == 1
}

// isXTrackingHost：X 的追踪表只用于 status 链接和 t.co；X 的搜索、个人页等其余路径只走通用表。
func isXTrackingHost(host, path string) bool {
	return (hostSuffix(host, "x.com", "twitter.com", "fxtwitter.com", "vxtwitter.com", "fixupx.com", "fixvx.com") && isXStatusPath(path)) || host == "t.co"
}

func isXStatusPath(path string) bool {
	_, _, ok := xStatus(path)
	return ok
}

func stripTracking(host, path, raw string) (string, bool) {
	// 不用 Values.Encode：未知参数的重复值、空值、次序和编码都要原样留下。
	var out strings.Builder
	removed, kept, start := false, false, 0
	for part := range strings.SplitSeq(raw, "&") {
		key, value, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			return "", false
		}
		if _, err := url.QueryUnescape(value); err != nil {
			return "", false
		}
		if isTracking(decoded, host, path) || isBiliFirstPart(decoded, value, host, raw) {
			if !removed && kept {
				out.WriteString(raw[:start-1])
			}
			removed = true
		} else {
			if removed {
				if kept {
					out.WriteByte('&')
				}
				out.WriteString(part)
			}
			kept = true
		}
		start += len(part) + 1
	}
	if !removed {
		return raw, true
	}
	return out.String(), true
}

func hostSuffix(host string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}
