package brave

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"sumika-native/internal/web"
)

func plain(_ context.Context, c *http.Client, r *http.Request) (*http.Response, error) {
	return c.Do(r)
}

func TestSearchParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/res/v1/web/search" || r.Header.Get("X-Subscription-Token") != "k-test" || q.Get("q") != "你好 world" || q.Get("count") != "5" || q.Get("freshness") != "pm" {
			t.Errorf("请求不对：%s %v %q", r.URL.Path, q, r.Header.Get("X-Subscription-Token"))
		}
		w.Write([]byte(`{"web":{"results":[{"title":"甲","url":"https://a.example/1","description":"摘要甲"},{"title":"乙","url":"https://b.example/2","description":"摘要乙"}]}}`))
	}))
	defer srv.Close()
	p := New("k-test", nil)
	p.base, p.do = srv.URL, plain
	res, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "你好 world", MaxResults: 5, TimeRange: "month"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sources) != 2 || res.Sources[1].URL != "https://b.example/2" || res.Sources[0].Snippet != "摘要甲" || res.Sources[0].Title != "甲" {
		t.Fatalf("解析结果不对：%+v", res)
	}
	if res.Usage.MicroCredits == nil || *res.Usage.MicroCredits != 1_000_000 {
		t.Fatalf("额度记账不对：%+v", res.Usage)
	}
}

func TestAvailableNeedsKey(t *testing.T) {
	if New("", nil).Available() == nil || New("k", nil).Available() != nil {
		t.Fatal("Available 判断不对")
	}
}

func TestCountCappedAt20(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("count") != "20" {
			t.Errorf("count=%s", r.URL.Query().Get("count"))
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := New("k", nil)
	p.base, p.do = srv.URL, plain
	if _, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "q", MaxResults: 50}); err != nil {
		t.Fatal(err)
	}
}
