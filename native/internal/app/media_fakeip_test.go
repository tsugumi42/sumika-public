package app

import (
	"net/netip"
	"testing"

	"sumika-native/internal/config"
)

// linkReader.fakeIPRanges 改了，收图下载认的假地址段跟着变（ADR-392）
func TestMediaFakeIPRangesFollowsConfig(t *testing.T) {
	nolog := func(string, ...any) {}
	v6 := netip.MustParseAddr("2001:2::94")
	has := func(ps []netip.Prefix, a netip.Addr) bool {
		for _, p := range ps {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if got := mediaFakeIPRanges(config.DefaultFakeIPRanges(), nolog); !has(got, v6) || !has(got, netip.MustParseAddr("198.18.0.1")) {
		t.Fatalf("默认值要含 IPv4、IPv6 假地址段：%v", got)
	}
	if got := mediaFakeIPRanges([]string{"198.18.0.0/15"}, nolog); has(got, v6) {
		t.Fatalf("只写 IPv4 段时不该认 IPv6 假地址：%v", got)
	}
	if got := mediaFakeIPRanges([]string{}, nolog); got == nil || len(got) != 0 {
		t.Fatalf("[] 要是非 nil 的空切片（nil 会被当成默认段）：%#v", got)
	}
	// 接线：配置写 [] 时传给下载器的也是非 nil 空切片
	cfg := config.Config{}
	cfg.LinkReader.FakeIPRanges = []string{}
	if o := mediaDownloadOptions(cfg, nolog); o.FakeIPRanges == nil || len(o.FakeIPRanges) != 0 {
		t.Fatalf("配置 [] 没传到下载器：%#v", o.FakeIPRanges)
	}
	cfg.LinkReader.FakeIPRanges = config.DefaultFakeIPRanges()
	if o := mediaDownloadOptions(cfg, nolog); !has(o.FakeIPRanges, v6) {
		t.Fatalf("默认配置没传到下载器：%v", o.FakeIPRanges)
	}
	var logged bool
	if got := mediaFakeIPRanges([]string{"10.0.0.0/8"}, func(string, ...any) { logged = true }); got == nil || len(got) != 0 || !logged {
		t.Fatalf("不合格的配置按没有假地址段并记日志：%#v %v", got, logged)
	}
}
