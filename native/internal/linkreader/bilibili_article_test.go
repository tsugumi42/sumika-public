package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 数据都是编的：作者、标题、编号不对应任何真实专栏。cv 号 810001，对应的 opus 号 920000000000000001。
const (
	artCV   = "810001"
	artOpus = "920000000000000001"
)

const artDetailBody = `{"code":0,"data":{"item":{"id_str":"` + artOpus + `","basic":{"comment_id_str":"` + artCV + `","comment_type":12},"type":"DYNAMIC_TYPE_ARTICLE",
"modules":{"module_author":{"name":"纸灯实验室","face":"http://i1.hdslb.com/bfs/face/fake.png","mid":770002,"pub_ts":1700000000},
"module_dynamic":{"major":{"article":{"title":"折纸入门笔记","desc":"这是编的摘要，正文的开头几十个字。","covers":["//i0.hdslb.com/bfs/article/cover1.png"],"label":"1200阅读"}}},
"module_stat":{"like":{"count":52},"comment":{"count":8},"forward":{"count":4}}}}}}`

func artOpusBody(commentID string) string {
	return `{"code":0,"data":{"item":{"id_str":"` + artOpus + `","type":64,"basic":{"comment_id_str":"` + commentID + `","rid_str":"` + commentID + `"},"modules":[
{"module_type":"MODULE_TYPE_TITLE","module_title":{"text":"折纸入门笔记"}},
{"module_type":"MODULE_TYPE_AUTHOR","module_author":{"name":"纸灯实验室","mid":770002,"face":"https://i1.hdslb.com/bfs/face/fake.png","pub_time":"2023-11-15 06:13","pub_ts":1700000000}},
{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[
 {"para_type":1,"text":{"nodes":[{"word":{"words":"第一段：先把纸对折。"}}]}},
 {"para_type":2,"pic":{"pics":[{"url":"https://i0.hdslb.com/bfs/article/step1.png","width":800,"height":600}]}},
 {"para_type":1,"text":{"nodes":[{"word":{"words":"第二段：再折一次。"}}]}}]}},
{"module_type":"MODULE_TYPE_STAT","module_stat":{"like":{"count":52},"comment":{"count":8},"forward":{"count":4}}}]}}}`
}

const artInfoBody = `{"code":0,"data":{"title":"折纸入门笔记","mid":770002,"author_name":"纸灯实验室","stats":{"view":1200,"favorite":31,"like":52,"reply":8,"share":4,"coin":10},"image_urls":["https://i0.hdslb.com/bfs/article/cover1.png"]}}`

type articleServer struct {
	t        *testing.T
	detail   func(*http.Request) string
	page     func() *http.Response
	opus     func(*http.Request) string
	info     string
	reply    string
	spi      string
	requests []string
	cookies  []string
}

func (s *articleServer) RoundTrip(req *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, req.URL.Path)
	s.cookies = append(s.cookies, req.Header.Get("Cookie"))
	switch req.URL.Path {
	case "/x/polymer/web-dynamic/v1/detail":
		if q := req.URL.Query(); q.Get("rid") != artCV || q.Get("type") != "64" {
			s.t.Fatalf("detail 参数不对：%s", req.URL.RawQuery)
		}
		return response(200, s.detail(req)), nil
	case "/x/polymer/web-dynamic/v1/opus/detail":
		if !strings.Contains(req.URL.Query().Get("features"), "htmlNewStyle") {
			s.t.Fatalf("专栏取正文必须带 features：%s", req.URL.RawQuery)
		}
		return response(200, s.opus(req)), nil
	case "/x/article/viewinfo":
		return response(200, s.info), nil
	case "/x/frontend/finger/spi":
		return response(200, s.spi), nil
	case "/x/v2/reply":
		if q := req.URL.Query(); q.Get("oid") != artCV || q.Get("type") != "12" {
			s.t.Fatalf("专栏评论参数不对：%s", req.URL.RawQuery)
		}
		return response(200, s.reply), nil
	case "/read/cv" + artCV + "/":
		if s.page == nil {
			return response(200, `<html></html>`), nil
		}
		return s.page(), nil
	}
	s.t.Fatalf("不该请求：%s", req.URL)
	return nil, nil
}

