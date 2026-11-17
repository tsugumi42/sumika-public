package linkreader

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// 匿名访客标识（finger/spi 取到的 buvid3、buvid4）：浏览器打开主页就会拿一份。第一次请求 B 站 API 时取一次，缓存六小时，
// 之后带在所有发往 B 站 API 主机的请求上；不登录、不借别人的 cookie、不本地伪造。

type visitorServer struct {
	spi      string // finger/spi 的响应体
	spiCalls int
	spiCode  int // 不为 0 时 spi 回这个 HTTP 状态码
	seen     []string
	cookies  []string
}

func (s *visitorServer) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/x/frontend/finger/spi" {
		s.spiCalls++
		if r.Header.Get("Cookie") != "" {
			panic("取访客标识的请求自己不能带 Cookie")
		}
		if s.spiCode != 0 {
			return response(s.spiCode, "{}"), nil
		}
		return response(200, s.spi), nil
	}
	s.seen = append(s.seen, r.URL.Host+r.URL.Path)
	s.cookies = append(s.cookies, r.Header.Get("Cookie"))
	return response(200, `{"code":0,"data":{}}`), nil
}

const spiOK = `{"code":0,"data":{"b_3":"fakeb3","b_4":"fakeb4"}}`

func newVisitor(t *testing.T, s *visitorServer, now *time.Time) *Fetcher {
	t.Helper()
	visitorOn(t)
	f, err := NewFetcher(FetchOptions{Transport: s, Rate: RateOptions{Now: func() time.Time { return *now }}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestBiliVisitorCookieFetchedOnceAndAttached(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := &visitorServer{spi: spiOK}
	f := newVisitor(t, s, &now)
	ctx := context.Background()
	for range 3 {
		if _, _, _, err := f.request(ctx, "https://api.bilibili.com/x/web-interface/view?bvid=BV1GJ411x7h7", "test"); err != nil {
			t.Fatal(err)
		}
	}
	if s.spiCalls != 1 {
		t.Fatalf("六小时内只取一次访客标识：%d", s.spiCalls)
	}
	for i, c := range s.cookies {
		if c != "buvid3=fakeb3; buvid4=fakeb4" {
			t.Fatalf("第 %d 个请求 Cookie=%q", i, c)
		}
	}
	// 直播接口也是 api. 开头的 B 站 API 主机
	if _, _, _, err := f.request(ctx, "https://api.live.bilibili.com/room/v1/Room/get_info?room_id=1", "test"); err != nil || s.cookies[len(s.cookies)-1] == "" {
		t.Fatalf("直播 API 也该带：%v %q", err, s.cookies)
	}
	now = now.Add(6*time.Hour + time.Second)
	if _, _, _, err := f.request(ctx, "https://api.bilibili.com/x/web-interface/view", "test"); err != nil || s.spiCalls != 2 {
		t.Fatalf("六小时后重取：calls=%d err=%v", s.spiCalls, err)
	}
}

// Cookie 只发给 B 站的 API 主机：页面、小店、短链、图床、别的平台一律不带，也不会因为它们去取访客标识。
func TestBiliVisitorCookieOnlyForAPIHosts(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := &visitorServer{spi: spiOK}
	f := newVisitor(t, s, &now)
	for _, raw := range []string{
		"https://www.bilibili.com/read/cv1/",
		"https://mall.bilibili.com/mall-up-search/items/info?itemsId=1",
		"https://space.bilibili.com/1",
		"https://b23.tv/abc",
		"https://i0.hdslb.com/bfs/archive/a.jpg",
		"https://api.github.com/repos/a/b",
		"https://api.fxtwitter.com/i/status/1",
		"https://example.com/",
	} {
		if _, _, _, err := f.request(context.Background(), raw, "test"); err != nil {
			t.Fatal(raw, err)
		}
	}
	if s.spiCalls != 0 {
		t.Fatalf("非 API 主机不该触发取访客标识：%d", s.spiCalls)
	}
	for i, c := range s.cookies {
		if c != "" {
			t.Fatalf("%s 带了 Cookie：%q", s.seen[i], c)
		}
	}
}

// 取不到访客标识不挡主请求；失败后 5 分钟内不再取，之后再试。取到的值里有分号、空格、换行这类的不用。
func TestBiliVisitorFailureDoesNotBlock(t *testing.T) {
	for name, body := range map[string]string{"接口错误": `{"code":-1}`, "值不合法": `{"code":0,"data":{"b_3":"a; b=c","b_4":"x"}}`, "不是 JSON": `<html>`} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
			s := &visitorServer{spi: body}
			f := newVisitor(t, s, &now)
			ctx := context.Background()
			for range 3 {
				if _, _, _, err := f.request(ctx, "https://api.bilibili.com/x/web-interface/view", "test"); err != nil {
					t.Fatalf("主请求不该被访客标识挡住：%v", err)
				}
			}
			if s.spiCalls != 1 || len(s.seen) != 3 {
				t.Fatalf("失败后 5 分钟内不再取：spi=%d 主请求=%d", s.spiCalls, len(s.seen))
			}
			for _, c := range s.cookies {
				if c != "" {
					t.Fatalf("没取到就不带 Cookie：%q", c)
				}
			}
			now = now.Add(5*time.Minute + time.Second)
			s.spi = spiOK
			if _, _, _, err := f.request(ctx, "https://api.bilibili.com/x/web-interface/view", "test"); err != nil || s.spiCalls != 2 || s.cookies[len(s.cookies)-1] == "" {
				t.Fatalf("5 分钟后应重试并带上：spi=%d err=%v %q", s.spiCalls, err, s.cookies)
			}
		})
	}
}

