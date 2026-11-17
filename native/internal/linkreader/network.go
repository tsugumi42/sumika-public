package linkreader

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"sumika-native/internal/netsafe"
)

// 目标地址的裁决（docs/规格-链接解析.md 第五节“fake-ip 与代理”，2026-11-13 用户同意）：
//
//   - 每个解析出来的地址都是真公网 → 绑定这个地址去连（老做法，防 DNS 改指向）。
//   - 地址在 fake-ip 段里（Clash / mihomo 的假地址，真地址只有代理知道）：
//     主机名在 trustedHost 名单里 → 不连假地址，经配置的 HTTP 代理按主机名 CONNECT，让代理自己解析；
//     不在名单里 → 拒绝（无法确认它不指向局域网）；名单里但没配代理 → 拒绝并写明原因。
//   - 任何一个地址既不是真公网也不在 fake-ip 段（私网、回环、链路本地、保留段……）→ 整次拒绝，名单内的主机也一样。
//
// 安全拨号（safeDial）和媒体候选检查（inspectMedia）共用 judge，不各算各的。

// 拒绝时记在 FetchError.Reason 里的短原因（linkexpand 的 MediaReason / MediaClass 按它写人看得懂的话）。
// Kind 仍是 unsafe_target，下游按 Kind 分支
const (
	ReasonFakeIPNoProxy   = "fake_ip_no_proxy"
	ReasonFakeIPNotListed = "fake_ip_not_listed"
)

// dialMode 是裁决的结果：怎么连
type dialMode int

const (
	dialByAddress dialMode = iota // 绑定已验证的地址（解析结果的第一个）
	dialByName                    // 经 fake-ip 回退代理，CONNECT 主机名，不带地址
)

// netPolicy 是一个 Fetcher 的 fake-ip 规则：哪些段算假地址，名单内主机碰到假地址时走哪个代理
type netPolicy struct {
	fake  []netip.Prefix
	proxy *url.URL // 配置的 HTTP 代理（linkReader.proxy 或环境变量）；nil = 没有
}

