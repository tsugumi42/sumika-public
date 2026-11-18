package linkreader

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// tiktokRehydrationHTML 把夹具的 __DEFAULT_SCOPE__ 包回页面；属性顺序故意与实测页不同（type 在前）。
func tiktokRehydrationHTML(scope string) string {
	return `<!doctype html><html><head><script type="application/json" id="__UNIVERSAL_DATA_FOR_REHYDRATION__" crossorigin="anonymous">{"__DEFAULT_SCOPE__":` + scope + `}</script></head><body></body></html>`
}

func tiktokFixturePage(t *testing.T, name string) string {
	t.Helper()
	return tiktokRehydrationHTML(fixture(t, name))
}

func TestTikTokReplayVideo(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || req.URL.RawQuery != "" {
			t.Fatalf("not anonymous or query kept: %s", req.URL)
		}
		if req.URL.Path != "/@hankgreen1/video/7000000000000000001" {
			t.Fatalf("path: %s", req.URL.Path)
		}
		if got := req.Header.Get("Referer"); got != "https://www.tiktok.com/" {
			t.Fatalf("referer: %q", got)
		}
		return response(200, tiktokFixturePage(t, "tiktok-video")), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@hankgreen1/video/7000000000000000001?is_copy_url=1&x=keep"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Platform != "tiktok" || r.Kind != "video" || r.ID != "7000000000000000001" || r.CleanURL != "https://www.tiktok.com/@hankgreen1/video/7000000000000000001?x=keep" {
		t.Fatalf("identity: %+v", r)
	}
	if r.Author.Name.Value != "Hank Green" || r.Author.Account.Value != "hankgreen1" || r.Author.ID.Value != "7000000000000000002" {
		t.Fatalf("author: %+v", r.Author)
	}
	if want := time.Unix(1640896362, 0).UTC(); !r.Published.Value.Equal(want) {
		t.Fatalf("published: %v", r.Published)
	}
	// 实测视频 desc 为空：正文如实标缺失，不生成空文本块。
	if r.Text.State != "not_returned" || r.Text.Reason != "empty_text" {
		t.Fatalf("text: %+v", r.Text)
	}
	for k, want := range map[string]int64{"views": 1400000, "likes": 270400, "comments": 4544, "shares": 5149, "collects": 4094} {
		if got := r.Stats[k]; got.State != "available" || got.Value != want {
			t.Fatalf("stat %s: %+v", k, got)
		}
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].Type != "video" || r.Media.Value[0].Duration.Value != 21 || r.Media.Value[0].Width.Value != 576 || r.Media.Value[0].Height.Value != 1024 {
		t.Fatalf("media: %+v", r.Media)
	}
	if m := r.Media.Value[0]; m.URL.State != "unsupported" || m.URL.Reason != "session_cookie_required" || m.Thumbnail.State != "available" {
		t.Fatalf("video media url/thumbnail: %+v", m)
	}
	if len(r.Blocks) != 1 || r.Blocks[0].Type != "video" {
		t.Fatalf("blocks: %+v", r.Blocks)
	}
	if r.Title.State != "not_returned" {
		t.Fatalf("title should stay not_returned: %+v", r.Title)
	}
}

func TestTikTokReplayPhotoPathRewritten(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/@hullcity/video/7000000000000000003" {
			t.Fatalf("photo must be requested on /video/: %s", req.URL.Path)
		}
		return response(200, tiktokFixturePage(t, "tiktok-photo")), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://m.tiktok.com/@HullCity/photo/7000000000000000003"))
	if err != nil {
		t.Fatal(err)
	}
	if r.CleanURL != "https://www.tiktok.com/@hullcity/video/7000000000000000003" || r.Text.Value != "#fixture #photo #tiktok" {
		t.Fatalf("photo: %+v", r)
	}
	if len(r.Media.Value) != 2 || r.Media.Value[0].Type != "image" || r.Media.Value[1].Type != "image" {
		t.Fatalf("media: %+v", r.Media)
	}
	first, second := r.Media.Value[0], r.Media.Value[1]
	if first.URL.Value != "https://p16-common-sign.tiktokcdn-us.com/fixture/photo-1.jpeg" || first.Width.Value != 1080 || first.Height.Value != 1920 {
		t.Fatalf("first image: %+v", first)
	}
	if second.URL.Value != "https://p16-common-sign.tiktokcdn-us.com/fixture/photo-2.jpeg" || second.Height.Value != 1440 {
		t.Fatalf("second image: %+v", second)
	}
	if r.Stats["likes"].Value != 4072 || r.Stats["views"].Value != 34500 {
		t.Fatalf("photo stats: %+v", r.Stats)
	}
	for _, m := range r.Media.Value {
		if m.Duration.State == "available" {
			t.Fatalf("photo must not carry video duration: %+v", m)
		}
	}
}

