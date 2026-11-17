package wiki

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/web"
)

func plain(_ context.Context, c *http.Client, r *http.Request) (*http.Response, error) {
	return c.Do(r)
}

func sample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// mw 是一个假的 MediaWiki 站：prefix 是前缀搜索的结果标题，search 是全文搜索的结果，pages 是 title -> parse 响应。
type mw struct {
	mu     sync.Mutex
	prefix []string
	search [][2]string
	pages  map[string]string
	// reject 非空：所有接口请求都回这段原文（模拟主站拒绝 API、或回校验页）
	reject  string
	calls   []string
	uas     []string
	variant []string
}

func (m *mw) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := r.URL.Query()
	m.calls = append(m.calls, q.Get("action")+"/"+q.Get("list"))
	m.uas = append(m.uas, r.Header.Get("User-Agent"))
	m.variant = append(m.variant, q.Get("variant"))
	if m.reject != "" {
		w.Write([]byte(m.reject))
		return
	}
	switch {
	case q.Get("list") == "prefixsearch":
		var items []string
		for _, t := range m.prefix {
			items = append(items, `{"ns":0,"title":"`+t+`"}`)
		}
		w.Write([]byte(`{"batchcomplete":true,"query":{"prefixsearch":[` + strings.Join(items, ",") + `]}}`))
	case q.Get("list") == "search":
		var items []string
		for _, h := range m.search {
			items = append(items, `{"ns":0,"title":"`+h[0]+`","snippet":"`+h[1]+`"}`)
		}
		w.Write([]byte(`{"batchcomplete":true,"query":{"search":[` + strings.Join(items, ",") + `]}}`))
	case q.Get("action") == "parse":
		if body, ok := m.pages[q.Get("page")]; ok {
			w.Write([]byte(body))
			return
		}
		w.Write([]byte(`{"error":{"code":"missingtitle","info":"The page you specified doesn't exist."}}`))
	default:
		w.WriteHeader(400)
	}
}

func newProvider(t *testing.T, hosts map[string]*mw, wikis map[string]*mw, order ...string) *Provider {
	t.Helper()
	servers := map[string]*httptest.Server{}
	mk := func(m *mw) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(m.handler))
		t.Cleanup(s.Close)
		return s
	}
	for h, m := range hosts {
		servers[h] = mk(m)
	}
	wsrv := map[string]*httptest.Server{}
	for l, m := range wikis {
		wsrv[l] = mk(m)
	}
	p := New(order, nil)
	p.do = plain
	p.apiURL = func(host string) string { return servers[host].URL }
	p.wikiAPI = func(lang string) string { return wsrv[lang].URL }
	p.pageURL = func(host, title string) string { return "https://" + host + "/" + escapeTitle(title) }
	return p
}

func TestMoegirlExactTitleReturnsCleanArticle(t *testing.T) {
	m := &mw{prefix: []string{"编造示例条目"}, pages: map[string]string{"编造示例条目": sample(t, "moegirl_parse.json")}}
	p := newProvider(t, map[string]*mw{"moegirl.uk": m}, map[string]*mw{"zh": {}}, "moegirl.uk")
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "编造示例条目", Lang: "zh", MaxChars: 6000})
	if err != nil {
		t.Fatal(err)
	}
	r := got.Result
	if r.Site != "萌娘百科" || r.Host != "moegirl.uk" || r.Title != "编造示例条目" || r.URL != "https://moegirl.uk/%E7%BC%96%E9%80%A0%E7%A4%BA%E4%BE%8B%E6%9D%A1%E7%9B%AE" {
		t.Fatalf("来源/标题不对：%+v", r)
	}
	if !strings.Contains(r.Content, "编造的条目正文") {
		t.Errorf("正文没了：%.300s", r.Content)
	}
	for _, noise := range []string{"本曲目已进入殿堂", "歌曲名称", "/images/", "window.RLQ", "niconicoCount", "编辑", "[1]", "VOCALOID殿堂曲"} {
		if strings.Contains(r.Content, noise) {
			t.Errorf("杂项 %q 还在：%.600s", noise, r.Content)
		}
	}
	if got := strings.Count(strings.Join(m.variant, ","), "zh-cn"); got != len(m.variant) {
		t.Errorf("萌百请求应带简体 variant：%v", m.variant)
	}
	for _, ua := range m.uas {
		if !strings.Contains(ua, "github.com/tsugumi42/sumika") || strings.Contains(ua, "@") {
			t.Errorf("User-Agent 要带项目名和仓库地址、不带邮箱：%q", ua)
		}
	}
	if len(r.Content) == 0 || strings.Count(r.Content, "\n\n\n") > 0 {
		t.Errorf("空行没收拾干净")
	}
}