// fakeHostRE：经代理按主机名连之前再核一遍主机名的写法，挡住控制字符、空格这些会弄乱 CONNECT 请求行的东西
var fakeHostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,251}[a-z0-9])?$`)

func normalizeHost(host string) string { return strings.TrimSuffix(strings.ToLower(host), ".") }

// judge 对一个主机和它解析出来的全部地址做裁决。source 记在错误的 Source 上（“dns”或“media_dns”）。
// ips 不能为空（调用方先处理）
func (p netPolicy) judge(host, port string, ips []netip.Addr, source string) (dialMode, *FetchError) {
	hasFake := false
	for _, ip := range ips {
		switch {
		case netsafe.InFakeRanges(p.fake, ip): // 盖住真实内网地址的段 InFakeRanges 不认，那样的地址落到下一条按私有地址整次拒绝
			hasFake = true
		case !netsafe.PublicIP(ip):
			return 0, failure("unsafe_target", source)
		}
	}
	if !hasFake {
		return dialByAddress, nil
	}
	name := normalizeHost(host)
	if !trustedHost(name) || !fakeHostRE.MatchString(name) {
		fe := failure("unsafe_target", source)
		fe.Reason = ReasonFakeIPNotListed
		fe.Detail = "解析到 fake-ip 段，主机不在取数 / 下载名单里，不能确认指向哪里"
		return 0, fe
	}
	if port != "80" && port != "443" {
		return 0, failure("unsafe_target", source)
	}
	if p.proxy == nil {
		fe := failure("unsafe_target", source)
		fe.Reason = ReasonFakeIPNoProxy
		fe.Detail = "解析到 fake-ip 段，fake-ip 下没有代理可走（linkReader.proxy 和 HTTPS_PROXY / HTTP_PROXY 都没配）"
		return 0, fe
	}
	return dialByName, nil
}

// dial 是 safeDial 的核心：解析、裁决、连接。proxy 是这条线路平时的出口（nil = 直连）
func (p netPolicy) dial(ctx context.Context, network, address string, proxy *url.URL, lookup lookupIP) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, failure("unsafe_target", "dns")
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, failure("network", "dns")
	}
	mode, fe := p.judge(host, port, ips, "dns")
	if fe != nil {
		return nil, fe
	}
	if mode == dialByName {
		return dialPinned(ctx, "tcp", net.JoinHostPort(normalizeHost(host), port), p.proxy)
	}
	return dialPinned(ctx, network, net.JoinHostPort(ips[0].String(), port), proxy)
}

// fetchHosts 是取数白名单：固定接口、已使用的 CDN 主机和短链服务的精确主机名，不按平台后缀放宽。
// 短链服务（b23.tv、bili2233.cn、t.co）只连它自己、只读跳转地址，跳去的主机另外照旧检查（2026-11-03）。
// www.bilibili.com 只为小游戏外壳页放行（精确主机，2026-11-10）；收回就删这一项（docs/规格-链接页面类型和样式.md）
var fetchHosts = func() map[string]bool {
	m := map[string]bool{}
	for _, h := range []string{
		"api.bilibili.com", "api.live.bilibili.com", "mall.bilibili.com", "www.bilibili.com", "api.fxtwitter.com", "api.vxtwitter.com",
		"i0.hdslb.com", "i1.hdslb.com", "i2.hdslb.com", "s1.hdslb.com", "archive.biliimg.com",
		"pbs.twimg.com", "video.twimg.com", "mosaic.fxtwitter.com", "cdn.syndication.twimg.com",
		"b23.tv", "bili2233.cn", "t.co",
		"api.github.com", "avatars.githubusercontent.com", "opengraph.githubassets.com", "www.youtube.com", "i.ytimg.com",
		"img.youtube.com", "www.douyin.com", "www.iesdouyin.com", "v.douyin.com", "jx.douyin.com", "music.163.com", "163cn.tv",
		"p1.music.126.net", "p2.music.126.net", "p3.music.126.net", "p4.music.126.net", "p5.music.126.net", "p6.music.126.net",
		"www.pixiv.net", "i.pximg.net", "s.pximg.net", "booth.pm", "booth.pximg.net", "public.api.bsky.app", "bsky.app",
		"cdn.bsky.app", "video.bsky.app", "video.cdn.bsky.app", "misskey.io", "media.misskeyusercontent.jp", "proxy.misskeyusercontent.jp", "mastodon.social",
		"files.mastodon.social", "store.steampowered.com", "api.steampowered.com", "steamcommunity.com", "shared.akamai.steamstatic.com",
		"images.steamusercontent.com", "avatars.fastly.steamstatic.com", "avatars.akamai.steamstatic.com", "afdian.com", "afdian.net", "ifdian.net", "pic1.afdiancdn.com",
		"vod.afdiancdn.com", "open.spotify.com", "spotify.link", "image-cdn-fa.spotifycdn.com", "image-cdn-ak.spotifycdn.com",
		"i.scdn.co", "www.tiktok.com", "vm.tiktok.com", "vt.tiktok.com", "api.xiaoheihe.cn", "www.xiaoheihe.cn", "xhslink.com",
		"xhslink.cn", "t.me", "telegram.me",
	} {
		m[h] = true
	}
	return m
}()

// mediaPlatforms 是 mediaHost 认识的平台。mediaHost 加平台时这里同步加（TestMediaPlatformsListed 对着源码查）
var mediaPlatforms = []string{"x", "bilibili", "pixiv", "youtube", "douyin", "netease", "steam", "afdian", "spotify", "booth", "bluesky", "telegram", "misskey", "mastodon", "tiktok"}

// trustedHost 说这个主机名在不在“取数白名单或任一站点的下载许可名单”里：只有它们碰到 fake-ip 时可以经代理按主机名连。
// 下载许可名单含后缀项（hdslb.com、tiktokcdn-us.com 这类平台自己的 CDN 域），所以同域的新子域也算；
// 名单外的任意网页、IP 字面量、伪后缀（x.hdslb.com.evil.example）都不算
func trustedHost(host string) bool {
	host = normalizeHost(host)
	if fetchHosts[host] || telegramCDN.MatchString(host) || moreMediaHost(host) {
		return true
	}
	for _, platform := range mediaPlatforms {
		if mediaHost(platform, host) {
			return true
		}
	}
	return false
}

func biliHost(host string) bool {
	host = strings.ToLower(host)
	return host == "b23.tv" || host == "bili2233.cn" || host == "bilibili.com" || strings.HasSuffix(host, ".bilibili.com") || strings.HasSuffix(host, ".hdslb.com") || host == "biliimg.com" || strings.HasSuffix(host, ".biliimg.com")
}