func TestTikTokReplayUser(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/@tiktok" {
			t.Fatalf("user path: %s", req.URL.Path)
		}
		return response(200, tiktokFixturePage(t, "tiktok-user")), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@TikTok"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "user" || r.Title.Value != "TikTok" || r.Author.Account.Value != "tiktok" || r.Author.ID.Value != "7000000000000000006" || r.Author.Avatar.State != "available" {
		t.Fatalf("user: %+v", r)
	}
	if r.Text.Value != "Your next discovery is waiting on TikTok." {
		t.Fatalf("signature: %+v", r.Text)
	}
	want := map[string]int64{"followers": 10001, "following": 2, "heart": 123456789, "videos": 1515}
	for k, v := range want {
		if got := r.Stats[k]; got.State != "available" || got.Value != v {
			t.Fatalf("stat %s: %+v", k, got)
		}
	}
	if r.Flags["verified"].Value != true {
		t.Fatalf("verified: %+v", r.Flags)
	}
}

func TestTikTokFailureShapes(t *testing.T) {
	video := fixture(t, "tiktok-video")
	for _, c := range []struct {
		name string
		resp func(*http.Request) (*http.Response, error)
		kind string
	}{
		{"not_found_404", func(*http.Request) (*http.Response, error) { return response(404, ""), nil }, "not_found"},
		{"gone_410", func(*http.Request) (*http.Response, error) { return response(410, ""), nil }, "not_found"},
		{"rate_429", func(*http.Request) (*http.Response, error) { return response(429, ""), nil }, "rate_limited"},
		{"forbidden_403", func(*http.Request) (*http.Response, error) { return response(403, ""), nil }, "not_returned"},
		{"login_redirect", func(*http.Request) (*http.Response, error) {
			r := response(302, "")
			r.Header.Set("Location", "https://www.tiktok.com/login?redirect_url=x")
			return r, nil
		}, "login_required"},
		{"other_redirect", func(*http.Request) (*http.Response, error) {
			r := response(302, "")
			r.Header.Set("Location", "https://www.tiktok.com/")
			return r, nil
		}, "not_returned"},
		{"status_login_10216", func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"statusCode":10216}}`)), nil
		}, "login_required"},
		{"status_login_10222", func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"statusCode":10222}}`)), nil
		}, "login_required"},
		{"status_ip_10204", func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"statusCode":10204}}`)), nil
		}, "rate_limited"},
		{"status_other_10201", func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"statusCode":10201}}`)), nil
		}, "not_returned"},
		{"status_missing", func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"itemInfo":{}}}`)), nil
		}, "invalid_response"},
		{"no_rehydration", func(*http.Request) (*http.Response, error) { return response(200, "<html><body>x</body></html>"), nil }, "invalid_response"},
		{"malformed_json", func(*http.Request) (*http.Response, error) {
			return response(200, `<script id="__UNIVERSAL_DATA_FOR_REHYDRATION__">{"__DEFAULT_SCOPE__": {</script>`), nil
		}, "invalid_response"},
		{"challenge_page", func(*http.Request) (*http.Response, error) {
			return response(200, `<html><body><div id="cs">challenge</div></body></html>`), nil
		}, "not_returned"},
		{"item_missing", func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"statusCode":0,"itemInfo":{}}}`)), nil
		}, "not_returned"},
		{"identity_mismatch", func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(strings.Replace(video, "7000000000000000001", "7000000000000000009", 1))), nil
		}, "identity_mismatch"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, c.resp)
			_, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@hankgreen1/video/7000000000000000001"))
			wantError(t, err, c.kind)
		})
	}
	t.Run("user_identity_mismatch", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, tiktokFixturePage(t, "tiktok-user")), nil
		})
		_, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@someoneelse"))
		wantError(t, err, "identity_mismatch")
	})
}

