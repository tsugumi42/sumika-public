package linkreader

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestRequestPlatformBudget(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	calls := 0
	f, e := NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 2, Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(200, `{}`), nil })})
	if e != nil {
		t.Fatal(e)
	}
	request := func(raw string) error { _, _, _, e := f.request(context.Background(), raw, "offline"); return e }
	if e = request("https://t.co/one"); e != nil {
		t.Fatal(e)
	}
	if e = request("https://api.fxtwitter.com/2/status/90001"); e != nil {
		t.Fatal(e)
	}
	wantError(t, request("https://api.vxtwitter.com/example/status/90001"), "rate_limited")
	if calls != 2 || f.Requests() != 2 {
		t.Fatal("over-budget HTTP issued", calls, f.Requests())
	}
	if e = request("https://b23.tv/one"); e != nil {
		t.Fatal("platforms mixed", e)
	}
	now = now.Add(time.Minute - time.Nanosecond)
	wantError(t, request("https://api.fxtwitter.com/2/status/90001"), "rate_limited")
	now = now.Add(time.Nanosecond)
	if e = request("https://api.fxtwitter.com/2/status/90001"); e != nil {
		t.Fatal("window boundary", e)
	}
}
func TestRequest429CooldownMaximum(t *testing.T) {
	for _, c := range []struct {
		name, retry string
		seconds     int
	}{{"short", "10", 60}, {"long", "120", 120}, {"invalid", "bad", 60}, {"negative", "-1", 60}, {"date", "date", 90}} {
		t.Run(c.name, func(t *testing.T) {
			now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
			calls := 0
			retry := c.retry
			if retry == "date" {
				retry = now.Add(90 * time.Second).Format(http.TimeFormat)
			}
			f, e := NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 100, Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				r := response(429, `{}`)
				r.Header.Set("Retry-After", retry)
				return r, nil
			})})
			if e != nil {
				t.Fatal(e)
			}
			_, status, h, e := f.request(context.Background(), "https://api.fxtwitter.com/2/status/90001", "fx_v2")
			if e != nil || status != 429 || h.Get("Retry-After") != retry {
				t.Fatal(status, h, e)
			}
			now = now.Add(time.Duration(c.seconds)*time.Second - time.Nanosecond)
			// X 的冷却按主机记（TestX429CooldownPerHost），这里看同一台主机
			_, _, _, e = f.request(context.Background(), "https://api.fxtwitter.com/2/status/90002", "fx_v2")
			wantError(t, e, "rate_limited")
			if calls != 1 {
				t.Fatal("cooldown issued HTTP", calls)
			}
			now = now.Add(time.Nanosecond)
			_, status, _, e = f.request(context.Background(), "https://api.fxtwitter.com/2/status/90002", "fx_v2")
			if e != nil || status != 429 || calls != 2 {
				t.Fatal("cooldown boundary "+strconv.Itoa(c.seconds), status, calls, e)
			}
		})
	}
}
func TestFetchBudgetCountsFallbackAndShortHop(t *testing.T) {
	calls := 0
	f, e := NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 1}, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host == "t.co" {
			r := response(302, "")
			r.Header.Set("Location", cacheURL)
			return r, nil
		}
		return response(503, `{}`), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Fetch(context.Background(), link(t, "https://t.co/one"))
	wantError(t, e, "rate_limited")
	if calls != 1 {
		t.Fatal("short hop not charged", calls)
	}
	f, e = NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 1}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(503, `{}`), nil })})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Fetch(context.Background(), link(t, cacheURL))
	wantError(t, e, "rate_limited")
	if calls != 2 {
		t.Fatal("fallback not charged", calls)
	}
}

func TestCooldownRejectsBeforeLegacyInterval(t *testing.T) {
	f, err := NewFetcher(FetchOptions{MinInterval: time.Hour, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, failure("unexpected_request", "offline") })})
	if err != nil {
		t.Fatal(err)
	}
	f.rate.cooldown("x", "60")
	f.nextAt["x.com"] = time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, _, err := f.request(ctx, cacheURL, "offline"); done <- err }()
	select {
	case err := <-done:
		wantError(t, err, "rate_limited")
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("cooldown waited for legacy interval")
	}
}

