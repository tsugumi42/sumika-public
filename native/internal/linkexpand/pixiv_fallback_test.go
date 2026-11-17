package linkexpand

import (
	"context"
	"image/color"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"sumika-native/internal/linkreader"
)

// 回归：pixiv 的原图超过 20 MB 时，真实服务器在响应头里就写了 Content-Length，下载端一个字节都没收就拒绝；
// 以前只有“流到一半才超”才改取 regular，所以这种最常见的情形直接写“图片未下载”（2026-11-09 真实 24 MB 原图实测）。
func TestPixivOriginalOverLimitByContentLengthFallsBackToRegular(t *testing.T) {
	blue := mediaPNG(t, color.RGBA{B: 255, A: 255})
	var regular atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/original.png":
			w.Header().Set("Content-Length", strconv.FormatInt(imageLimit+1024, 10))
			w.WriteHeader(200)
		case "/regular.png":
			regular.Add(1)
			w.Write(blue)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	defer transport.CloseIdleConnections()
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: transport, Rate: linkreader.RateOptions{RequestsPerMinute: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	m := NewMedia(f, t.TempDir())
	src := linkreader.Media{Type: "image", URL: mediaField("https://i.pximg.net/original.png"),
		Fallback: &linkreader.MediaFallback{URL: mediaField("https://i.pximg.net/regular.png")}}
	loaded, err := m.Image(context.Background(), "pixiv", src)
	if err != nil {
		t.Fatalf("按长度超限没有退到 regular：%v", err)
	}
	defer loaded.Resource.Release()
	if regular.Load() != 1 || loaded.Note != "原图超过20 MB，使用regular" {
		t.Fatalf("regular=%d note=%q", regular.Load(), loaded.Note)
	}
	// 没有退路的站点：超限照旧是失败，不乱换图
	other := src
	other.Fallback = nil
	if _, err = m.Image(context.Background(), "pixiv", other); err == nil {
		t.Fatal("没有退路却成功了")
	}
}