func TestTikTokEdgeShapes(t *testing.T) {
	t.Run("very_long_text", func(t *testing.T) {
		long := strings.Repeat("长", 900)
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(strings.Replace(fixture(t, "tiktok-video"), `"desc": ""`, `"desc": "`+long+`"`, 1))), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@hankgreen1/video/7000000000000000001"))
		if err != nil {
			t.Fatal(err)
		}
		if !r.Text.Truncated || len([]rune(r.Text.Value)) != 800 {
			t.Fatalf("long text: truncated=%v runes=%d", r.Text.Truncated, len([]rune(r.Text.Value)))
		}
	})
	t.Run("create_time_zero", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"statusCode":0,"itemInfo":{"itemStruct":{"id":"7000000000000000001","desc":"x","createTime":"0","video":{"duration":1}}}}}`)), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@hankgreen1/video/7000000000000000001"))
		if err != nil {
			t.Fatal(err)
		}
		// createTime 为 0 不是发布时间，不能落成 1970 年。
		if r.Published.State != "not_returned" {
			t.Fatalf("published from zero createTime: %+v", r.Published)
		}
	})
	t.Run("zero_images", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"statusCode":0,"itemInfo":{"itemStruct":{"id":"7000000000000000003","desc":"x","createTime":1,"author":{"id":"7000000000000000005","uniqueId":"hullcity","nickname":"hullcity"},"imagePost":{"images":[]}}}}}`)), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@hullcity/video/7000000000000000003"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Media.State != "available" || len(r.Media.Value) != 0 || len(r.Blocks) != 1 || r.Blocks[0].Type != "text" {
			t.Fatalf("zero media: %+v blocks=%+v", r.Media, r.Blocks)
		}
	})
	t.Run("optional_fields_missing", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, tiktokRehydrationHTML(`{"webapp.video-detail":{"statusCode":0,"itemInfo":{"itemStruct":{"id":"7000000000000000001","desc":"","video":{"duration":0}}}}}`)), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@hankgreen1/video/7000000000000000001"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Author.Name.State != "not_returned" || r.Published.State != "not_returned" || r.Stats["likes"].State != "not_returned" {
			t.Fatalf("missing optional should stay not_returned: %+v", r)
		}
		// 显式的 0 时长和缺失分开：这里是 0 秒，状态为 available。
		if r.Media.Value[0].Duration.State != "available" || r.Media.Value[0].Duration.Value != 0 {
			t.Fatalf("zero duration: %+v", r.Media.Value[0].Duration)
		}
	})
}

