package linkreader

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

const mosaicJPEG = "https://mosaic.fxtwitter.com/jpeg/90001/a/b/c"

func mosaicBody(images int, mosaic string) string {
	var all []string
	for range images {
		all = append(all, `{"type":"photo","url":"https://pbs.twimg.com/media/x.jpg","width":10,"height":10}`)
	}
	m := ""
	if mosaic != "" {
		m = `,"mosaic":{"type":"mosaic_photo","formats":{"jpeg":"` + mosaic + `","webp":"https://mosaic.fxtwitter.com/webp/90001/a/b/c"}}`
	}
	return `{"code":200,"status":{"id":"90001","text":"x","media":{"all":[` + strings.Join(all, ",") + `]` + m + `}}}`
}

// 三张及以上的图，fx 给的 jpeg 拼图进结果；只认 jpeg，不用 webp。
func TestXMosaicIsParsed(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, mosaicBody(4, mosaicJPEG)), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/example/status/90001"))
	if err != nil || r.Mosaic.State != "available" || r.Mosaic.Value.URL.Value != mosaicJPEG || r.Mosaic.Value.Type != "image" {
		t.Fatalf("%+v %v", r.Mosaic, err)
	}
}

func TestXMosaicUntrustedNotUsed(t *testing.T) {
	for _, c := range []struct{ name, body string }{{"fx 没给拼图", mosaicBody(4, "")}, {"拼图主机不在名单里", mosaicBody(4, "https://evil.example/jpeg/90001")}} {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, c.body), nil })
		r, err := f.Fetch(context.Background(), link(t, "https://x.com/example/status/90001"))
		if err != nil {
			t.Fatalf("%s：%v", c.name, err)
		}
		if r.Mosaic.State == "available" && r.Mosaic.Value.URL.State == "available" {
			t.Fatalf("%s：拼图地址不该被接受", c.name)
		}
	}
}

// 拼图和别的媒体一样过主机白名单、DNS 公网检查；下载只放行 mosaic.fxtwitter.com，不带 Referer，体积照预算。
func TestXMosaicHostChecks(t *testing.T) {
	for _, raw := range []string{"https://mosaic.fxtwitter.com.evil.example/a", "http://127.0.0.1/a", "https://a:b@mosaic.fxtwitter.com/a", "https://mosaic.fxtwitter.com:444/a", "https://fxtwitter.com/a"} {
		r := Result{Mosaic: available(Media{Type: "image", URL: available(raw)})}
		(&Fetcher{}).inspectMedia(context.Background(), &r)
		if r.Mosaic.Value.URL.State != "failed" || r.Mosaic.Value.URL.Reason != "unsafe_target" {
			t.Fatalf("不安全的拼图地址被接受：%s", raw)
		}
	}
	r := Result{Mosaic: available(Media{Type: "image", URL: available(mosaicJPEG)})}
	(&Fetcher{}).inspectMedia(context.Background(), &r)
	if r.Mosaic.Value.URL.State != "available" {
		t.Fatalf("正常拼图地址被拒：%+v", r.Mosaic.Value.URL)
	}
	private := &Fetcher{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("10.0.0.1")}, nil
	}}
	r = Result{Mosaic: available(Media{Type: "image", URL: available(mosaicJPEG)})}
	private.inspectMedia(context.Background(), &r)
	if r.Mosaic.Value.URL.State != "failed" {
		t.Fatal("解析到内网地址的拼图主机被接受")
	}
	// Clash fake-ip 下和 pbs.twimg.com 一样放行；别的主机不放行
	fake := netip.MustParseAddr("198.18.0.9")
	if !allowedWithProxy("mosaic.fxtwitter.com", fake) || allowedWithProxy("mosaic.fxtwitter.com.evil.example", fake) {
		t.Fatal("fake-ip 放行范围不对")
	}
	if !mediaHost("x", "mosaic.fxtwitter.com") || mediaHost("x", "mosaic.fxtwitter.com.evil.example") || mediaHost("bilibili", "mosaic.fxtwitter.com") {
		t.Fatal("下载主机名单不对")
	}
	var gotReferer string
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		gotReferer = req.Header.Get("Referer")
		return response(200, "0123456789"), nil
	})
	var sink strings.Builder
	if err := f.Stream(context.Background(), "x", mosaicJPEG, &sink, &ByteBudget{Limit: 64}); err != nil || sink.String() != "0123456789" || gotReferer != "" {
		t.Fatalf("下载：%v %q referer=%q", err, sink.String(), gotReferer)
	}
	if err := f.Stream(context.Background(), "x", mosaicJPEG, &sink, &ByteBudget{Limit: 4}); err == nil {
		t.Fatal("拼图超过体积预算也放行")
	}
}
