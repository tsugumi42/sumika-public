package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// 数据都是编的，沿用 bilibili_article_test.go 里的 cv 号和 opus 号。
func opusCoverBody(commentType, pic string) string {
	paras := `{"para_type":1,"text":{"nodes":[{"word":{"words":"只有文字的一篇。"}}]}}`
	if pic != "" {
		paras += `,{"para_type":2,"pic":{"pics":[{"url":"` + pic + `","width":800,"height":600}]}}`
	}
	return `{"code":0,"data":{"item":{"id_str":"` + artOpus + `","type":1,"basic":{"comment_id_str":"` + artCV + `","comment_type":` + commentType + `,"rid_str":"` + artCV + `"},"modules":[
{"module_type":"MODULE_TYPE_TITLE","module_title":{"text":"折纸入门笔记"}},
{"module_type":"MODULE_TYPE_AUTHOR","module_author":{"name":"纸灯实验室","mid":770002,"face":"https://i1.hdslb.com/bfs/face/fake.png","pub_time":"2023-11-15 06:13","pub_ts":1700000000}},
{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[` + paras + `]}},
{"module_type":"MODULE_TYPE_STAT","module_stat":{"like":{"count":1},"comment":{"count":2},"forward":{"count":3}}}]}}}`
}

// 回归：专栏转成的 opus 链接，正文没有图时也要像专栏链接一样带上封面（样张 B-19）。
func TestOpusLinkOfArticleGetsCover(t *testing.T) {
	for _, c := range []struct {
		name       string
		opus       string
		detail     string
		wantMedia  string
		wantDetail bool
	}{
		{"补封面", opusCoverBody("12", ""), artDetailBody, "https://i0.hdslb.com/bfs/article/cover1.png", true},
		{"正文有图不再补", opusCoverBody("12", "https://i0.hdslb.com/bfs/article/step1.png"), artDetailBody, "https://i0.hdslb.com/bfs/article/step1.png", false},
		{"不是专栏不补", opusCoverBody("11", ""), artDetailBody, "", false},
		{"封面接口失败不影响结果", opusCoverBody("12", ""), `{"code":-404,"message":"x"}`, "", true},
		{"opus 号对不上不补", opusCoverBody("12", ""), strings.Replace(artDetailBody, `"id_str":"`+artOpus+`"`, `"id_str":"920000000000000002"`, 1), "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var paths []string
			f, err := NewFetcher(FetchOptions{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				paths = append(paths, req.URL.Path)
				switch req.URL.Path {
				case "/x/polymer/web-dynamic/v1/opus/detail":
					return response(200, c.opus), nil
				case "/x/polymer/web-dynamic/v1/detail":
					return response(200, c.detail), nil
				}
				t.Fatalf("不该请求：%s", req.URL)
				return nil, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/opus/"+artOpus))
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if len(r.Media.Value) > 0 {
				got = r.Media.Value[0].URL.Value
			}
			if got != c.wantMedia || len(r.Media.Value) > 1 {
				t.Fatalf("图：%+v", r.Media.Value)
			}
			asked := false
			for _, p := range paths {
				asked = asked || p == "/x/polymer/web-dynamic/v1/detail"
			}
			if asked != c.wantDetail {
				t.Fatalf("请求 detail=%v，应为 %v：%v", asked, c.wantDetail, paths)
			}
		})
	}
}
