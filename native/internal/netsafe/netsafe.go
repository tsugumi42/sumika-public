// Package netsafe 判断一个 IP 地址能不能当"外网目标"去连。常驻的小包，给要按名字取外网内容、
// 又不能被 DNS 指到内网的几处用（链接取数 linkreader、收图下载 mediafetch）。只放判断，不联网。
package netsafe

import (
	"fmt"
	"net/netip"
	"strings"
)

// excludedPrefixes 是 IsGlobalUnicast 之外还要排除的网段：CGNAT（Tailscale 等常用）、协议保留、文档用、基准测试、
// 240/4 保留段，以及 IPv6 里的 Teredo、6to4、文档、保留段
var excludedPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 88, 99, 0}), 24), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01}), 23), netip.MustParsePrefix("2001:db8::/32"),
	netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x02}), 16), netip.PrefixFrom(netip.AddrFrom16([16]byte{0x3f, 0xff}), 20),
}

// globalIPv6 是目前分配出去的全球单播段 2000::/3；NAT64（64:ff9b::/96）等不在里面，所以也被挡
var globalIPv6 = netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20}), 3)

// PublicIP 是白名单式的判断：只有全球单播、不在私网 / 回环 / 链路本地、IPv6 落在 2000::/3、也不在上面排除网段里的才算外网。
// IPv4 映射的 IPv6 地址按它里面的 IPv4 判断
func PublicIP(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	if a.Is6() && !globalIPv6.Contains(a) {
		return false
	}
	for _, prefix := range excludedPrefixes {
		if prefix.Contains(a) {
			return false
		}
	}
	return true
}

// DefaultFakeIPRanges 是代理 fake-ip 模式默认的假地址段。解析到这些段的地址不是真地址，真正的目的地只有代理知道。
//   - 198.18.0.0/15：IPv4 基准测试保留段（RFC 2544），Clash / mihomo 的 fake-ip-range 默认 198.18.0.1/16 落在这里；
//   - 2001:2::/48：IPv6 基准测试保留段（RFC 5180），和上一段同一类，不会有真实主机。机场订阅常把 fake-ip-range6 写成 2001:2::/64
//     （2026-11-18 主人的订阅自动更新后就是这样，QQ 收图因此全被挡）；
//   - fdfe:dcba:9876::/48：Clash Verge / mihomo 自带 DNS 设置里 fake-ip-range6 的默认段（ULA 里的一个固定前缀，按 minULAFakeBits 收）。
var DefaultFakeIPRanges = []string{"198.18.0.0/15", "2001:2::/48", "fdfe:dcba:9876::/48"}

// DefaultFakeIPPrefixes 是 DefaultFakeIPRanges 解析好的网段
func DefaultFakeIPPrefixes() []netip.Prefix {
	p, err := ParseFakeIPRanges(DefaultFakeIPRanges)
	if err != nil {
		panic("netsafe: 默认假地址段不合格：" + err.Error())
	}
	return p
}

// 假地址段配置的上限：条数和最短前缀。前缀太短（0.0.0.0/0 之类）会把真公网地址也当成"假地址"，名单内主机就全走代理
const (
	MaxFakeIPRanges = 16
	minFakeBits4    = 8
	minFakeBits6    = 16
)

// ParseFakeIPRanges 把配置里的假地址段文字解析成网段，每条是 CIDR（带主机位的写法如 198.18.0.1/16 按网段取整）。
// 拒绝：空条目、不是 CIDR、IPv4 映射的 IPv6 写法、前缀太短（IPv4 短于 /8、IPv6 短于 /16）、
// 和真实本机 / 内网 / 保留地址段重叠（127/8、10/8、172.16/12、192.168/16、169.254/16、100.64/10、::1、fe80::/10 等，写错了会把真私有地址当假地址）、
// 条数超过 MaxFakeIPRanges。
// nil 和空列表都解析成空（没有任何假地址段，解析到保留段的地址一律照旧拒绝）；要用默认值由调用方传 DefaultFakeIPRanges
func ParseFakeIPRanges(raw []string) ([]netip.Prefix, error) {
	if len(raw) > MaxFakeIPRanges {
		return nil, fmt.Errorf("最多 %d 段", MaxFakeIPRanges)
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("%q 不是 CIDR 网段（例如 198.18.0.0/15）", s)
		}
		if p.Addr().Is4In6() {
			return nil, fmt.Errorf("%q 不要用 IPv4 映射的 IPv6 写法，直接写 IPv4 网段", s)
		}
		if p.Addr().Is4() && p.Bits() < minFakeBits4 || p.Addr().Is6() && p.Bits() < minFakeBits6 {
			return nil, fmt.Errorf("%q 范围太大（IPv4 至少 /%d，IPv6 至少 /%d）", s, minFakeBits4, minFakeBits6)
		}
		if overlapsLocal(p.Masked()) {
			return nil, fmt.Errorf("%q 和本机 / 内网 / 保留地址段重叠，不能当假地址段（假地址段是代理占位用的保留段，例如 198.18.0.0/15；IPv6 的 fd00::/8 里要写到 /%d 或更长）", s, minULAFakeBits)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// localPrefixes 是真实的本机 / 内网 / 链路本地 / 组播 / 保留地址段：它们不可能是代理的假地址，配置的假地址段不许盖住它们
// （overlapsLocal，解析时拒绝，裁决时 InFakeRanges 也不认）。IPv6 的唯一本地地址 fc00::/7 只收 fd00::/8（真实局域网用的那一半），
// fc00::/8 没有真实分配，sing-box 的 fake-ip 默认段 fc00::/18 就在里面
var localPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("::ffff:0:0/96"),
	ulaLocal, netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

// ulaLocal 是真实局域网用的唯一本地地址那一半；假地址段落在它里面时只要求写到 minULAFakeBits 位以上，别的内网段一律不许重叠
var ulaLocal = netip.MustParsePrefix("fd00::/8")

// minULAFakeBits：fd00::/8 里的假地址段至少写到 /48（一个完整的随机前缀），再宽就会盖住别人的局域网。
// Clash 的 fdfe:dcba:9876::/48 这类写法能过
const minULAFakeBits = 48

// overlapsLocal 说假地址段 p 有没有盖住（或落进）真实的本机 / 内网 / 保留地址段
func overlapsLocal(p netip.Prefix) bool {
	for _, l := range localPrefixes {
		if !l.Overlaps(p) {
			continue
		}
		if l == ulaLocal && p.Bits() >= minULAFakeBits && l.Contains(p.Addr()) {
			continue
		}
		return true
	}
	return false
}

// InFakeRanges 说 a 算不算假地址：落在某个配置的假地址段里，而且那个段没有盖住真实的本机 / 内网 / 保留地址段。
// 解析时已经拒绝过这样的段，这里再认一次，是为了绕开解析、直接拼出来的规则也不会把真私有地址当假地址
func InFakeRanges(prefixes []netip.Prefix, a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range prefixes {
		if p.Contains(a) && !overlapsLocal(p) {
			return true
		}
	}
	return false
}
