package exa

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

func TestSearchParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/search" || r.Method != "POST" || r.Header.Get("x-api-key") != "k-test" {
			t.Errorf("请求不对：%s %s %q", r.Method, r.URL.Path, r.Header.Get("x-api-key"))
		}
		if !strings.Contains(string(body), `"numResults":3`) || !strings.Contains(string(body), `"startPublishedDate":"2026-11-03T00:00:00Z"`) {
			t.Errorf("请求体不对：%s", body)
		}
		w.Write([]byte(`{"requestId":"r1","results":[{"title":"甲","url":"https://a.example/1","publishedDate":"2026-11-01T00:00:00.000Z","text":"摘要甲"},{"title":"乙","url":"https://b.example/2","highlights":["高亮一","高亮二"]}]}`))
	}))
	defer srv.Close()
	p := New("k-test", nil)
	p.base, p.do = srv.URL, plain
	p.now = func() time.Time { return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC) }
	res, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "q", MaxResults: 3, TimeRange: "week"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sources) != 2 || res.Sources[0].Title != "甲" || res.Sources[0].Snippet != "摘要甲" || res.Sources[0].PublishedAt == nil || res.Sources[1].Snippet != "高亮一 高亮二" || res.RequestID != "r1" {
		t.Fatalf("解析结果不对：%+v", res)
	}
	if res.Usage.MicroCredits == nil || *res.Usage.MicroCredits != 1_000_000 {
		t.Fatalf("额度记账不对：%+v", res.Usage)
	}
}

func TestAvailableNeedsKey(t *testing.T) {
	if New(" ", nil).Available() == nil || New("x", nil).Available() != nil {
		t.Fatal("Available 判断不对")
	}
}

func TestHTTPErrorHidesKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	p := New("secret-key-xyz", nil)
	p.base, p.do = srv.URL, plain
	_, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "q", MaxResults: 1})
	if err == nil || strings.Contains(err.Error(), "secret-key-xyz") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误不对：%v", err)
	}
}