func TestTikTokShortLinks(t *testing.T) {
	for _, c := range []struct{ short, target, clean string }{
		{"https://vm.tiktok.com/ZMAAAAAAA/", "https://www.tiktok.com/@hankgreen1/video/7000000000000000001?is_from_webapp=v1&x=keep", "https://www.tiktok.com/@hankgreen1/video/7000000000000000001?x=keep"},
		{"https://vt.tiktok.com/ZSAAAAAAA", "https://m.tiktok.com/@hullcity/photo/7000000000000000003", "https://www.tiktok.com/@hullcity/video/7000000000000000003"},
		{"https://www.tiktok.com/t/ZTAAAAAAA/", "https://www.tiktok.com/@tiktok", "https://www.tiktok.com/@tiktok"},
	} {
		t.Run(c.short, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) {
				resp := response(302, "")
				resp.Header.Set("Location", c.target)
				return resp, nil
			})
			got, err := f.Resolve(context.Background(), link(t, c.short))
			if err != nil || got.Clean != c.clean || f.Requests() != 1 {
				t.Fatalf("resolve: %+v %v requests=%d", got, err, f.Requests())
			}
		})
	}
	t.Run("dead_home_redirect", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			resp := response(302, "")
			resp.Header.Set("Location", "https://www.tiktok.com/?_r=1")
			return resp, nil
		})
		_, err := f.Resolve(context.Background(), link(t, "https://vm.tiktok.com/ZMAAAAAAA/"))
		wantError(t, err, "not_found")
	})
	// 域内但不是支持的内容页（标签页、普通首页）报 unsupported；look-alike 主机仍是 unsafe_target。
	for _, c := range []struct{ name, location, kind string }{
		{"in_domain_tag_page", "https://www.tiktok.com/tag/fyp", "unsupported"},
		{"plain_home_without_r", "https://www.tiktok.com/", "unsupported"},
		{"look_alike_tag_page", "https://www.tiktok.com.evil.example/tag/fyp", "unsafe_target"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) {
				resp := response(302, "")
				resp.Header.Set("Location", c.location)
				return resp, nil
			})
			_, err := f.Resolve(context.Background(), link(t, "https://vm.tiktok.com/ZMAAAAAAA/"))
			wantError(t, err, c.kind)
		})
	}
}

func TestTikTokHostPolicy(t *testing.T) {
	fake := netip.MustParseAddr("198.18.0.7")
	for _, c := range []struct {
		host string
		ok   bool
	}{{"www.tiktok.com", true}, {"vm.tiktok.com", true}, {"vt.tiktok.com", true}, {"www.tiktok.com.evil.example", false}, {"tiktokcdn.example.com", false}} {
		if got := allowedWithProxy(c.host, fake); got != c.ok {
			t.Fatalf("fake-ip %s: got %v want %v", c.host, got, c.ok)
		}
	}
	if moreMediaHost("p16-common-sign.tiktokcdn-us.com") != true || moreMediaHost("example.tiktokcdn.evil.example") {
		t.Fatal("tiktok CDN host allowlist")
	}
	// 实测（2026-11-13）TikTok 封面、图片是 p16-common-sign、头像是 p19-common-sign（都在 tiktokcdn-us.com 下）。
	// 不再为这两个主机单独写死 fake-ip 放行：它们落在下载许可名单的后缀项 tiktokcdn-us.com 里，由通用规则（有代理 = 经代理按主机名连）覆盖；
	// 同域其他名字也在名单里，假后缀和名单外的主机不在。
	for _, c := range []struct {
		host string
		ok   bool
	}{
		{"p16-common-sign.tiktokcdn-us.com", true}, {"p19-common-sign.tiktokcdn-us.com", true},
		{"p16-sign-va.tiktokcdn.com", true}, {"p20-common-sign.tiktokcdn-us.com", true}, {"tiktokcdn-us.com", true},
		{"p16-common-sign.tiktokcdn-us.com.evil.example", false}, {"p16-common-sign.tiktokcdn-us.evil.example", false}, {"tiktokcdn.example.com", false},
	} {
		if got := allowedWithProxy(c.host, fake); got != c.ok {
			t.Fatalf("fake-ip CDN %s: got %v want %v", c.host, got, c.ok)
		}
	}
	if got := requestPlatform("https://vm.tiktok.com/ZMAAAAAAA/"); got != "tiktok" {
		t.Fatalf("rate platform: %s", got)
	}
	if got := requestPlatform("https://www.tiktok.com/@hankgreen1/video/7000000000000000001"); got != "tiktok" {
		t.Fatalf("rate platform www: %s", got)
	}
}

