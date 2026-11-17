package firecrawl

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sumika-native/internal/web"
)

func plain(_ context.Context, c *http.Client, r *http.Request) (*http.Response, error) {
	return c.Do(r)
}

func server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer k-test" {
			t.Errorf("鉴权头不对")
		}
		switch r.URL.Path {
		case "/v2/search":
			if !strings.Contains(string(body), `"tbs":"qdr:d"`) || !strings.Contains(string(body), `"limit":2`) {
				t.Errorf("搜索请求体不对：%s", body)
			}
			w.Write([]byte(`{"success":true,"id":"s1","creditsUsed":2,"data":{"web":[{"title":"甲","url":"https://a.example/1","description":"摘要甲"}]}}`))
		case "/v2/scrape":
			if !strings.Contains(string(body), `"url":"https://a.example/1"`) || !strings.Contains(string(body), `"markdown"`) {
				t.Errorf("读取请求体不对：%s", body)
			}
			w.Write([]byte(`{"success":true,"data":{"markdown":"# 正文","metadata":{"sourceURL":"https://a.example/1","url":"https://a.example/final","statusCode":200}}}`))
		default:
			t.Errorf("路径不对：%s", r.URL.Path)
		}
	}))
}

func TestSearchAndFetch(t *testing.T) {
	srv := server(t)
	defer srv.Close()
	p := New("k-test", nil)
	p.base, p.do = srv.URL, plain
	res, err := p.Search(context.Background(), web.ProviderSearchRequest{Query: "q", MaxResults: 2, TimeRange: "day"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sources) != 1 || res.Sources[0].Title != "甲" || res.Sources[0].Snippet != "摘要甲" || res.RequestID != "s1" {
		t.Fatalf("搜索解析不对：%+v", res)
	}
	f, err := p.Fetch(context.Background(), web.FetchRequest{URL: "https://a.example/1", Format: "markdown"})
	if err != nil {
		t.Fatal(err)
	}
	r := f.Result
	if r.Content != "# 正文" || r.ReturnedURL != "https://a.example/final" || r.OriginStatus == nil || *r.OriginStatus != 200 || r.Provider != "firecrawl" {
		t.Fatalf("读取解析不对：%+v", r)
	}
}

func TestFetchFailureReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"success":false}`)) }))
	defer srv.Close()
	p := New("k", nil)
	p.base, p.do = srv.URL, plain
	if _, err := p.Fetch(context.Background(), web.FetchRequest{URL: "https://a.example", Format: "markdown"}); err == nil {
		t.Fatal("success=false 应报错")
	}
	if New("", nil).Available() == nil {
		t.Fatal("没 key 应不可用")
	}
}
