package duckduckgo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"sumika-native/internal/web"
)

func plain(_ context.Context, c *http.Client, r *http.Request) (*http.Response, error) {
	return c.Do(r)
}

const resultsPage = `<html><body><div id="links" class="results">
<div class="result results_links results_links_deep result--ad"><h2 class="result__title"><a class="result__a" href="//duckduckgo.com/y.js?ad_provider=x">广告</a></h2></div>
<div class="result results_links results_links_deep web-result"><div class="links_main result__body">
<h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fa.example%2Fpage%3Fx%3D1&amp;rut=abc">甲 <b>标题</b></a></h2>
<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fa.example%2Fpage%3Fx%3D1">摘要 <b>甲</b></a></div></div>
<div class="result results_links web-result"><h2 class="result__title"><a class="result__a" href="https://b.example/direct">乙</a></h2>
<a class="result__snippet">摘要乙</a></div>
</div></body></html>`

const challengePage = `<html><body><div class="anomaly-modal__modal">Unfortunately, bots use DuckDuckGo too.</div></body></html>`

func run(t *testing.T, status int, body, timeRange, wantDF string) (web.ProviderSearchResult, error) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		if r.Method != "POST" || r.URL.Path != "/html/" || form.Get("q") != "你好" || form.Get("df") != wantDF {
			t.Errorf("请求不对：%s %s %v", r.Method, r.URL.Path, form)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	p := New(nil)
	p.base, p.do = srv.URL, plain
	return p.Search(context.Background(), web.ProviderSearchRequest{Query: "你好", MaxResults: 5, TimeRange: timeRange})
}

func TestSearchParsesHTML(t *testing.T) {
	res, err := run(t, 200, resultsPage, "week", "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sources) != 2 {
		t.Fatalf("应跳过广告只剩 2 条：%+v", res.Sources)
	}
	a, b := res.Sources[0], res.Sources[1]
	if a.URL != "https://a.example/page?x=1" || a.Title != "甲 标题" || a.Snippet != "摘要 甲" || b.URL != "https://b.example/direct" || b.Snippet != "摘要乙" {
		t.Fatalf("解析不对：%+v", res.Sources)
	}
}

func TestChallengePageIsUnavailable(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{"校验页": {200, challengePage}, "202": {202, resultsPage}, "429": {429, ""}} {
		_, err := run(t, c.status, c.body, "", "")
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s：应返回暂时不可用，得到 %v", name, err)
		}
	}
}

// 对方断开连接（EOF）重试一次就拿到结果；别的错误不重试。
func TestDroppedConnRetriedOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(resultsPage)) }))
	defer srv.Close()
	for _, c := range []struct {
		name      string
		first     error
		wantCalls int
		wantErr   bool
	}{
		{"EOF 重试", io.EOF, 2, false},
		{"别的错不重试", errors.New("编的额度用完"), 1, true},
	} {
		calls := 0
		p := New(nil)
		p.base = srv.URL
		p.do = func(ctx context.Context, cl *http.Client, r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: c.first}
			}
			return cl.Do(r)
		}
		res, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "你好", MaxResults: 5})
		if calls != c.wantCalls || (err != nil) != c.wantErr || (!c.wantErr && len(res.Sources) != 2) {
			t.Fatalf("%s：调了 %d 次、err=%v、%d 条", c.name, calls, err, len(res.Sources))
		}
	}
}