func newArticleServer(t *testing.T) *articleServer {
	return &articleServer{t: t,
		detail: func(*http.Request) string { return artDetailBody },
		opus:   func(*http.Request) string { return artOpusBody(artCV) },
		info:   artInfoBody,
		reply:  `{"code":0,"data":{"page":{"count":1},"replies":[{"rpid":1,"like":7,"rcount":0,"ctime":1,"member":{"uname":"路人甲"},"content":{"message":"写得不错"}}]}}`,
		spi:    `{"code":0,"data":{"b_3":"fakeb3","b_4":"fakeb4"}}`,
	}
}

func fetchArticleWith(t *testing.T, s *articleServer) (Result, error) {
	f, err := NewFetcher(FetchOptions{Transport: s})
	if err != nil {
		t.Fatal(err)
	}
	return f.Fetch(context.Background(), link(t, "https://www.bilibili.com/read/cv"+artCV))
}

func TestArticleFetchViaDetail(t *testing.T) {
	s := newArticleServer(t)
	r, err := fetchArticleWith(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "article" || r.Title.Value != "折纸入门笔记" || r.Author.Name.Value != "纸灯实验室" || r.Author.ID.Value != "770002" {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Text.Value, "第一段") || !strings.Contains(r.Text.Value, "第二段") || r.Text.Truncated {
		t.Fatalf("正文：%+v", r.Text)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/article/step1.png" {
		t.Fatalf("正文里的图：%+v", r.Media.Value)
	}
	for key, want := range map[string]int64{"view": 1200, "favorite": 31, "coin": 10, "like": 52, "comment": 8, "forward": 4} {
		if got := r.Stats[key]; got.State != "available" || got.Value != want {
			t.Fatalf("统计 %s = %+v，想要 %d", key, got, want)
		}
	}
	// 顺序：detail、opus、viewinfo；匿名请求不该带访客标识，也不该去取 spi
	if c := r.Comments.Value; r.Comments.State != "available" || len(c) != 1 || c[0].Author != "路人甲" || c[0].Likes != 7 {
		t.Fatalf("专栏评论：%+v", r.Comments)
	}
	if strings.Join(s.requests, ",") != "/x/polymer/web-dynamic/v1/detail,/x/polymer/web-dynamic/v1/opus/detail,/x/article/viewinfo,/x/v2/reply" {
		t.Fatalf("请求：%v", s.requests)
	}
	for _, c := range s.cookies {
		if c != "" {
			t.Fatalf("不该带 Cookie：%v", s.cookies)
		}
	}
}

// 风控（-352）后不再换标识重试：正文接口只发一次，平台随即冷却（rate.go riskCool），后面的请求不会再发到 B 站。
func TestArticleRiskControlNoRetry(t *testing.T) {
	s := newArticleServer(t)
	s.detail = func(*http.Request) string { return `{"code":-352,"message":"风控校验失败"}` }
	_, _ = fetchArticleWith(t, s)
	detail := 0
	for _, p := range s.requests {
		if p == "/x/polymer/web-dynamic/v1/detail" {
			detail++
		}
	}
	if detail != 1 {
		t.Fatalf("detail 应只请求一次，实际 %d 次：%v", detail, s.requests)
	}
}

// detail 一直取不到：试专栏页的 301；不行就用 viewinfo 出卡片，不编正文。
// 不再请求 x/article/view：它匿名常回 -352、-509，会让整个平台冷却（测试服务器碰到它会直接失败）。
func TestArticleFallbackChain(t *testing.T) {
	// 用普通的接口错误（不是风控）：风控后整条链会停在冷却里，见 TestArticleRiskControlNoRetry
	plain := func(*http.Request) string { return `{"code":-1}` }
	t.Run("301", func(t *testing.T) {
		s := newArticleServer(t)
		s.detail = plain
		s.page = func() *http.Response {
			resp := response(301, "")
			resp.Header.Set("Location", "/opus/"+artOpus)
			return resp
		}
		r, err := fetchArticleWith(t, s)
		if err != nil || !strings.Contains(r.Text.Value, "第二段") {
			t.Fatalf("%v %+v", err, r.Text)
		}
	})
	t.Run("viewinfo 出卡片", func(t *testing.T) {
		s := newArticleServer(t)
		s.detail = plain
		r, err := fetchArticleWith(t, s)
		if err != nil {
			t.Fatal(err)
		}
		if r.Text.State != "not_returned" || r.Text.Reason != "article_body_unavailable" || r.Title.Value != "折纸入门笔记" || r.Stats["view"].Value != 1200 {
			t.Fatalf("%+v %+v", r.Text, r.Title)
		}
		if len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/article/cover1.png" {
			t.Fatalf("封面：%+v", r.Media)
		}
	})
	t.Run("什么都取不到如实报错", func(t *testing.T) {
		s := newArticleServer(t)
		s.detail = func(*http.Request) string { return `{"code":-352}` }
		s.info = `{"code":-509}`
		_, err := fetchArticleWith(t, s)
		wantError(t, err, "risk_control")
	})
}