// 推特卡片一次要下头像和最多八张图：X 图床的下载单独记预算，不吃掉取数接口每分钟那几次（反过来也一样）。
func TestXMediaBudgetSeparateFromAPI(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	f, e := NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 2, Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{}`), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if _, _, _, e = f.request(context.Background(), "https://api.fxtwitter.com/2/status/90001", "offline"); e != nil {
			t.Fatal(e)
		}
	}
	for range 2 {
		if e = f.Stream(context.Background(), "x", "https://pbs.twimg.com/media/fiction.png", io.Discard, &ByteBudget{Limit: 32}); e != nil {
			t.Fatal("取数预算用完不该挡住图床下载", e)
		}
	}
	// 图床自己的预算照样有上限，取数那边也照样满着
	wantError(t, f.Stream(context.Background(), "x", "https://pbs.twimg.com/media/fiction.png", io.Discard, &ByteBudget{Limit: 32}), "rate_limited")
	_, _, _, e = f.request(context.Background(), "https://api.vxtwitter.com/example/status/90001", "offline")
	wantError(t, e, "rate_limited")
	// 图床回 429 只冷却图床，不冷却取数
	f2, e := NewFetcher(FetchOptions{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "pbs.twimg.com" {
			return response(429, ""), nil
		}
		return response(200, `{}`), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	_ = f2.Stream(context.Background(), "x", "https://pbs.twimg.com/media/fiction.png", io.Discard, &ByteBudget{Limit: 32})
	if _, _, _, e = f2.request(context.Background(), "https://api.fxtwitter.com/2/status/90001", "offline"); e != nil {
		t.Fatal("图床 429 冷却到了取数", e)
	}
}

// X 名下的 fx、vx 是两家互不相干的服务：fx 回 429 只冷却 fx 的主机，同时在取的 vx 照常能发（规格 C2：vx 成功就用 vx）。
// 冷却按主机记以后，平台级的冷却（如 cooldown("x")）照样挡住所有 X 主机。
func TestX429CooldownPerHost(t *testing.T) {
	f, e := NewFetcher(FetchOptions{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "api.fxtwitter.com" {
			return response(429, ""), nil
		}
		return response(200, `{}`), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	request := func(raw string) error { _, _, _, e := f.request(context.Background(), raw, "offline"); return e }
	if e = request("https://api.fxtwitter.com/2/status/90001"); e != nil {
		t.Fatal(e)
	}
	wantError(t, request("https://api.fxtwitter.com/2/status/90002"), "rate_limited")
	if e = request("https://api.vxtwitter.com/example/status/90001"); e != nil {
		t.Fatal("fx 的 429 冷却到了 vx", e)
	}
	f.rate.cooldown("x", "60")
	wantError(t, request("https://api.vxtwitter.com/example/status/90001"), "rate_limited")
}

// 查冷却只读不建表项：平台拆开冷却时按主机查，没冷却过的主机不能在表里留空项（放回“查冷却时顺手 state(主机)”就红）
func TestHostCooldownCheckDoesNotCreateEntries(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	r, e := newRateLimiter(RateOptions{RequestsPerMinute: 1000, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 50; i++ {
		if e = r.allowHost("x", "site"+strconv.Itoa(i)+".example", true); e != nil {
			t.Fatal(e)
		}
	}
	if n := len(r.platforms); n != 1 {
		t.Fatalf("没冷却过的主机留了表项：表里 %d 项，应只有 x 一项", n)
	}
	// 冷却过的主机照样挡住，其他主机不受影响
	r.cooldownHost("x", "cool.example", "60")
	if e = r.allowHost("x", "cool.example", false); e == nil {
		t.Fatal("冷却中的主机没被挡住")
	}
	if e = r.allowHost("x", "other.example", false); e != nil {
		t.Fatal("别的主机被连累", e)
	}
}

// 回归：认不出平台的普通网页不再落在 x 名下耗 X 的每分钟预算（旧的 requestPlatform 末尾 return "x" 会让这条红）。
func TestPlainWebDoesNotSpendXBudget(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	f, e := NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 2, Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{}`), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	request := func(raw string) error { _, _, _, e := f.request(context.Background(), raw, "offline"); return e }
	for _, raw := range []string{"https://blog.example.org/a", "https://blog.example.org/b"} {
		if e = request(raw); e != nil {
			t.Fatal(e)
		}
	}
	// 同一主机自己的预算照样有上限
	wantError(t, request("https://blog.example.org/c"), "rate_limited")
	// 另一台主机、X 都不受影响
	if e = request("https://news.example.net/a"); e != nil {
		t.Fatal("别的主机被连累", e)
	}
	for range 2 {
		if e = request("https://api.fxtwitter.com/2/status/90001"); e != nil {
			t.Fatal("普通网页耗掉了 X 的预算", e)
		}
	}
	wantError(t, request("https://api.vxtwitter.com/example/status/90001"), "rate_limited")
}

