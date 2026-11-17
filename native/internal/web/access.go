package web

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type URLPolicy struct {
	Resolver Resolver
	Ports    map[string]bool
}
type Target struct {
	URL *url.URL
	IPs []netip.Addr
}

// IANA IPv4/IPv6 Special-Purpose Address Registries，固定为本实现策略版本；拒绝过渡地址。
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 88, 99, 0}), 24), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"), netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 1}), 23), netip.MustParsePrefix("2001:db8::/32"), netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 2}), 16), netip.PrefixFrom(netip.AddrFrom16([16]byte{0x3f, 0xff}), 20), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

// fakeIPRange 是 Clash 等代理 fake-ip 模式给外网域名分配的假地址段；真正的目的地由代理按域名决定。
var fakeIPRange = netip.MustParsePrefix("198.18.0.0/15")

// IsFakeIP 判断是否代理的假地址（IPv4 映射写法先还原）。
func IsFakeIP(ip netip.Addr) bool { return fakeIPRange.Contains(ip.Unmap()) }

// HasFakeIP 说明解析结果里有没有假地址。
func HasFakeIP(ips []netip.Addr) bool {
	for _, ip := range ips {
		if IsFakeIP(ip) {
			return true
		}
	}
	return false
}

func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() {
		return false
	}
	if ip.Is6() && !netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20}), 3).Contains(ip) {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func (p URLPolicy) Check(ctx context.Context, raw string) (Target, error) {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\\\x00") {
		return Target{}, fmt.Errorf("WEB_URL_INVALID")
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return Target{}, fmt.Errorf("WEB_URL_INVALID")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || !(u.Scheme == "http" || u.Scheme == "https") || u.Hostname() == "" || strings.Contains(u.Hostname(), "%") || u.Fragment != "" {
		return Target{}, fmt.Errorf("WEB_URL_INVALID")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if _, ipErr := netip.ParseAddr(host); ipErr != nil {
		var err error
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return Target{}, fmt.Errorf("WEB_URL_INVALID")
		}
		numeric := true
		for _, label := range strings.Split(host, ".") {
			digits := label
			hex := strings.HasPrefix(label, "0x")
			if hex {
				digits = label[2:]
			}
			if digits == "" {
				numeric = false
				break
			}
			for _, r := range digits {
				if !(r >= '0' && r <= '9' || hex && r >= 'a' && r <= 'f') {
					numeric = false
					break
				}
			}
			if !numeric {
				break
			}
		}
		if numeric {
			return Target{}, fmt.Errorf("WEB_HOST_BLOCKED")
		}
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return Target{}, fmt.Errorf("WEB_URL_BLOCKED")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if port != "80" && port != "443" && !p.Ports[port] {
		return Target{}, fmt.Errorf("WEB_PORT_BLOCKED")
	}
	for key, values := range u.Query() {
		low := strings.ToLower(key)
		if strings.Contains(low, "token") || strings.Contains(low, "secret") || strings.Contains(low, "password") || low == "key" || low == "api_key" || low == "authorization" {
			return Target{}, fmt.Errorf("WEB_URL_CREDENTIALS")
		}
		for _, v := range values {
			if strings.HasPrefix(v, "sk-") {
				return Target{}, fmt.Errorf("WEB_URL_CREDENTIALS")
			}
		}
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		if !strings.Contains(host, ".") {
			return Target{}, fmt.Errorf("WEB_HOST_BLOCKED")
		}
		resolver := p.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		ips, err = resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return Target{}, err
		}
	}
	if len(ips) == 0 {
		return Target{}, fmt.Errorf("WEB_DNS_EMPTY")
	}
	for _, ip := range ips {
		if !PublicIP(ip) && !IsFakeIP(ip) {
			return Target{}, fmt.Errorf("WEB_ADDRESS_BLOCKED")
		}
	}
	if strings.Contains(host, ":") {
		u.Host = net.JoinHostPort(host, port)
	} else if u.Port() != "" {
		u.Host = net.JoinHostPort(host, port)
	} else {
		u.Host = host
	}
	return Target{u, ips}, nil
}
