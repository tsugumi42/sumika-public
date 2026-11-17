package linkreader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/netsafe"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, e := os.ReadFile("testdata/" + name + ".json")
	if e != nil {
		t.Fatal(e)
	}
	return string(bytes.TrimPrefix(b, []byte{239, 187, 191}))
}

// fxOnly 让 vx 和 syndication 两条线路一律 503，不交给 rt：这些测试只验 fx_v2 的映射。
// X 的线路是同时取的（twitter_routes.go），vx 那一路照样会发一个请求，f.Requests() 里算它一次。
func fxOnly(rt roundTripFunc) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		if h := r.URL.Hostname(); h == "api.vxtwitter.com" || h == "cdn.syndication.twimg.com" {
			return response(503, ""), nil
		}
		return rt(r)
	}
}
func testFetcher(t *testing.T, rt roundTripFunc) *Fetcher {
	t.Helper()
	f, e := NewFetcher(FetchOptions{Transport: rt})
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func link(t *testing.T, raw string) Link {
	t.Helper()
	l, ok := Recognize(raw)
	if !ok {
		t.Fatal(raw)
	}
	return l
}
func wantError(t *testing.T, err error, kind string) {
	t.Helper()
	var e *FetchError
	if !errors.As(err, &e) || e.Kind != kind {
		t.Fatalf("want %s; got %v", kind, err)
	}
}
func TestXReplay(t *testing.T) {
	cases := []struct {
		name, id, author, contains string
	}{
		{name: "plain", id: "1900000000000000014", author: "10001", contains: "编造的示例帖子正文"},
		{name: "photo", id: "1900000000000000008", author: "10001", contains: "编造的示例帖子正文"},
		{name: "four", id: "1900000000000000011", author: "10002"},
		{name: "video", id: "1900000000000000013", author: "10001", contains: "编造的示例帖子正文"},
		{name: "quote", id: "1900000000000000005", author: "10003"},
		{name: "thread", id: "1900000000000000012", author: "10004"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, fxOnly(func(req *http.Request) (*http.Response, error) {
				if c.name == "quote" && req.URL.Host == "api.fxtwitter.com" && req.URL.Path == "/2/status/1900000000000000004" {
					// 补“引用里还有引用”时会多取被引的那一条；这里没有它的夹具，按取不到处理
					return response(404, `{}`), nil
				}
				// 传输函数跑在取数的 goroutine 里，不能 t.Fatal
				if req.URL.Host != "api.fxtwitter.com" || req.URL.Path != "/2/status/"+c.id {
					t.Errorf("unexpected API %s", req.URL)
					return response(500, ""), nil
				}
				if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
					t.Errorf("headers")
					return response(500, ""), nil
				}
				return response(200, fixture(t, "x-"+c.name)), nil
			}))
			r, e := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/"+c.id+"/photo/1?s=20"))
			if e != nil {
				t.Fatal(e)
			}
			if r.ID != c.id || r.Author.ID.Value != c.author || r.Source != "fx_v2" || r.Text.State != "available" || !strings.Contains(r.Text.Value, c.contains) {
				t.Fatalf("wrong content: %+v", r)
			}
			if c.name == "plain" && r.Author.Verification.Value != VerifyBusiness {
				// 真实夹具里机构金标的种类是 organization，不是 business
				t.Fatalf("机构金标没认出来：%+v", r.Author.Verification)
			}
			if c.name == "plain" && (r.Published.Value.Unix() != 1793289623 || r.Stats["replies"].State != "available" || r.Stats["replies"].Value != 0 || r.Stats["views"].Value != 22360) {
				t.Fatal("timestamp or zero statistics lost")
			}
			if c.name == "four" {
				if len([]rune(r.Text.Value)) != 2156 || len(r.Media.Value) != 4 || r.Media.Value[0].Width.Value != 2274 || r.Media.Value[0].Height.Value != 2842 || !strings.Contains(r.Media.Value[0].URL.Value, "name=orig") {
					t.Fatalf("long Note or original photo lost")
				}
			}
			if c.name == "video" {
				m := r.Media.Value[0]
				if m.Duration.Value != 22.633 || m.Width.Value != 1080 || m.Height.Value != 1920 || !strings.Contains(m.URL.Value, "1080x1920") || m.Size.State != "not_returned" {
					t.Fatalf("video: %+v", m)
				}
			}
			if c.name == "photo" && len(r.Media.Value) != 1 {
				t.Fatal("single photo")
			}
			if c.name == "quote" {
				if r.Quote.State != "available" || r.Quote.Value.ID != "1900000000000000004" || r.Quote.Value.Author.ID.State != "available" {
					t.Fatal("quote identity")
				}
			}
			if c.name == "thread" && (r.ReplyTo.Value != "1900000000000000010" || r.Thread.State != "unsupported") {
				t.Fatal("self reply is not full thread")
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(404, fixture(t, "x-missing")), nil })
		_, e := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/2115477952339999999"))
		wantError(t, e, "not_found")
	})
}