func TestMoegirlMainRejectedFallsToMirror(t *testing.T) {
	main := &mw{reject: `{"error":{"code":"action-notallowed","info":"Unauthorized API call"}}`}
	mirror := &mw{prefix: []string{"编造示例条目"}, pages: map[string]string{"编造示例条目": sample(t, "moegirl_parse.json")}}
	p := newProvider(t, map[string]*mw{"zh.moegirl.org.cn": main, "moegirl.uk": mirror}, map[string]*mw{"zh": {}}, "zh.moegirl.org.cn", "moegirl.uk")
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "编造示例条目", Site: "moegirl", Lang: "zh", MaxChars: 6000})
	if err != nil || got.Result.Host != "moegirl.uk" || got.Result.Content == "" {
		t.Fatalf("主站被拒应换镜像：%+v %v", got.Result, err)
	}
	if len(main.calls) != 1 {
		t.Errorf("主站只该试一次，不重试：%v", main.calls)
	}
}

func TestMoegirlChallengePageFallsToMirror(t *testing.T) {
	main := &mw{reject: `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>Checking your browser</body></html>`}
	mirror := &mw{prefix: []string{"A"}, pages: map[string]string{"A": `{"parse":{"title":"A","text":"<p>` + strings.Repeat("正文。", 30) + `</p>"}}`}}
	p := newProvider(t, map[string]*mw{"zh.moegirl.org.cn": main, "moegirl.uk": mirror}, map[string]*mw{"zh": {}}, "zh.moegirl.org.cn", "moegirl.uk")
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "A", Site: "moegirl", Lang: "zh", MaxChars: 6000})
	if err != nil || got.Result.Host != "moegirl.uk" {
		t.Fatalf("校验页应换镜像：%+v %v", got.Result, err)
	}
}

func TestAllMoegirlHostsBlockedIsAnError(t *testing.T) {
	deny := `{"error":{"code":"action-notallowed","info":"x"}}`
	p := newProvider(t, map[string]*mw{"a": {reject: deny}, "b": {reject: deny}}, map[string]*mw{"zh": {}}, "a", "b")
	_, err := p.Lookup(context.Background(), web.WikiRequest{Query: "x", Site: "moegirl", Lang: "zh", MaxChars: 6000})
	if err == nil || !strings.Contains(err.Error(), "萌娘百科取不到") {
		t.Fatalf("全拒绝应报错并说明：%v", err)
	}
	// 不指定站点：萌百全拒绝后继续查维基，而不是整件事失败
	wiki := &mw{prefix: []string{"X"}, pages: map[string]string{"X": `{"parse":{"title":"X","text":"<p>维基里的内容很长很长。</p>"}}`}}
	p = newProvider(t, map[string]*mw{"a": {reject: deny}}, map[string]*mw{"zh": wiki}, "a")
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "X", Lang: "zh", MaxChars: 6000})
	if err != nil || got.Result.Site != "维基百科" {
		t.Fatalf("萌百不行应接着查维基：%+v %v", got.Result, err)
	}
}

func TestDefaultOrderMoegirlThenWikipedia(t *testing.T) {
	moe := &mw{pages: map[string]string{}}
	wp := &mw{prefix: []string{"编造维基示例"}, pages: map[string]string{"编造维基示例": sample(t, "wikipedia_parse.json")}}
	p := newProvider(t, map[string]*mw{"moegirl.uk": moe}, map[string]*mw{"en": wp}, "moegirl.uk")
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "编造维基示例", Lang: "en", MaxChars: 6000})
	if err != nil {
		t.Fatal(err)
	}
	r := got.Result
	if r.Site != "维基百科" || r.Host != "en.wikipedia.org" || r.URL != "https://en.wikipedia.org/wiki/%E7%BC%96%E9%80%A0%E7%BB%B4%E5%9F%BA%E7%A4%BA%E4%BE%8B" {
		t.Fatalf("应回落到维基：%+v", r)
	}
	if len(moe.calls) == 0 {
		t.Error("省略 site 时应先查萌百")
	}
	for _, noise := range []string{"导航", "Navbox", "↑", "[1]", "upload.wikimedia.org"} {
		if strings.Contains(r.Content, noise) {
			t.Errorf("杂项 %q 还在：%.500s", noise, r.Content)
		}
	}
	if len(wp.variant) > 0 && wp.variant[0] != "" {
		t.Errorf("英文维基不该带 variant：%v", wp.variant)
	}
}

func TestZhWikipediaUsesSimplifiedVariant(t *testing.T) {
	wp := &mw{prefix: []string{"编造维基示例"}, pages: map[string]string{"编造维基示例": sample(t, "wikipedia_parse.json")}}
	p := newProvider(t, nil, map[string]*mw{"zh": wp})
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "编造维基示例", Site: "wikipedia", Lang: "zh", MaxChars: 6000})
	if err != nil || got.Result.Content == "" || wp.variant[0] != "zh-cn" {
		t.Fatalf("%+v %v %v", got.Result, err, wp.variant)
	}
}

func TestSeveralPrefixHitsReturnCandidates(t *testing.T) {
	wp := &mw{prefix: []string{"莫比乌斯", "莫比乌斯带", "莫比乌斯变换"}}
	p := newProvider(t, nil, map[string]*mw{"zh": wp})
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "莫比", Site: "wikipedia", Lang: "zh", MaxChars: 6000})
	if err != nil || got.Result.Content != "" || len(got.Result.Candidates) != 3 || got.Result.Candidates[1].Title != "莫比乌斯带" {
		t.Fatalf("多个候选应返回列表：%+v %v", got.Result, err)
	}
	md := web.RenderWiki(got.Result)
	if !strings.Contains(md, "1. 莫比乌斯（维基百科）") || !strings.Contains(md, "完整标题再查") {
		t.Errorf("候选列表渲染：%s", md)
	}
}

