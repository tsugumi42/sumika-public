package linkreader

import (
	"regexp"
	"strings"
)

func domesticHost(host string) bool {
	host = strings.ToLower(host)
	return biliHost(host) || hostSuffix(host, "douyin.com", "iesdouyin.com", "douyinpic.com", "douyincdn.com", "byteimg.com", "music.163.com", "music.126.net", "afdian.com", "afdian.net", "ifdian.net", "afdiancdn.com", "xiaoheihe.cn", "xiaohongshu.com", "xhslink.com", "xhslink.cn") || host == "163cn.tv"
}

func moreMediaHost(host string) bool {
	switch host {
	case "avatars.githubusercontent.com", "opengraph.githubassets.com", "i.ytimg.com", "img.youtube.com", "i.pximg.net", "s.pximg.net", "booth.pximg.net", "p1.music.126.net", "p2.music.126.net", "p3.music.126.net", "p4.music.126.net", "p5.music.126.net", "p6.music.126.net", "cdn.bsky.app", "video.bsky.app", "media.misskeyusercontent.jp", "proxy.misskeyusercontent.jp", "files.mastodon.social", "shared.akamai.steamstatic.com", "images.steamusercontent.com", "avatars.fastly.steamstatic.com", "avatars.akamai.steamstatic.com", "image-cdn-fa.spotifycdn.com", "image-cdn-ak.spotifycdn.com", "i.scdn.co":
		return true
	}
	// 后缀项是平台自己的 CDN 域，同域的新子域也算下载许可名单（解析到 fake-ip 时经代理按主机名连，见 network.go）。
	return hostSuffix(host, "biliimg.com", "douyinpic.com", "douyincdn.com", "byteimg.com", "afdiancdn.com", "tiktokcdn-us.com", "tiktokcdn.com", "cdn-telegram.org", "telesco.pe")
}

var telegramCDN = regexp.MustCompile(`^cdn[1-5]\.(telesco\.pe|cdn-telegram\.org)$`)
