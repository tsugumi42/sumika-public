package linkreader

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// token 照 react-tweet 的 ((id/1e15)*π).toString(36) 去掉 0 和小数点；样例由 node（V8）算出。
func TestSyndicationTokenMatchesV8(t *testing.T) {
	for id, want := range map[string]string{
		"20":                  "6dq1a2xwd93",
		"1142974214":          "614auwgenvi",
		"1900000000000000013": "4ltxr9b3f",
		"1683920951807971329": "42y6zv7ufp",
		"1808168357224571130": "4dsjubau54",
	} {
		if got := syndicationToken(id); got != want {
			t.Errorf("%s：%s，应为 %s", id, got, want)
		}
	}
	if syndicationToken("12a") != "" {
		t.Fatal("不是数字的 ID 不算 token")
	}
}

const syndBody = `{"__typename":"Tweet","id_str":"90001","text":"编的推文正文 https://t.co/abc","display_text_range":[0,6],"created_at":"2026-11-01T10:30:00.000Z",
"favorite_count":12,"conversation_count":3,"possibly_sensitive":false,
"user":{"id_str":"7","name":"纸云","screen_name":"paper_cloud","profile_image_url_https":"https://pbs.twimg.com/profile_images/1/a_normal.jpg","is_blue_verified":false,"verified_type":"Business"},
"mediaDetails":[{"type":"photo","media_url_https":"https://pbs.twimg.com/media/a.jpg","original_info":{"width":1200,"height":800}},
{"type":"video","media_url_https":"https://pbs.twimg.com/ext_tw_video_thumb/1/pu/img/b.jpg","original_info":{"width":1280,"height":720},
"video_info":{"duration_millis":12500,"variants":[{"content_type":"application/x-mpegURL","url":"https://video.twimg.com/a.m3u8"},{"bitrate":256000,"content_type":"video/mp4","url":"https://video.twimg.com/low.mp4"},{"bitrate":2176000,"content_type":"video/mp4","url":"https://video.twimg.com/high.mp4"}]}}],
"quoted_tweet":{"__typename":"Tweet","id_str":"90002","text":"被引的","user":{"name":"纸灯","screen_name":"paper_lamp","is_blue_verified":true},"mediaDetails":[]}}`

// fx_v2、vx 都失败才取 syndication；它不给的转发、浏览等记“没返回”，不编成 0。
func TestXFetchSyndicationLast(t *testing.T) {
	var synd atomic.Int32
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "cdn.syndication.twimg.com" {
			synd.Add(1)
			q := r.URL.Query()
			if q.Get("id") != "90001" || q.Get("token") != syndicationToken("90001") || !strings.Contains(q.Get("features"), "tfw_show_blue_verified_badge:on") {
				t.Errorf("请求参数：%s", r.URL.RawQuery)
			}
			return response(200, syndBody), nil
		}
		return response(503, ""), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if synd.Load() != 1 || r.Source != RouteSyndication || len(r.Attempts) != 3 || r.Attempts[2].Route != RouteSyndication {
		t.Fatalf("来源 %s，syndication 请求 %d 次，线路 %+v", r.Source, synd.Load(), r.Attempts)
	}
	if r.Text.Value != "编的推文正文" || r.Author.Account.Value != "paper_cloud" || r.Author.Avatar.Value != "https://pbs.twimg.com/profile_images/1/a_200x200.jpg" || r.Author.Verification.Value != VerifyBusiness {
		t.Fatalf("作者或正文：%+v %q", r.Author, r.Text.Value)
	}
	if r.Stats["likes"].Value != 12 || r.Stats["replies"].Value != 3 || r.Stats["reposts"].State != "not_returned" || r.Stats["views"].State != "not_returned" {
		t.Fatalf("统计：%+v", r.Stats)
	}
	if len(r.Media.Value) != 2 || r.Media.Value[0].Type != "image" || r.Media.Value[1].URL.Value != "https://video.twimg.com/high.mp4" || r.Media.Value[1].Duration.Value != 12.5 || r.Published.Value.Hour() != 10 {
		t.Fatalf("媒体或时间：%+v", r.Media.Value)
	}
	if r.Quote.State != "available" || r.Quote.Value.Author.Verification.Value != VerifyBlue || r.Quote.Value.Text.Value != "被引的" {
		t.Fatalf("引用：%+v", r.Quote)
	}

	// 前两条有一条成功：不碰 syndication
	synd.Store(0)
	f2 := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Hostname() {
		case "cdn.syndication.twimg.com":
			synd.Add(1)
		case "api.vxtwitter.com":
			return response(200, fixture(t, "vx-video")), nil
		}
		return response(503, ""), nil
	})
	if _, err := f2.Fetch(context.Background(), link(t, "https://x.com/AP/status/1900000000000000013")); err != nil || synd.Load() != 0 {
		t.Fatalf("前两条成功时不该取备用：%v %d", err, synd.Load())
	}

	// 关掉备用路线：都失败就失败
	f3, err := NewFetcher(FetchOptions{NoSyndication: true, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "cdn.syndication.twimg.com" {
			synd.Add(1)
		}
		return response(503, ""), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f3.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001")); err == nil || synd.Load() != 0 {
		t.Fatalf("关着备用路线还取了：%v %d", err, synd.Load())
	}

	// 墓碑是不存在；ID 对不上不收。空内容另见 TestXSyndicationEmptyNotProof
	for body, kind := range map[string]string{`{"__typename":"TweetTombstone"}`: "not_found", `{"__typename":"Tweet","id_str":"90009","text":"别的"}`: "identity_mismatch"} {
		f4 := testFetcher(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Hostname() == "cdn.syndication.twimg.com" {
				return response(200, body), nil
			}
			return response(503, ""), nil
		})
		_, err := f4.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
		wantError(t, err, kind)
	}
}