// 取访客标识时撞了风控（412）：平台随即冷却，主请求也不发出去。
func TestBiliVisitorRiskStopsMainRequest(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := &visitorServer{spiCode: 412}
	f := newVisitor(t, s, &now)
	_, _, _, err := f.request(context.Background(), "https://api.bilibili.com/x/web-interface/view", "test")
	wantError(t, err, "rate_limited")
	if len(s.seen) != 0 || s.spiCalls != 1 {
		t.Fatalf("冷却后主请求不该发：主请求=%d spi=%d", len(s.seen), s.spiCalls)
	}
}

// 调用方明确写了 Cookie 就用它的，不覆盖也不为它取访客标识。
func TestBiliVisitorExplicitCookieWins(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := &visitorServer{spi: spiOK}
	f := newVisitor(t, s, &now)
	ctx := withHeader(context.Background(), "Cookie", "buvid3=mine")
	if _, _, _, err := f.request(ctx, "https://api.bilibili.com/x/web-interface/view", "test"); err != nil {
		t.Fatal(err)
	}
	if s.spiCalls != 0 || s.cookies[0] != "buvid3=mine" {
		t.Fatalf("spi=%d cookie=%q", s.spiCalls, s.cookies[0])
	}
}

// 生产默认（访客标识开着、预算每分钟 10 次）下取视频：view、标签、评论都带访客标识；
// 访客标识那一次请求占一个预算名额，连发 4 条视频仍然都展开成功、标签不被挤掉。
func TestVideoFetchWithVisitorCookieFitsDefaultBudget(t *testing.T) {
	visitorOn(t)
	now := time.Now()
	cookies := map[string][]string{}
	spi := 0
	f, err := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return now }}, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/x/frontend/finger/spi" {
			cookies[req.URL.Path] = append(cookies[req.URL.Path], req.Header.Get("Cookie"))
		}
		switch req.URL.Path {
		case "/x/frontend/finger/spi":
			spi++
			return response(200, spiOK), nil
		case "/x/web-interface/view":
			return response(200, fixture(t, "bili-single")), nil
		case "/x/tag/archive/tags":
			return response(200, `{"code":0,"data":[]}`), nil
		case "/x/v2/reply":
			return response(200, `{"code":0,"data":{"page":{"count":0},"replies":[]}}`), nil
		}
		t.Fatalf("不该请求：%s", req.URL)
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/video/BV1xx411c7mD"))
		if err != nil || r.Title.State != "available" || r.Tags.State != "available" {
			t.Fatalf("第 %d 条应展开成功：%v tags=%+v", i, err, r.Tags)
		}
	}
	if spi != 1 {
		t.Fatalf("访客标识只取一次：%d", spi)
	}
	for _, path := range []string{"/x/web-interface/view", "/x/tag/archive/tags", "/x/v2/reply"} {
		if len(cookies[path]) == 0 {
			t.Fatalf("%s 没被请求", path)
		}
		for _, c := range cookies[path] {
			if c != "buvid3=fakeb3; buvid4=fakeb4" {
				t.Fatalf("%s 的 Cookie=%q", path, c)
			}
		}
	}
}