// fx_v2 和 vx 同时取（规格-链接发送改版 C2）：fx 不管怎么失败，vx 成功就用 vx 的；
// 两条都失败时报 fx 那条“答上来了”的原因（404、403、429、坏响应），fx 也没答上来（5xx）才报别的。
func TestXFallbackPolicy(t *testing.T) {
	for _, c := range []struct {
		status int
		kind   string
	}{{500, ""}, {404, "not_found"}, {403, "login_required"}, {429, "rate_limited"}, {200, "invalid_response"}} {
		t.Run(http.StatusText(c.status), func(t *testing.T) {
			for _, vxOK := range []bool{true, false} {
				var calls atomic.Int32
				f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					switch req.URL.Host {
					case "api.vxtwitter.com":
						if vxOK {
							return response(200, fixture(t, "vx-video")), nil
						}
						return response(503, ""), nil
					case "cdn.syndication.twimg.com":
						return response(503, ""), nil
					}
					return response(c.status, "bad"), nil
				})
				r, e := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1900000000000000013/video/1"))
				switch {
				case vxOK:
					if e != nil || calls.Load() != 2 || r.Source != "vx" || r.Author.ID.State != "not_returned" || r.Media.Value[0].Duration.Value != 22.633 || r.Media.Value[0].Size.State != "not_returned" {
						t.Fatalf("vx 成功却没用它：%v %+v calls=%d", e, r, calls.Load())
					}
				case c.kind != "":
					wantError(t, e, c.kind)
					var fe *FetchError
					if errors.As(e, &fe); fe.Source != "fx_v2" || !strings.Contains(fe.Trail, "vx interface_error") {
						t.Fatalf("应报 fx 的原因、线路里有 vx：%+v", fe)
					}
				default:
					wantError(t, e, "interface_error")
				}
			}
		})
	}
}

