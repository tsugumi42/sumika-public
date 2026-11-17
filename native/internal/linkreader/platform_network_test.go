package linkreader

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestPlatformAddressPolicy(t *testing.T) {
	// 假代理记下 CONNECT 目标：公网地址绑定 IP 连，fake-ip 经代理按主机名连（绝不带 198.18 地址）
	proxy := startFakeProxy(t, nil)
	policy := netPolicy{fake: defaultFakeRanges(t), proxy: proxy.URL}
	shapes := []struct {
		name         string
		ips          []string
		public, fake bool
	}{
		{"public", []string{"1.1.1.1"}, true, true},
		{"private", []string{"10.0.0.1"}, false, false},
		{"loopback", []string{"127.0.0.1"}, false, false},
		{"link_local", []string{"169.254.1.1"}, false, false},
		{"reserved", []string{"192.0.2.1"}, false, false},
		{"fake_start", []string{"198.18.0.0"}, false, true},
		{"fake_end", []string{"198.19.255.255"}, false, true},
		{"mapped_fake", []string{"::ffff:198.18.0.4"}, false, true},
		{"mapped_private", []string{"::ffff:10.0.0.1"}, false, false},
		{"ipv6_private", []string{"fc00::1"}, false, false},
		{"mixed_private", []string{"1.1.1.1", "10.0.0.1"}, false, false},
		{"mixed_fake_private", []string{"198.18.0.4", "10.0.0.1"}, false, false},
		{"mixed_public_fake", []string{"1.1.1.1", "198.18.0.4"}, false, true},
	}
	// fake：碰到 fake-ip 时能不能经代理连（= 在取数白名单或下载许可名单里）；media：inspectMedia 是否把它收作媒体候选（只看 moreMediaHost 那份）
	hosts := []struct {
		host        string
		fake, media bool
	}{
		{"api.bilibili.com", true, false}, {"api.fxtwitter.com", true, false}, {"api.vxtwitter.com", true, false},
		{"i0.hdslb.com", true, true}, {"i1.hdslb.com", true, true}, {"i2.hdslb.com", true, true}, {"s1.hdslb.com", true, true},
		{"pbs.twimg.com", true, true}, {"video.twimg.com", true, true},
		{"other.hdslb.com", true, true}, {"api.bilibili.com.evil.example", false, false},
		{"b23.tv", true, false}, {"bili2233.cn", true, false}, {"t.co", true, false}, {"www.bilibili.com", true, false},
		{"avatars.akamai.steamstatic.com", true, true}, {"avatars.fastly.steamstatic.com", true, true}, {"avatars.evil.steamstatic.com", false, false},
		{"api.github.com", true, false}, {"avatars.githubusercontent.com", true, true}, {"opengraph.githubassets.com", true, true},
		{"www.youtube.com", true, false}, {"i.ytimg.com", true, true}, {"img.youtube.com", true, true},
		{"www.douyin.com", true, false}, {"www.iesdouyin.com", true, false}, {"v.douyin.com", true, false}, {"jx.douyin.com", true, false},
		{"music.163.com", true, false}, {"163cn.tv", true, false},
		{"p1.music.126.net", true, true}, {"p2.music.126.net", true, true}, {"p3.music.126.net", true, true}, {"p4.music.126.net", true, true}, {"p5.music.126.net", true, true}, {"p6.music.126.net", true, true},
		{"www.pixiv.net", true, false}, {"i.pximg.net", true, true}, {"s.pximg.net", true, true},
		{"other.music.126.net", true, false}, {"i.pximg.net.evil.example", false, false}, {"other.douyinpic.com", true, true}, {"api.github.com.evil.example", false, false},
		{"api.xiaoheihe.cn", true, false}, {"www.xiaoheihe.cn", true, false}, {"xhslink.com", true, false}, {"xhslink.cn", true, false},
		{"xiaoheihe.cn.evil.example", false, false}, {"xhslink.com.evil.example", false, false},
		{"p16-common-sign.tiktokcdn-us.com", true, true}, {"p19-common-sign.tiktokcdn-us.com", true, true},
		{"p16-common-sign.tiktokcdn-us.com.evil.example", false, false},
	}
	for _, h := range hosts {
		for _, shape := range shapes {
			t.Run(h.host+"/"+shape.name, func(t *testing.T) {
				ips := make([]netip.Addr, len(shape.ips))
				for i, raw := range shape.ips {
					ips[i] = netip.MustParseAddr(raw)
				}
				lookup := func(context.Context, string) ([]netip.Addr, error) { return ips, nil }
				allowed := shape.public || h.fake && shape.fake
				before := len(proxy.Targets())
				conn, err := safeDial(proxy.URL, lookup, policy)(context.Background(), "tcp", net.JoinHostPort(h.host, "443"))
				if allowed {
					if err != nil {
						t.Fatal(err)
					}
					conn.Close()
					want := h.host + ":443"
					if shape.public {
						want = ips[0].String() + ":443" // 公网：绑定解析出的第一个地址（这里平常出口也是这个假代理）
					}
					if got := proxy.Targets(); len(got) != before+1 || got[before] != want {
						t.Fatalf("CONNECT 目标 %v，想要 %s", got, want)
					}
				} else {
					if conn != nil {
						conn.Close()
						t.Fatal("unsafe DNS connected")
					}
					wantError(t, err, "unsafe_target")
					if len(proxy.Targets()) != before {
						t.Fatal("被拒的目标到了代理")
					}
				}
				r := Result{Media: available([]Media{{URL: available("https://" + h.host + "/image.jpg")}})}
				r.Author.Avatar = available("https://" + h.host + "/avatar.jpg")
				(&Fetcher{lookup: lookup, policy: policy}).inspectMedia(context.Background(), &r)
				if got := r.Media.Value[0].URL.State == "available"; got != (allowed && h.media) {
					t.Fatalf("media accepted=%v", got)
				}
				if got := r.Author.Avatar.State == "available"; got != (allowed && h.media) {
					t.Fatalf("avatar accepted=%v", got)
				}
			})
		}
	}
}