// 三条线路时：成功率高的前两名同时取；syndication 成功多了会挤进前两名，排第三的只在前两名都失败时才取。
func TestXRouteOrderBySuccessThree(t *testing.T) {
	var hosts sync32
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		hosts.add(r.URL.Hostname())
		if r.URL.Hostname() == "cdn.syndication.twimg.com" {
			return response(200, syndBody), nil
		}
		return response(503, ""), nil
	})
	order := func() string {
		var s []string
		for _, r := range f.xRoutes() {
			s = append(s, r.name)
		}
		return strings.Join(s, ",")
	}
	if got := order(); got != "fx_v2,vx,syndication" {
		t.Fatalf("默认顺序：%s", got)
	}
	for range 10 {
		f.xStats.record(RouteVX, false)
		f.xStats.record(RouteSyndication, true)
	}
	// syndication 成功率再高也固定垫底（规格 C2：只在前两条都失败时取）；fx、vx 之间照成功率排
	if got := order(); got != "fx_v2,vx,syndication" {
		t.Fatalf("按成功率：%s", got)
	}
	for range 20 {
		f.xStats.record(RouteFX, false)
	}
	if got := order(); got != "vx,fx_v2,syndication" {
		t.Fatalf("fx 变差：%s", got)
	}
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil || r.Source != RouteSyndication || !hosts.has("api.vxtwitter.com") || !hosts.has("api.fxtwitter.com") {
		t.Fatalf("前两条都失败才取备用：%v %v %v", r.Source, err, hosts.list())
	}
}

// 前两条线路都没答上来（网络断、5xx）时，备用路线回空内容不足以说明帖子不存在：报线路故障，不报“帖子不存在”。
// 前两条里有一条明确说了 404，才报不存在。
func TestXSyndicationEmptyNotProof(t *testing.T) {
	for _, c := range []struct {
		name    string
		primary func() (*http.Response, error)
		kind    string
	}{
		{"网络断", func() (*http.Response, error) { return nil, errors.New("编的连接断开") }, "network"},
		{"5xx", func() (*http.Response, error) { return response(503, ""), nil }, "interface_error"},
		{"fx 说了 404", func() (*http.Response, error) { return response(404, `{}`), nil }, "not_found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Hostname() == "cdn.syndication.twimg.com" {
					return response(200, ""), nil
				}
				if c.kind == "not_found" && r.URL.Hostname() == "api.vxtwitter.com" {
					return response(503, ""), nil
				}
				return c.primary()
			})
			_, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
			wantError(t, err, c.kind)
		})
	}
}

// 成功率只记线路自己的成败：本地预算用完、等请求间隔时被取消，都不是那条线路的错。
func TestXRouteStatsSkipLocalFailures(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	f, err := NewFetcher(FetchOptions{NoSyndication: true, Rate: RateOptions{RequestsPerMinute: 1, Now: func() time.Time { return now }}, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(503, ""), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	// 一条发出去拿到 503（记失败），另一条被本地预算挡下（不记）
	n := len(f.xStats.hist[RouteFX]) + len(f.xStats.hist[RouteVX])
	if n != 1 {
		t.Fatalf("只该记真发出去的那一次，记了 %d 次：%+v", n, f.xStats.hist)
	}
}

// 备用路线的正文是转义过的 HTML、链接是 t.co：反转义、t.co 换回原链接。
func TestXSyndicationTextUnescapeAndExpand(t *testing.T) {
	body := `{"__typename":"Tweet","id_str":"90001","text":"猫 &amp; 狗 &lt;3 看 https://t.co/abc https://t.co/media","display_text_range":[0,27],` +
		`"entities":{"urls":[{"url":"https://t.co/abc","expanded_url":"https://example.com/编的页面"}]},` +
		`"user":{"id_str":"7","name":"纸云","screen_name":"paper_cloud"}}`
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "cdn.syndication.twimg.com" {
			return response(200, body), nil
		}
		return response(503, ""), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "猫 & 狗 <3 看 https://example.com/编的页面"; r.Text.Value != want {
		t.Fatalf("正文：%q", r.Text.Value)
	}
}
