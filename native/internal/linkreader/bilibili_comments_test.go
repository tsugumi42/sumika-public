package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func commentsFor(t *testing.T, body string) Field[[]Comment] {
	t.Helper()
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/x/v2/reply" || req.URL.Query().Get("oid") != "80433022" || req.URL.Query().Get("type") != "1" || req.URL.Query().Get("sort") != "2" || req.URL.Query().Get("ps") != "3" {
			t.Fatalf("评论请求不对：%s", req.URL)
		}
		return response(200, body), nil
	})
	return f.biliComments(context.Background(), "80433022", 1)
}

// 数据都是编的昵称和内容。置顶的排最前，热评里同一条不重复，最多三条，空内容跳过，过长的正文截断。
func TestCommentsOrderAndLimits(t *testing.T) {
	long := strings.Repeat("长", 300)
	c := commentsFor(t, `{"code":0,"data":{"page":{"count":900},
"upper":{"top":{"rpid":2,"like":5,"rcount":1,"ctime":123456,"member":{"uname":"置顶用户"},"content":{"message":"UP 主置顶的评论"}}},
"replies":[
 {"rpid":1,"like":300,"rcount":20,"member":{"uname":"热评甲"},"content":{"message":"第一条热评"}},
 {"rpid":2,"like":5,"member":{"uname":"置顶用户"},"content":{"message":"UP 主置顶的评论"}},
 {"rpid":3,"like":9,"member":{"uname":"空评论"},"content":{"message":"  "}},
 {"rpid":4,"like":8,"member":{"uname":"热评乙"},"content":{"message":"`+long+`"}},
 {"rpid":5,"like":7,"member":{"uname":"热评丙"},"content":{"message":"第四条，超出上限"}}]}}`)
	if c.State != "available" || len(c.Value) != 3 {
		t.Fatalf("%+v", c)
	}
	if !c.Value[0].Top || c.Value[0].Author != "置顶用户" || c.Value[1].Author != "热评甲" || c.Value[1].Likes != 300 || c.Value[1].Replies != 20 || c.Value[2].Author != "热评乙" {
		t.Fatalf("%+v", c.Value)
	}
	if n := len([]rune(c.Value[2].Text)); n != commentTextRune+1 {
		t.Fatalf("长评论应截到 %d 字加省略号，得到 %d 字", commentTextRune, n)
	}
	text := CommentsText(c, 20)
	for _, want := range []string{"热评：", "1. ［置顶］置顶用户：UP 主置顶的评论（赞 5）", "2. 热评甲：第一条热评（赞 300）"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, strings.Repeat("长", 21)) {
		t.Fatalf("CommentsText 的字数限制没生效：\n%s", text)
	}
}

// 没有评论的几种原因要分开写：真的没有、只显示 UP 主精选（评论不公开）、评论区关闭、取不到。
func TestCommentsAbsentReasons(t *testing.T) {
	cases := []struct {
		name, body, reason, text string
	}{
		{"没人评论", `{"code":0,"data":{"page":{"count":0},"replies":null,"control":{"root_input_text":"发一条友善的评论"}}}`, "no_comments", "暂无评论"},
		{"精选后可见", `{"code":0,"data":{"page":{"count":0},"replies":null,"control":{"root_input_text":"评论被up主精选后，对所有人可见"}}}`, "comments_selected_only", "评论不公开（UP 主精选后才可见）"},
		{"评论区关闭", `{"code":12002,"message":"评论区已关闭"}`, "comments_closed", "评论区已关闭"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := commentsFor(t, c.body)
			if f.State != "not_returned" || f.Reason != c.reason || CommentsText(f, 0) != c.text {
				t.Fatalf("%+v %q", f, CommentsText(f, 0))
			}
		})
	}
	for name, c := range map[string]struct{ body, reason, text string }{
		"风控":     {`{"code":-352}`, "risk_control", "评论这次没取到（被平台拦了）"},
		"限流":     {`{"code":-509}`, "rate_limited", "评论这次没取到（请求太频繁）"},
		"字段不对":   {`{"code":0}`, "invalid_response", "评论这次没取到"},
		"不是JSON": {`<html>`, "invalid_response", "评论这次没取到"},
	} {
		t.Run(name, func(t *testing.T) {
			f := commentsFor(t, c.body)
			if f.State != "failed" || f.Reason != c.reason || CommentsText(f, 0) != c.text {
				t.Fatalf("%+v %q", f, CommentsText(f, 0))
			}
		})
	}
}

// 没取过评论的旧缓存（字段是零值）不写评论文字。
func TestCommentsTextEmptyForOldCache(t *testing.T) {
	if got := CommentsText(Field[[]Comment]{}, 0); got != "" {
		t.Fatalf("%q", got)
	}
}

// 评论取不到不拖垮视频本身：视频照出，评论字段写原因；充电专属视频的标志位照给。
func TestVideoSurvivesCommentFailure(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/x/web-interface/view":
			return response(200, fixture(t, "bili-single")), nil
		case "/x/tag/archive/tags":
			return response(200, `{"code":0,"data":[]}`), nil
		case "/x/v2/reply":
			return response(200, `{"code":-352}`), nil
		}
		t.Fatalf("不该请求：%s", req.URL)
		return nil, nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/video/BV1xx411c7mD"))
	if err != nil || r.Title.State != "available" {
		t.Fatalf("%v", err)
	}
	if r.Comments.State != "failed" || r.Comments.Reason != "risk_control" {
		t.Fatalf("%+v", r.Comments)
	}
}