// 个别字段类型不对或值不合法时只让这一个字段缺失，页面其余内容照出，缺的如实写原因（不当 0、不整页失败）。
func TestTikTokBadFieldKeepsPage(t *testing.T) {
	video := tiktokFixturePage(t, "tiktok-video")
	for _, c := range []struct {
		name  string
		page  string
		check func(t *testing.T, r Result)
	}{
		{"count_not_digits", strings.Replace(video, `"270400"`, `"27k"`, 1), func(t *testing.T, r Result) {
			if r.Stats["likes"].State == "available" || r.Stats["likes"].Reason != "invalid_value" || r.Stats["views"].State != "available" {
				t.Fatalf("stats: %+v", r.Stats)
			}
		}},
		{"count_negative", strings.Replace(video, `"270400"`, `"-1"`, 1), func(t *testing.T, r Result) {
			if r.Stats["likes"].State == "available" || r.Stats["likes"].Reason != "invalid_value" {
				t.Fatalf("stats: %+v", r.Stats)
			}
		}},
		{"nickname_is_number", strings.Replace(video, `"nickname": "Hank Green"`, `"nickname": 123`, 1), func(t *testing.T, r Result) {
			if r.Author.Name.State == "available" || r.Author.Account.State != "available" || r.Stats["likes"].State != "available" {
				t.Fatalf("author: %+v stats: %+v", r.Author, r.Stats)
			}
		}},
		{"duration_is_text", strings.Replace(video, `"duration": 21`, `"duration": "21"`, 1), func(t *testing.T, r Result) {
			if len(r.Media.Value) != 1 || r.Media.Value[0].Duration.State == "available" || r.Media.Value[0].Thumbnail.State != "available" {
				t.Fatalf("media: %+v", r.Media)
			}
		}},
		{"create_time_bad", strings.Replace(video, `"createTime": "1640896362"`, `"createTime": "x1640896362"`, 1), func(t *testing.T, r Result) {
			if r.Published.State == "available" || r.Published.Reason != "invalid_value" || r.Stats["likes"].State != "available" {
				t.Fatalf("published: %+v", r.Published)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.page == video {
				t.Fatal("夹具没替换成功")
			}
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, c.page), nil })
			r, err := f.Fetch(context.Background(), link(t, "https://www.tiktok.com/@hankgreen1/video/7000000000000000001"))
			if err != nil {
				t.Fatalf("单个字段坏了不该整页失败：%v", err)
			}
			c.check(t, r)
		})
	}
}

// 回归：本机 DNS 是 fake-ip 时，TikTok 的封面、图片和头像不能被当成 unsafe_target 丢掉（有代理可走）；
// 没有代理时明确拒绝；媒体主机名单外的名字仍然被拒。
func TestTikTokMediaSurvivesFakeIPDNS(t *testing.T) {
	fakeLookup := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.18.5.237")}, nil
	}
	build := func() Result {
		r := Result{
			Media: available([]Media{{
				Type:      "video",
				Thumbnail: available("https://p16-common-sign.tiktokcdn-us.com/tos-test/cover~tplv-test.image"),
			}, {
				Type: "image",
				URL:  available("https://p16-common-sign.tiktokcdn-us.com/tos-test/photo~tplv-test.jpeg"),
			}, {
				Type: "image",
				URL:  available("https://p16-sign-va.tiktokcdn.evil.example/tos-test/other.jpeg"),
			}}),
		}
		r.Author.Avatar = available("https://p19-common-sign.tiktokcdn-us.com/tos-test/avatar~tplv-test.jpeg")
		return r
	}
	r := build()
	(&Fetcher{lookup: fakeLookup, policy: testPolicy(true)}).inspectMedia(context.Background(), &r)
	if r.Media.Value[0].Thumbnail.State != "available" || r.Media.Value[1].URL.State != "available" || r.Author.Avatar.State != "available" {
		t.Fatalf("tiktok media dropped under fake-ip: %+v avatar=%+v", r.Media.Value, r.Author.Avatar)
	}
	if got := r.Media.Value[2].URL; got.State != "failed" || got.Reason != "unsafe_target" {
		t.Fatalf("unlisted CDN name must stay rejected: %+v", got)
	}
	r = build()
	(&Fetcher{lookup: fakeLookup, policy: testPolicy(false)}).inspectMedia(context.Background(), &r)
	if r.Media.Value[0].Thumbnail.State != "failed" || r.Media.Value[1].URL.State != "failed" || r.Author.Avatar.State != "failed" {
		t.Fatalf("no proxy: fake-ip media must be refused: %+v avatar=%+v", r.Media.Value, r.Author.Avatar)
	}
}