// opus 里的 cv 号和链接对不上就不能用它的正文；改用 detail 的摘要出卡片并标未完整。
func TestArticleBodyIdentityMismatch(t *testing.T) {
	s := newArticleServer(t)
	s.opus = func(*http.Request) string { return artOpusBody("999999") }
	r, err := fetchArticleWith(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Text.Value, "第一段") || !r.Text.Truncated || !strings.Contains(r.Text.Value, "编的摘要") {
		t.Fatalf("%+v", r.Text)
	}
}

func TestArticleNotFound(t *testing.T) {
	s := newArticleServer(t)
	s.detail = func(*http.Request) string { return `{"code":4101152,"message":"动态不可见"}` }
	_, err := fetchArticleWith(t, s)
	wantError(t, err, "not_found")
	if len(s.requests) != 1 {
		t.Fatalf("不存在就不该再试退路：%v", s.requests)
	}
}

// detail 回来的编号不是这篇专栏（comment_id_str 对不上）就报身份对不上，不拿别的专栏顶。
func TestArticleDetailIdentityMismatch(t *testing.T) {
	s := newArticleServer(t)
	s.detail = func(*http.Request) string {
		return strings.Replace(artDetailBody, `"comment_id_str":"`+artCV+`"`, `"comment_id_str":"999999"`, 1)
	}
	_, err := fetchArticleWith(t, s)
	wantError(t, err, "identity_mismatch")
}

// 老专栏：opus/detail 不带 features 只回 fallback，普通动态链接也要能带上 features 再取一次。
func TestOpusLinkOldArticleFallbackRetry(t *testing.T) {
	calls := 0
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("features") == "" {
			return response(200, `{"code":0,"data":{"item":null,"fallback":{"type":2,"id":"`+artCV+`"}}}`), nil
		}
		return response(200, artOpusBody(artCV)), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/opus/"+artOpus))
	if err != nil || calls != 2 || r.Title.Value != "折纸入门笔记" {
		t.Fatalf("%v calls=%d %+v", err, calls, r.Title)
	}
}

// 默认预算下连发 4 条视频：评论让位给后面链接的主请求，4 条都要展开成功（旧代码第 4 条主请求被挤成 rate_limited）。
func TestFourVideosFitDefaultBudget(t *testing.T) {
	now := time.Now()
	comments := 0
	f, err := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return now }}, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/x/web-interface/view":
			return response(200, fixture(t, "bili-single")), nil
		case "/x/tag/archive/tags":
			return response(200, `{"code":0,"data":[]}`), nil
		case "/x/v2/reply":
			comments++
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
		if err != nil || r.Title.State != "available" {
			t.Fatalf("第 %d 条应展开成功：%v", i, err)
		}
		if r.Tags.State != "available" || r.Comments.Reason == "rate_limited" {
			t.Fatalf("第 %d 条的标签不该被评论挤掉：tags=%+v comments=%+v", i, r.Tags, r.Comments)
		}
	}
	if comments == 0 || comments >= 4 {
		t.Fatalf("预算紧时应跳过一部分评论，实际取了 %d 次", comments)
	}
}
