package searxng

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sumika-native/internal/web"
)

func plain(_ context.Context, c *http.Client, r *http.Request) (*http.Response, error) {
	return c.Do(r)
}

func TestSearchParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/search" || q.Get("format") != "json" || q.Get("q") != "测试" || q.Get("time_range") != "year" {
			t.Errorf("请求不对：%s %v", r.URL.Path, q)
		}
		w.Write([]byte(`{"results":[{"url":"https://a.example/1","title":"甲","content":"摘要甲","publishedDate":"2026-11-01T08:00:00+00:00"},{"url":"https://b.example/2","title":"乙","content":"摘要乙","publishedDate":null}]}`))
	}))
	defer srv.Close()
	p := New(srv.URL+"/search/", nil)
	p.do = plain
	if err := p.Available(); err != nil {
		t.Fatal(err)
	}
	res, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "测试", MaxResults: 5, TimeRange: "year"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sources) != 2 || res.Sources[0].Title != "甲" || res.Sources[0].Snippet != "摘要甲" || res.Sources[0].PublishedAt == nil || res.Sources[1].PublishedAt != nil {
		t.Fatalf("解析结果不对：%+v", res)
	}
	if res.Usage.MicroCredits == nil || *res.Usage.MicroCredits != 0 {
		t.Fatalf("免费服务应记 0：%+v", res.Usage)
	}
}

func TestAvailableNeedsURL(t *testing.T) {
	for _, bad := range []string{"", "  ", "searx.example.org", "ftp://x"} {
		if New(bad, nil).Available() == nil {
			t.Errorf("%q 应不可用", bad)
		}
	}
	if New("https://searx.example.org/", nil).Available() != nil {
		t.Error("正常地址应可用")
	}
}

func TestForbiddenExplainsJSONFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	p := New(srv.URL, nil)
	p.do = plain
	_, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "q", MaxResults: 1})
	if err == nil || !strings.Contains(err.Error(), "json") {
		t.Fatalf("应提示启用 json：%v", err)
	}
}