// 普通网页的归法：小写主机名、不含端口、只去掉开头的 www.，子域各算各的；认得出的平台照旧按平台。
func TestRequestPlatformPlainWebKeys(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Example.COM/a":                "web@example.com",
		"https://www.example.com:8443/a":       "web@example.com",
		"https://example.com/a":                "web@example.com",
		"https://blog.example.com/a":           "web@blog.example.com",
		"https://notx.com/a":                   "web@notx.com",
		"https://x.com.evil.example/a":         "web@x.com.evil.example",
		"https://x.com/a":                      "x",
		"https://mobile.twitter.com/a":         "x",
		"https://t.co/a":                       "x",
		"https://cdn.syndication.twimg.com/a":  "x",
		"https://api.fxtwitter.com/2/status/1": "x",
		"https://api.vxtwitter.com/a/status/1": "x",
		"https://www.bilibili.com/video/BV1":   "bilibili",
		"https://raw.githubusercontent.com/a":  "web@raw.githubusercontent.com",
		"https://docs.github.com/a":            "web@docs.github.com",
		"::bad":                                "web@",
	} {
		if got := requestPlatform(raw); got != want {
			t.Errorf("%s 认成了 %q，期望 %q", raw, got, want)
		}
	}
}

// 普通网页回 429 只冷却回 429 的那台主机（平台键本身就是主机），不连累别的网页，也不碰 X。
func TestPlainWebCooldownIsPerHost(t *testing.T) {
	f, e := NewFetcher(FetchOptions{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "slow.example.org" {
			return response(429, ""), nil
		}
		return response(200, `{}`), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	request := func(raw string) error { _, _, _, e := f.request(context.Background(), raw, "offline"); return e }
	if e = request("https://slow.example.org/a"); e != nil {
		t.Fatal(e)
	}
	wantError(t, request("https://slow.example.org/b"), "rate_limited")
	for _, raw := range []string{"https://fast.example.org/a", "https://api.fxtwitter.com/2/status/90001"} {
		if e = request(raw); e != nil {
			t.Fatal(raw, "被连累", e)
		}
	}
}

// 普通网页主机很多，用过就清：窗口里没有请求、冷却也过了的表项超过 sweepAt 后会被清掉，表不会一直涨。
func TestPlainWebEntriesAreSwept(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	r, e := newRateLimiter(RateOptions{RequestsPerMinute: 10, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < sweepAt*3; i++ {
		if e = r.allow(webKey("site"+strconv.Itoa(i)+".example"), true); e != nil {
			t.Fatal(e)
		}
		if i%100 == 99 {
			now = now.Add(time.Minute) // 过一分钟，前面的都过期
		}
	}
	if n := len(r.platforms); n > sweepAt {
		t.Fatalf("过期的普通网页表项没清：表里 %d 项，上限 %d", n, sweepAt)
	}
	// 还在冷却的不能被清掉
	r.cooldown(webKey("hot.example"), "3600")
	for i := 0; i < sweepAt*2; i++ {
		_ = r.allow(webKey("more"+strconv.Itoa(i)+".example"), true)
		now = now.Add(time.Second)
	}
	wantError(t, r.allow(webKey("hot.example"), false), "rate_limited")
}

// 回归：Retry-After 没有上限时，对端回一个很大的值会让平台冷却到重启。超过 MaxRetryAfter 的按上限算（秒数、超长数字、日期三种写法）。
func TestRetryAfterIsCapped(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, retry string
		seconds     int // 期望冷却多少秒；0 表示按默认冷却（60）
	}{
		{"huge seconds", "999999999999", int(MaxRetryAfter / time.Second)},
		{"overflowing digits", "99999999999999999999999", int(MaxRetryAfter / time.Second)},
		{"exactly the cap", "600", 600},
		{"just under the cap", "599", 599},
		{"far future date", now.AddDate(10, 0, 0).Format(http.TimeFormat), int(MaxRetryAfter / time.Second)},
		{"date in range", now.Add(300 * time.Second).Format(http.TimeFormat), 300},
		{"past date", now.Add(-time.Hour).Format(http.TimeFormat), 60},
		{"negative", "-5", 60},
		{"negative overflow", "-99999999999999999999999", 60},
		{"fraction", "1.5", 60},
		{"garbage", "soon", 60},
		{"empty", "", 60},
		{"spaces", " 120 ", 120},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, e := newRateLimiter(RateOptions{Now: func() time.Time { return now }})
			if e != nil {
				t.Fatal(e)
			}
			r.cooldown("web@cap.example", c.retry)
			r.mu.Lock()
			got := r.platforms["web@cap.example"].until.Sub(now)
			r.mu.Unlock()
			if want := time.Duration(c.seconds) * time.Second; got != want {
				t.Fatalf("Retry-After %q 冷却了 %v，期望 %v", c.retry, got, want)
			}
		})
	}
}

// 整条链路：对端回很大的 Retry-After，冷却到上限那一刻放行，不是一直挡着。
func TestRequest429HugeRetryAfterEndsAtCap(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	calls := 0
	f, e := NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 100, Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := response(429, `{}`)
		r.Header.Set("Retry-After", "31536000")
		return r, nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	request := func() error {
		_, _, _, e := f.request(context.Background(), "https://api.fxtwitter.com/2/status/90001", "fx_v2")
		return e
	}
	_ = request()
	now = now.Add(MaxRetryAfter - time.Nanosecond)
	wantError(t, request(), "rate_limited")
	now = now.Add(time.Nanosecond)
	if e = request(); e != nil || calls != 2 {
		t.Fatal("冷却到点没放行", calls, e)
	}
}

// GitHub 额度耗尽后等 reset：窗口是一小时，reset 离现在 50 分钟就冷却 50 分钟，不被 10 分钟的上限截短；谎报的更远时刻封在一小时。
func TestGithubResetNotCutByRetryAfterCap(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		reset time.Duration
		want  time.Duration
	}{{50 * time.Minute, 50 * time.Minute}, {24 * time.Hour, time.Hour}} {
		f, e := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, `{}`), nil
		})})
		if e != nil {
			t.Fatal(e)
		}
		resp := response(200, `{}`)
		resp.Header.Set("X-RateLimit-Remaining", "0")
		resp.Header.Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(c.reset).Unix(), 10))
		f.githubBudget(resp)
		f.rate.mu.Lock()
		got := f.rate.platforms["github"].until.Sub(now)
		f.rate.mu.Unlock()
		if got != c.want {
			t.Fatalf("reset 在 %v 后：冷却了 %v，期望 %v", c.reset, got, c.want)
		}
	}
}