func TestFullTextFallbackAndSingleHit(t *testing.T) {
	// 标题前缀没有，全文搜出多个：给候选（带摘要，高亮标签去掉）
	wp := &mw{search: [][2]string{{"甲", "含<span class=\\\"searchmatch\\\">关键</span>词的摘要 &amp; 更多"}, {"乙", ""}}}
	p := newProvider(t, nil, map[string]*mw{"zh": wp})
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "关键词", Site: "wikipedia", Lang: "zh", MaxChars: 6000})
	if err != nil || len(got.Result.Candidates) != 2 || got.Result.Candidates[0].Snippet != "含关键词的摘要 & 更多" {
		t.Fatalf("全文候选：%+v %v", got.Result, err)
	}
	// 全文只有一个结果：直接取它
	wp = &mw{search: [][2]string{{"唯一", "x"}}, pages: map[string]string{"唯一": `{"parse":{"title":"唯一","text":"<p>唯一的词条内容。</p>"}}`}}
	p = newProvider(t, nil, map[string]*mw{"zh": wp})
	got, err = p.Lookup(context.Background(), web.WikiRequest{Query: "什么", Site: "wikipedia", Lang: "zh", MaxChars: 6000})
	if err != nil || got.Result.Title != "唯一" || !strings.Contains(got.Result.Content, "唯一的词条内容") {
		t.Fatalf("单个全文结果应直接取：%+v %v", got.Result, err)
	}
}

func TestNotFoundSaysWhatWasTried(t *testing.T) {
	p := newProvider(t, map[string]*mw{"moegirl.uk": {}}, map[string]*mw{"zh": {}}, "moegirl.uk")
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "不存在的东西", Lang: "zh", MaxChars: 6000})
	if err != nil || got.Result.Content != "" || len(got.Result.Candidates) != 0 {
		t.Fatalf("%+v %v", got.Result, err)
	}
	md := web.RenderWiki(got.Result)
	if !strings.Contains(md, "没有找到相关词条") || !strings.Contains(md, "萌娘百科：没有") || !strings.Contains(md, "维基百科：没有") {
		t.Errorf("说明不全：%s", md)
	}
}

func TestLongArticleIsTruncatedWithRemainder(t *testing.T) {
	body := strings.Repeat("<p>"+strings.Repeat("字", 100)+"</p>", 100) // 约 10000 字
	wp := &mw{prefix: []string{"长"}, pages: map[string]string{"长": `{"parse":{"title":"长","text":"` + body + `"}}`}}
	p := newProvider(t, nil, map[string]*mw{"zh": wp})
	got, err := p.Lookup(context.Background(), web.WikiRequest{Query: "长", Site: "wikipedia", Lang: "zh", MaxChars: 3000})
	if err != nil {
		t.Fatal(err)
	}
	r := got.Result
	if n := len([]rune(r.Content)); n > 3000 || n < 2400 || r.Remaining < 6900 || r.Remaining > 7600 {
		t.Fatalf("截断不对：正文 %d 字，剩 %d 字", n, r.Remaining)
	}
	if strings.HasSuffix(r.Content, "字字") && !strings.HasSuffix(r.Content, strings.Repeat("字", 100)) {
		t.Errorf("应在段落边界截断：尾部 %q", string([]rune(r.Content)[len([]rune(r.Content))-10:]))
	}
	if md := web.RenderWiki(r); !strings.Contains(md, "后面还有") || !strings.Contains(md, "来源：维基百科（zh.wikipedia.org） · https://zh.wikipedia.org/wiki/") {
		t.Errorf("渲染：%.400s", md)
	}
}

func TestPageURLEscape(t *testing.T) {
	u, _ := url.Parse("https://zh.wikipedia.org/wiki/" + escapeTitle("A B/C"))
	if u.Path != "/wiki/A_B%2FC" && u.EscapedPath() != "/wiki/A_B%2FC" {
		t.Errorf("%s", u.EscapedPath())
	}
}

// 没有表头的排版表（萌百的引言模板）不该转出一行空表头。
func TestToMarkdownDropsEmptyTableHeader(t *testing.T) {
	html := `<table><tbody><tr><td>“</td><td>编的引言</td><td>”</td></tr><tr><td>——编的出处</td></tr></tbody></table><table><tbody><tr><td>词曲</td><td>编的作者</td></tr></tbody></table>`
	md, err := ToMarkdown(html, "https://example.org/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(md, "| --- |") {
		t.Fatalf("还留着空表头：\n%s", md)
	}
	for _, want := range []string{"编的引言", "编的出处", "词曲", "编的作者"} {
		if !strings.Contains(md, want) {
			t.Fatalf("内容丢了 %q：\n%s", want, md)
		}
	}
}