func TestPlatformRouting(t *testing.T) {
	// 处理函数在服务器各自的协程里跑：经代理的解析同时发 A、AAAA 两个请求，计数要用原子量
	var directCalls, proxyCalls atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("non-anonymous request")
		}
		w.Write([]byte(`{"code":0}`))
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	for _, viaEnv := range []bool{false, true} {
		opts := FetchOptions{Proxy: proxy.URL}
		if viaEnv {
			t.Setenv("HTTPS_PROXY", proxy.URL)
			opts.Proxy = ""
		}
		f, err := NewFetcher(opts)
		if err != nil {
			t.Fatal(err)
		}
		direct := f.biliClient.Transport.(*http.Transport)
		direct.TLSClientConfig = origin.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
		direct.TLSClientConfig.ServerName = "example.com"
		direct.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
		}
		defer direct.CloseIdleConnections()
		for _, raw := range []string{"https://api.bilibili.com/x/web-interface/view", "https://b23.tv/example", "https://www.douyin.com/aweme/v1/web/aweme/detail/", "https://www.iesdouyin.com/web/api/v2/aweme/slidesinfo/", "https://163cn.tv/example", "https://music.163.com/api/song/detail/", "https://afdian.com/api/user/get-profile-by-slug?url_slug=example"} {
			body, status, _, err := f.request(context.Background(), raw, "test")
			if err != nil || status != 200 || string(body) != `{"code":0}` {
				t.Fatalf("B站 direct: %s %d %v", body, status, err)
			}
		}
		for _, raw := range []string{"https://api.fxtwitter.com/2/status/90001", "https://www.youtube.com/watch?v=abcdefghijk", "https://www.pixiv.net/ajax/illust/90001", "https://api.github.com/repos/example-lab/widget"} {
			_, _, _, err = f.request(context.Background(), raw, "test")
			wantError(t, err, "proxy_failed")
		}
		for _, tc := range []struct{ platform, raw string }{{"bilibili", "https://i0.hdslb.com/fixture.png"}, {"douyin", "https://p1.douyinpic.com/fixture.png"}, {"netease", "https://p1.music.126.net/fixture.png"}, {"afdian", "https://pic1.afdiancdn.com/fixture.png"}, {"afdian", "https://vod.afdiancdn.com/fixture.mp4"}} {
			var body bytes.Buffer
			if err := f.Stream(context.Background(), tc.platform, tc.raw, &body, &ByteBudget{Limit: 64}); err != nil || body.String() != `{"code":0}` {
				t.Fatalf("国内媒体走错出口：%s body=%s err=%v", tc.platform, body.String(), err)
			}
		}
		for _, tc := range []struct{ platform, raw string }{{"x", "https://pbs.twimg.com/fixture.png"}, {"youtube", "https://i.ytimg.com/fixture.png"}, {"pixiv", "https://i.pximg.net/fixture.png"}} {
			err := f.Stream(context.Background(), tc.platform, tc.raw, io.Discard, &ByteBudget{Limit: 64})
			wantError(t, err, "proxy_failed")
		}
	}
	// 直连 = 2 轮 ×（B 站/抖音/网易云/爱发电 7 条请求 + 国内媒体 5 条）
	// 经代理的 14 次解析各同时发 A、AAAA 两个 DoH 请求，每个都先打一次 CONNECT（被假代理拒绝），所以是 28。
	if directCalls.Load() != 24 || proxyCalls.Load() != 28 {
		t.Fatalf("direct=%d proxy=%d", directCalls.Load(), proxyCalls.Load())
	}
}
