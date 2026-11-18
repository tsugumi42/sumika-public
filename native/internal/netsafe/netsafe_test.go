package netsafe

import (
	"net/netip"
	"strings"
	"testing"
)

func TestPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		// 能连的外网
		"1.1.1.1": true, "8.8.8.8": true, "172.32.0.1": true, "2606:4700:4700::1111": true, "::ffff:8.8.8.8": true,
		// 本机、私网、链路本地、未指定、组播、广播
		"127.0.0.1": false, "10.0.0.1": false, "172.16.5.1": false, "192.168.1.1": false, "169.254.1.1": false, "0.0.0.0": false,
		"0.1.2.3": false, "224.0.0.1": false, "255.255.255.255": false, "::1": false, "::": false, "fe80::1": false, "fc00::1": false, "ff02::1": false,
		"::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		// 别的不该连的网段：CGNAT、协议保留、文档用、基准测试、保留段
		"100.64.0.1": false, "100.127.255.254": false, "192.0.0.8": false, "192.0.2.1": false, "198.18.0.1": false, "198.19.255.255": false,
		"198.51.100.1": false, "203.0.113.7": false, "240.0.0.1": false,
		// IPv6：NAT64（能内嵌 127.0.0.1 或内网 v4）、Teredo、6to4、文档、2000::/3 之外
		"64:ff9b::7f00:1": false, "64:ff9b::a00:1": false, "2001::1": false, "2002:7f00:1::1": false, "2001:db8::1": false, "3fff::1": false, "4000::1": false,
	} {
		if got := PublicIP(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s：PublicIP=%v，要 %v", ip, got, want)
		}
	}
}

func TestParseFakeIPRanges(t *testing.T) {
	got, err := ParseFakeIPRanges(DefaultFakeIPRanges)
	if err != nil || len(got) != 3 || got[0].String() != "198.18.0.0/15" || got[1].String() != "2001:2::/48" || got[2].String() != "fdfe:dcba:9876::/48" {
		t.Fatalf("默认段：%v %v", got, err)
	}
	if got, err = ParseFakeIPRanges([]string{" 198.18.0.1/16 "}); err != nil || got[0].String() != "198.18.0.0/16" {
		t.Fatalf("带主机位的写法按网段取整：%v %v", got, err)
	}
	if got, err = ParseFakeIPRanges(nil); err != nil || len(got) != 0 {
		t.Fatalf("nil：%v %v", got, err)
	}
	for _, bad := range []string{"", "x", "198.18.0.0", "0.0.0.0/0", "10.0.0.0/7", "::/0", "fc00::/15", "::ffff:10.0.0.0/104", "198.18.0.0/33"} {
		if _, err := ParseFakeIPRanges([]string{bad}); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
	if got, err = ParseFakeIPRanges([]string{"fc00::/18", "fc00::/16", "fdfe:dcba:9876::1/48", "28.0.0.0/8", "172.32.0.0/16", "192.0.2.0/24", "198.18.0.0/15"}); err != nil || len(got) != 7 {
		t.Fatalf("占位用的保留段和公网段该能过：%v %v", got, err)
	}
	if _, err := ParseFakeIPRanges(make([]string, MaxFakeIPRanges+1)); err == nil {
		t.Fatal("条数超限应被拒绝")
	}
	prefixes, _ := ParseFakeIPRanges([]string{"198.18.0.0/15"})
	for s, want := range map[string]bool{"198.18.0.0": true, "198.19.255.255": true, "::ffff:198.18.3.3": true, "198.20.0.0": false, "198.17.255.255": false, "10.0.0.1": false} {
		if InFakeRanges(prefixes, netip.MustParseAddr(s)) != want {
			t.Fatalf("InFakeRanges %s", s)
		}
	}
}

// 假地址段不许盖住真实的本机 / 内网 / 保留地址：写错了会把真私有地址当假地址，名单内主机就不再整次拒绝
func TestParseFakeIPRangesRejectsRealLocal(t *testing.T) {
	for _, bad := range []string{
		"127.0.0.0/8", "127.1.0.0/16", "10.0.0.0/8", "10.99.0.0/16", "192.168.0.0/16", "192.168.1.0/24", "172.16.0.0/12", "172.20.0.0/16",
		"169.254.0.0/16", "100.64.0.0/10", "100.100.0.0/16", "0.0.0.0/8", "224.0.0.0/8", "240.0.0.0/8", "255.0.0.0/8",
		"192.0.0.0/8", "172.0.0.0/8", "169.0.0.0/8", // 比私网段更宽、把它们盖在里面
		"::/16", "::/64", "fe80::/16", "fe80::/32", "ff00::/16", "ff02::/32", "ff02::/48", "ff00::/64", "ff02::1/128", "fd00::/16", "fd00::/47", "fd12::/32", "fd12:3456:789a::/47",
	} {
		_, err := ParseFakeIPRanges([]string{bad})
		if err == nil || !strings.Contains(err.Error(), "重叠") {
			t.Errorf("%q 应因和内网段重叠被拒绝：%v", bad, err)
		}
	}
	// 同一组里混一条也整组拒绝
	if _, err := ParseFakeIPRanges([]string{"198.18.0.0/15", "127.0.0.0/8"}); err == nil {
		t.Fatal("混着一条内网段应整组拒绝")
	}
	// InFakeRanges 不认盖住内网的段，也不认段外的地址
	local := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("fd00::/16")}
	for _, ip := range []string{"127.0.0.1", "fd00::1", "::ffff:127.0.0.1"} {
		if InFakeRanges(local, netip.MustParseAddr(ip)) {
			t.Errorf("%s 被当成假地址", ip)
		}
	}
	good, _ := ParseFakeIPRanges([]string{"198.18.0.0/15", "fdfe:dcba:9876::/48"})
	for ip, want := range map[string]bool{"198.19.1.1": true, "::ffff:198.18.0.7": true, "fdfe:dcba:9876::9": true, "fdfe:dcba:9877::9": false, "10.0.0.1": false} {
		if InFakeRanges(good, netip.MustParseAddr(ip)) != want {
			t.Errorf("InFakeRanges %s 要 %v", ip, want)
		}
	}
}
