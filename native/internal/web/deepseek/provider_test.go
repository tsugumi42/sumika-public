package deepseek

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/web"
)

func plain(_ context.Context, c *http.Client, r *http.Request) (*http.Response, error) {
	return c.Do(r)
}

func newTest(srv *httptest.Server) *Provider {
	p := New("k-test", nil)
	p.base, p.do = srv.URL, plain
	p.now = func() time.Time { return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) }
	return p
}

func TestSearchParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/tools/search" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer k-test" {
			t.Errorf("请求不对：%s %s", r.Method, r.URL.Path)
		}
		if !strings.Contains(string(body), `"count":3`) || !strings.Contains(string(body), `"need_content":false`) || !strings.Contains(string(body), `after:2026-11-03`) {
			t.Errorf("请求体不对：%s", body)
		}
		w.Write([]byte(`{"results":[{"title":"甲","url":"https://a.example/1","snippet":"短","summary":"较长摘要","site_name":"甲站","date":"2026-11-01T08:00:00Z","authority":3},{"title":"乙","url":"https://b.example/2","snippet":"只有短","summary":"","date":null}]}`))
	}))
	defer srv.Close()
	res, err := newTest(srv).Search(context.Background(), web.ProviderSearchRequest{Query: "q", MaxResults: 3, TimeRange: "week"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sources) != 2 || res.Sources[0].Snippet != "较长摘要" || res.Sources[0].SiteName != "甲站" || res.Sources[0].PublishedAt == nil || res.Sources[1].Snippet != "只有短" || res.Sources[1].PublishedAt != nil {
		t.Fatalf("解析结果不对：%+v", res)
	}
	if res.Usage.MicroCredits == nil || *res.Usage.MicroCredits != 1_000_000 {
		t.Fatalf("成功请求应记 1 个单位：%+v", res.Usage)
	}
}

func TestEmptyResultsAreNotCharged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"results":[]}`)) }))
	defer srv.Close()
	res, err := newTest(srv).Search(context.Background(), web.ProviderSearchRequest{Query: "q", MaxResults: 3})
	if err != nil || len(res.Sources) != 0 || res.Usage.MicroCredits == nil || *res.Usage.MicroCredits != 0 {
		t.Fatalf("空结果不该计费：%+v %v", res, err)
	}
}

func TestLongQueryTruncatedTo128(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Count(string(body), "字") != 128 {
			t.Errorf("查询没截到 128 字：%s", body)
		}
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	newTest(srv).Search(context.Background(), web.ProviderSearchRequest{Query: strings.Repeat("字", 300), MaxResults: 1, TimeRange: "day"})
}

func TestFetchReturnsMarkdownWithTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/tools/fetch" || !strings.Contains(string(body), `"url":"https://x.example/a"`) {
			t.Errorf("请求不对：%s %s", r.URL.Path, body)
		}
		w.Write([]byte(`{"title":"文章","content":"# 文章\n\n正文"}`))
	}))
	defer srv.Close()
	res, err := newTest(srv).Fetch(context.Background(), web.FetchRequest{URL: "https://x.example/a", Format: "markdown"})
	if err != nil || res.Result.Title != "文章" || !strings.Contains(res.Result.Content, "正文") || res.Result.Usage.MicroCredits == nil || *res.Result.Usage.MicroCredits != 1_000_000 {
		t.Fatalf("抓取结果不对：%+v %v", res, err)
	}
}

func TestStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		code      int
		exhausted bool
	}{{402, true}, {429, false}, {403, false}, {404, false}, {500, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
		bal := web.NewBalances()
		p := newTest(srv)
		p.SetBalances(bal)
		_, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "q", MaxResults: 1})
		srv.Close()
		if err == nil || web.IsExhausted(err) != tc.exhausted || strings.Contains(err.Error(), "k-test") {
			t.Errorf("%d：%v", tc.code, err)
		}
		if got := bal.Exhausted("deepseek", false); got != tc.exhausted {
			t.Errorf("%d：余额标记 %v", tc.code, got)
		}
	}
}

func TestAvailableNeedsKey(t *testing.T) {
	if New(" ", nil).Available() == nil || New("x", nil).Available() != nil {
		t.Fatal("Available 判断不对")
	}
}