func TestBiliReplay(t *testing.T) {
	for _, c := range []struct {
		name, url, title, uid, cid string
		part, duration             int
	}{{"single", "https://www.bilibili.com/video/BV1xx411c7mD", "编造的示例视频甲", "900000000000090007", "900000000000090004", 1, 213}, {"multi", "https://www.bilibili.com/video/av900000000000090002?p=2", "编造的示例视频乙", "900000000000090008", "900000000000090005", 2, 205}} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path == "/x/tag/archive/tags" {
					return response(200, `{"code":0,"data":[{"tag_name":"音乐"}]}`), nil
				}
				if req.URL.Path == "/x/v2/reply" {
					return response(200, `{"code":0,"data":{"page":{"count":0},"replies":null}}`), nil
				}
				return response(200, fixture(t, "bili-"+c.name)), nil
			})
			r, e := f.Fetch(context.Background(), link(t, c.url))
			if e != nil {
				t.Fatal(e)
			}
			if r.Title.Value != c.title || r.Author.ID.Value != c.uid || r.SelectedPart.Value.CID != c.cid || r.SelectedPart.Value.Number != c.part || r.SelectedPart.Value.Duration.Value != c.duration || r.Tags.Value[0] != "音乐" || r.Stats["view"].State != "available" || r.Flags["argue_info"].State != "available" || r.Flags["ai_generated"].State != "not_returned" || r.Comments.Reason != "no_comments" || calls != 3 {
				t.Fatalf("wrong video result: %+v calls=%d", r, calls)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, fixture(t, "bili-missing")), nil })
		_, e := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/video/av99999999999999999"))
		wantError(t, e, "not_found")
	})
	for _, p := range []string{"0", "11", "bad", "1&p=2"} {
		t.Run("invalid_part_"+p, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, fixture(t, "bili-multi")), nil })
			_, e := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/video/av900000000000090002?p="+p))
			wantError(t, e, "invalid_part")
		})
	}
	t.Run("tag_partial", func(t *testing.T) {
		f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/x/tag/archive/tags" {
				return response(403, ""), nil
			}
			return response(200, fixture(t, "bili-single")), nil
		})
		r, e := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/video/BV1xx411c7mD"))
		if e != nil || r.Title.State != "available" || r.Tags.State != "login_required" {
			t.Fatalf("partial result: %v %+v", e, r)
		}
	})
	t.Run("episode", func(t *testing.T) {
		// 番剧不能走 UGC 视频的 view 接口，要走自己的 pgc 接口（返回不对就如实报）
		var paths []string
		f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
			paths = append(paths, r.URL.Path)
			return response(200, "{}"), nil
		})
		_, e := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/bangumi/play/ep317925"))
		wantError(t, e, "invalid_response")
		if len(paths) != 1 || paths[0] != "/pgc/view/web/season" {
			t.Fatalf("番剧请求了 %v", paths)
		}
	})
}
func TestResponseGuards(t *testing.T) {
	for _, c := range []struct {
		name, body, kind string
		max              int64
	}{{"html", "<html>", "invalid_response", 4096}, {"wrong_type", `{"code":200,"status":{"id":123,"text":[]}}`, "invalid_response", 4096}, {"null", "null", "invalid_response", 4096}, {"oversize", strings.Repeat("x", 65), "response_too_large", 64}, {"mismatch", `{"code":200,"status":{"id":"2","text":"a"}}`, "identity_mismatch", 4096}} {
		t.Run(c.name, func(t *testing.T) {
			f, e := NewFetcher(FetchOptions{MaxBody: c.max, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, c.body), nil })})
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1"))
			wantError(t, e, c.kind)
		})
	}
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		base := http.DefaultTransport.(*http.Transport).Clone()
		f, e := NewFetcher(FetchOptions{Timeout: 20 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			copy := r.Clone(r.Context())
			copy.URL.Scheme = "http"
			copy.URL.Host = strings.TrimPrefix(server.URL, "http://")
			return base.RoundTrip(copy)
		})})
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1"))
		wantError(t, e, "timeout")
	})
}
func TestShortRedirectGuards(t *testing.T) {
	for _, c := range []struct{ name, target, kind string }{{"external", "https://evil.example/a", "unsafe_target"}, {"file", "file:///etc/passwd", "unsafe_target"}, {"private", "http://127.0.0.1/a", "unsafe_target"}, {"credentials", "https://a:b@x.com/AP/status/1", "unsafe_target"}, {"loop", "https://t.co/a", "redirect_loop"}, {"unsupported", "https://open.spotify.com/show/aaaaaaaaaaaaaaaaaaaaaa", "unsupported"}, {"valid", "https://twitter.com/AP/status/1?s=20&t=x", ""}} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
				calls++
				resp := response(302, "")
				resp.Header.Set("Location", c.target)
				return resp, nil
			})
			l, e := f.Resolve(context.Background(), link(t, "https://t.co/a"))
			if c.kind != "" {
				wantError(t, e, c.kind)
			} else if e != nil || l.Clean != "https://x.com/AP/status/1" {
				t.Fatalf("resolve %v %+v", e, l)
			}
			if calls != 1 {
				t.Fatalf("untrusted target connected: %d", calls)
			}
		})
	}
	t.Run("hop_limit", func(t *testing.T) {
		calls := 0
		f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
			calls++
			resp := response(302, "")
			resp.Header.Set("Location", "https://b23.tv/"+strings.Repeat("a", calls))
			return resp, nil
		})
		_, e := f.Resolve(context.Background(), link(t, "https://b23.tv/start"))
		wantError(t, e, "too_many_redirects")
		if calls != 5 {
			t.Fatalf("hops=%d", calls)
		}
	})
	t.Run("no_html_follow", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			r := response(200, `<meta http-equiv="refresh">`)
			r.Header.Set("Location", "https://x.com/AP/status/1")
			return r, nil
		})
		_, e := f.Resolve(context.Background(), link(t, "https://b23.tv/a"))
		wantError(t, e, "invalid_redirect")
	})
}
func TestPublicAddressGuards(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "0.0.0.0", "224.0.0.1", "100.64.1.1", "192.0.2.1", "::1", "::ffff:127.0.0.1", "fe80::1", "fc00::1", "2001:db8::1"} {
		if netsafe.PublicIP(netip.MustParseAddr(s)) {
			t.Fatalf("accepted %s", s)
		}
	}
	if !netsafe.PublicIP(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public refused")
	}
	c, e := safeDial(nil, func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}, netPolicy{})(context.Background(), "tcp", "127.0.0.1:80")
	if c != nil {
		c.Close()
		t.Fatal("connected to private address")
	}
	wantError(t, e, "unsafe_target")
}
func TestQuoteGuards(t *testing.T) {
	text := "q"
	a := &xPost{ID: "1", Text: &text}
	a.Quote = a
	r, e := mapX(Link{Platform: "x", Kind: "post", ID: "1"}, a, 0, map[string]bool{})
	if e != nil || r.Quote.State != "failed" || r.Quote.Reason != "quote_cycle" {
		t.Fatalf("quote cycle: %v %+v", e, r.Quote)
	}
	a.Quote = nil
	current := a
	for i := range 5 {
		next := &xPost{ID: strings.Repeat("2", i+1), Text: &text}
		current.Quote = next
		current = next
	}
	r, e = mapX(Link{Platform: "x", Kind: "post", ID: "1"}, a, 0, map[string]bool{})
	if e != nil {
		t.Fatal(e)
	}
	q := r.Quote.Value
	if q.Quote.State != "not_returned" || q.Quote.Reason != "deeper_quote_omitted" || !q.MoreQuotes.Value || q.Quote.Value != nil {
		t.Fatal("deeper quote not marked or retained")
	}
}
