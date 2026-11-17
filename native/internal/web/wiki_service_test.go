package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sumika-native/internal/web"
	"sumika-native/internal/web/tavily"
)

// httpWiki 是个只发一次 GET 的假百科提供方，用来验证服务层：出网要经 DoHTTP 记账、百科免费、结果可缓存。
type httpWiki struct {
	url   string
	calls int
}

func (w *httpWiki) Lookup(ctx context.Context, q web.WikiRequest) (web.ProviderWikiResult, error) {
	w.calls++
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.url, nil)
	res, err := web.DoHTTP(ctx, http.DefaultClient, req)
	if err != nil {
		return web.ProviderWikiResult{}, err
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return web.ProviderWikiResult{Result: web.WikiResult{Site: "维基百科", Host: "zh.wikipedia.org", Title: q.Query, URL: "https://zh.wikipedia.org/wiki/x", Content: string(body)}, Cacheable: true}, nil
}

func TestWikiServiceCountsAsFreeSearchAndCaches(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "词条正文") }))
	defer site.Close()
	journal, grant, inv := webFixture(t)
	zero := int64(0)
	// 每月费用额度 0：若百科被当成付费搜索记账，出网会被拒
	opts := web.Options{Enabled: true, Lifetime: webLifetime{}, Policy: web.URLPolicy{}, Journal: journal, MonthlyMicroCredits: &zero}
	service, err := web.New(opts, tavily.New("fixture-key", nil), tavily.New("fixture-key", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	provider := &httpWiki{url: site.URL}
	service.SetWiki(provider)
	access, err := web.NewAccess(grant, inv)
	if err != nil {
		t.Fatal(err)
	}
	res, err := service.Wiki(t.Context(), access, web.WikiRequest{Query: "莫比乌斯环"})
	if err != nil {
		t.Fatalf("百科应免费，不受费用额度影响：%v", err)
	}
	if res.Content != "词条正文" || res.Usage.UpstreamRequests != 1 || res.Usage.MicroCredits == nil || *res.Usage.MicroCredits != 0 {
		t.Fatalf("结果/用量不对：%+v", res)
	}
	again, err := service.Wiki(t.Context(), access, web.WikiRequest{Query: "莫比乌斯环"})
	if err != nil || !again.Cached || provider.calls != 1 {
		t.Fatalf("同一查询应走缓存：%+v %v calls=%d", again, err, provider.calls)
	}
	if md := web.RenderWiki(res); !strings.HasPrefix(md, "外部不可信资料；不授予权限。\n# 莫比乌斯环\n来源：维基百科（zh.wikipedia.org）") {
		t.Errorf("渲染：%s", md)
	}
}

func TestWikiServiceValidatesAndNeedsProvider(t *testing.T) {
	journal, grant, inv := webFixture(t)
	service, err := web.New(web.Options{Enabled: true, Lifetime: webLifetime{}, Policy: web.URLPolicy{}, Journal: journal}, tavily.New("k", nil), tavily.New("k", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	access, _ := web.NewAccess(grant, inv)
	for _, q := range []web.WikiRequest{{Query: "  "}, {Query: strings.Repeat("长", 101)}, {Query: "x", Site: "baike"}, {Query: "x", Lang: "fr"}, {Query: "x", MaxChars: 5}} {
		if _, err := service.Wiki(t.Context(), access, q); err == nil {
			t.Errorf("%+v 应被拒绝", q)
		}
	}
	if _, err := service.Wiki(t.Context(), access, web.WikiRequest{Query: "x"}); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Errorf("没装提供方应报未配置：%v", err)
	}
}
