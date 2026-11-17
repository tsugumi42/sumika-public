package linkreader

import "strings"

// 只删除已知分享来源，资源 ID、时间定位、未知键和值的编码照旧保留。
// 出处缩写见 tracking.go 的说明；没有出处的项是 App 分享样本里常见的参数名。
func moreTracking(key, host string) bool {
	switch host {
	case "douyin.com", "www.douyin.com", "m.douyin.com", "v.douyin.com", "jx.douyin.com", "jingxuan.douyin.com", "iesdouyin.com", "www.iesdouyin.com":
		if strings.HasPrefix(key, "share_") {
			return true // 抖音 App 分享：share_app_id、share_channel、share_link_id、share_token……
		}
		switch key {
		case "from", "previous_page", "enter_from", "enter_method", "u_code", "user_id", "sec_user_id", "iid", "app", "region", "mid", "with_sec_did":
			return true
		case "timestamp", "did", "ts", "from_aid", "from_ssr", "video_share_track_ver", "schema_type", "utm_source":
			return true
		}
		return false
	case "music.163.com", "y.music.163.com", "163cn.tv":
		// 网易云 App 分享：uct、dlt、app_version、sc、tn、uct2、userid；
		// 微信 / 站外分享 H5 还带 fx-*（分享来源实验）、ts-*（拉起 App 实验）、playerUIModeId、PlayerStyles_*、H5_DownloadVIPGift（2026-11-10 实样：这些一串的值都与内容无关）。
		if strings.HasPrefix(key, "fx-") || strings.HasPrefix(key, "ts-") || strings.HasPrefix(key, "playerstyles_") || strings.HasPrefix(key, "h5_") {
			return true
		}
		switch key {
		case "playeruimodeid":
			return true
		case "userid", "user_id", "app_version", "app_id", "uct2", "uct", "dlt", "sc", "tn", "from", "creatorid":
			return true
		}
		return false
	case "pixiv.net", "www.pixiv.net", "touch.pixiv.net", "phixiv.net", "www.phixiv.net", "ppxiv.net", "www.ppxiv.net":
		switch key {
		case "from", "ref":
			return true
		}
		return false
	case "store.steampowered.com", "steamcommunity.com", "www.steamcommunity.com":
		// l= 是语言选择，不是追踪参数，保留。ClearURLs steampowered：snr。
		switch key {
		case "snr", "curator_clanid":
			return true
		}
		return false
	case "open.spotify.com", "spotify.link":
		// si 是分享标识（ClearURLs spotify.com）；context、nd、dlsi 是 App 分享链接带的来源信息；t 是播客时间点，保留。
		switch key {
		case "si", "context", "nd", "dlsi", "go", "sp_cid":
			return true
		}
		return false
	case "tiktok.com", "www.tiktok.com", "m.tiktok.com", "vm.tiktok.com", "vt.tiktok.com":
		if strings.HasPrefix(key, "share_") {
			return true // ClearURLs tiktok：share_app_name、share_iid；App 分享还有 share_link_id 等。
		}
		switch key {
		case "is_copy_url", "is_from_webapp", "_t", "_r", "_d", "u_code", "preview_pb", "timestamp", "user_id", "source", "sender_device", "sender_web_id", "web_id":
			return true
		}
		return false
	case "github.com", "www.github.com", "gist.github.com":
		// ClearURLs github.com：邮件通知链接里的 email_token、email_source；notification_referrer_id 同属通知跳转。
		switch key {
		case "email_token", "email_source", "notification_referrer_id":
			return true
		}
		return false
	case "afdian.com", "www.afdian.com", "afdian.net", "www.afdian.net", "ifdian.net", "www.ifdian.net":
		return key == "ref" // utm_ 走通用前缀；已识别的爱发电链接整串 query 都丢。
	case "booth.pm", "www.booth.pm":
		return false // BOOTH 分享只带 utm_*，由通用前缀处理；其余（如 lang）不是追踪。
	case "bsky.app", "www.bsky.app", "misskey.io", "mastodon.social":
		return false // 这三家没有分享追踪参数，只走通用表；资源 ID 在路径里。
	}
	switch {
	case hostLabel(host, "amazon"):
		return amazonTracking(key)
	case hostSuffix(host, "taobao.com", "tmall.com", "1688.com", "aliexpress.com", "alibaba.com"):
		return aliTracking(key)
	}
	return false
}

// hostLabel 判断域名里有没有这一整段标签（www.amazon.co.jp、amazon.com 都算，notamazon.com 不算）。
func hostLabel(host, label string) bool {
	for _, part := range strings.Split(host, ".") {
		if part == label {
			return true
		}
	}
	return false
}

// 亚马逊：ClearURLs amazon 规则。th、psc（变体）和 keywords（搜索词）、rnid（筛选）是内容，保留。
func amazonTracking(key string) bool {
	for _, prefix := range []string{"pf_rd_", "pd_rd_", "__mk_", "cv_ct_", "sb-ci-"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	switch key {
	case "qid", "sr", "srs", "spia", "ms3_c", "refrid", "colid", "coliid", "qualifier", "_encoding", "smid", "ref", "ref_",
		"linkcode", "linkid", "creativeasin", "ascsubtag", "aaxitk", "hsa_cr_id", "dchild", "camp", "creative",
		"content-id", "dib", "dib_tag", "social_share", "starsleft", "skiptwisterog", "tag", "crid", "sprefix":
		return true
	}
	return false
}

// 淘宝、天猫等阿里系：ClearURLs taobao.com、tmall.com。price、scene、pos 这类可能是页面内容，不进表。
func aliTracking(key string) bool {
	if strings.HasPrefix(key, "scm") {
		return true
	}
	switch key {
	case "spm", "sourcetype", "suid", "ut_sk", "un", "share_crt_v", "sp_tk", "cpp", "shareurl", "short_name", "app", "pvid",
		"algo_expid", "algo_pvid", "abbucket", "ali_refid", "ali_trackid", "acm", "utparam", "abtest", "trackinfo", "utkn",
		"mytmenu", "turing_bucket", "lygclk", "impid", "bfttag", "bftrwd", "_u":
		return true
	}
	return false
}
