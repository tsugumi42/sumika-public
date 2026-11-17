package linkreader

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 夹具是 2026-11-13 匿名取到的真实响应（opus/detail 回 fallback 类型 1；普通动态详情是一条“转发图文动态”，
// 只删了头像框、会员标等和取数无关的字段）。别的动态类型用编造的响应。
const dynFixtureID = "1900000000000000100"

type dynServer struct {
	t        *testing.T
	opus     []string // 依次给 opus/detail 的响应（第一次不带 features）
	detail   string
	requests []string
}

func (s *dynServer) RoundTrip(req *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, req.URL.Path+"?features="+req.URL.Query().Get("features"))
	switch req.URL.Path {
	case "/x/polymer/web-dynamic/v1/opus/detail":
		i := 0
		for _, r := range s.requests {
			if strings.HasPrefix(r, "/x/polymer/web-dynamic/v1/opus/detail") {
				i++
			}
		}
		if i > len(s.opus) {
			s.t.Fatalf("opus/detail 请求多了：%v", s.requests)
		}
		return response(200, s.opus[i-1]), nil
	case "/x/polymer/web-dynamic/v1/detail":
		if req.URL.Query().Get("id") != dynFixtureID {
			s.t.Fatalf("detail 参数：%s", req.URL.RawQuery)
		}
		return response(200, s.detail), nil
	}
	s.t.Fatalf("不该请求：%s", req.URL)
	return nil, nil
}

func fetchDyn(t *testing.T, s *dynServer, raw string) (Result, error) {
	t.Helper()
	s.t = t
	f, err := NewFetcher(FetchOptions{Transport: s})
	if err != nil {
		t.Fatal(err)
	}
	return f.Fetch(context.Background(), link(t, raw))
}

func TestOpusFallbackToDynamicForward(t *testing.T) {
	s := &dynServer{opus: []string{fixture(t, "bili-opus-fallback-"+dynFixtureID)}, detail: fixture(t, "bili-dynamic-forward-"+dynFixtureID)}
	r, err := fetchDyn(t, s, "https://www.bilibili.com/opus/"+dynFixtureID)
	if err != nil {
		t.Fatal(err)
	}
	// 请求：一次 opus（fallback 类型 1），直接换普通动态详情，不带 features 再取
	if len(s.requests) != 2 || !strings.HasPrefix(s.requests[1], "/x/polymer/web-dynamic/v1/detail") {
		t.Fatalf("请求序列：%v", s.requests)
	}
	if r.Platform != "bilibili" || r.ID != dynFixtureID || r.CleanURL != "https://www.bilibili.com/opus/"+dynFixtureID {
		t.Fatalf("身份：%+v", r)
	}
	if r.Author.Name.Value != "编造作者" || r.Author.ID.Value != "900000000012285945" || r.Published.State != "available" || r.Published.Value.Unix() != 1794549009 || r.PublishedText.Value != "2026年11月13日 13:50" {
		t.Fatalf("作者和时间：%+v", r)
	}
	if r.Text.Value != "这是编造" || r.Stats["like"].Value != 10 || r.Stats["comment"].Value != 3 || r.Stats["forward"].Value != 0 || r.Stats["forward"].State != "available" {
		t.Fatalf("正文和数据：%+v %+v", r.Text, r.Stats)
	}
	if r.Quote.State != "available" {
		t.Fatalf("没有原动态：%+v", r.Quote)
	}
	q := r.Quote.Value
	if q.Author.Name.Value != "编造作者" || q.Title.Value != "编造的B站标题" || !strings.Contains(q.Text.Value, "编造的B站示例正文") || q.ID != "190000000000071665" {
		t.Fatalf("原动态：%+v", q)
	}
	if len(q.Media.Value) != 1 || q.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/fixture/8.jpg" || q.Media.Value[0].Width.Value != 2400 {
		t.Fatalf("原动态图：%+v", q.Media)
	}
	var kinds []string
	for _, b := range r.Blocks {
		kinds = append(kinds, b.Type)
	}
	if strings.Join(kinds, ",") != "text,quote" {
		t.Fatalf("块顺序：%v", kinds)
	}
	// 缓存要存得下：序列化往返后原动态还在
	enc, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Result
	if err := json.Unmarshal(enc, &back); err != nil || back.Quote.Value == nil || back.Quote.Value.Title.Value != "编造的B站标题" {
		t.Fatalf("往返：%v %+v", err, back.Quote)
	}
}

// 老专栏的 fallback 类型 2 仍走带 features 的第二次 opus 请求，不碰普通动态详情。
func TestOpusFallbackType2StillRetriesWithFeatures(t *testing.T) {
	s := &dynServer{opus: []string{
		`{"code":0,"data":{"item":null,"fallback":{"type":2,"id":"810001"}}}`,
		artOpusBodyFor(dynFixtureID, "810001"),
	}}
	r, err := fetchDyn(t, s, "https://www.bilibili.com/opus/"+dynFixtureID)
	if err != nil || r.Title.Value != "折纸入门笔记" {
		t.Fatalf("%v %+v", err, r.Title)
	}
	if len(s.requests) != 2 || strings.Contains(strings.Join(s.requests, " "), "/v1/detail") {
		t.Fatalf("请求序列：%v", s.requests)
	}
}

func artOpusBodyFor(opusID, cv string) string {
	return strings.Replace(artOpusBody(cv), artOpus, opusID, 1)
}

// fallback 类型不认识：带 features 再取一次仍是 fallback，最后试一次普通动态详情。
func TestOpusUnknownFallbackTypeTriesDynamicLast(t *testing.T) {
	fb := `{"code":0,"data":{"item":null,"fallback":{"type":9,"id":"` + dynFixtureID + `"}}}`
	s := &dynServer{opus: []string{fb, fb}, detail: fixture(t, "bili-dynamic-forward-"+dynFixtureID)}
	r, err := fetchDyn(t, s, "https://t.bilibili.com/"+dynFixtureID)
	if err != nil || r.Quote.State != "available" || len(s.requests) != 3 {
		t.Fatalf("%v %v %+v", err, s.requests, r.Quote)
	}
}

func TestOpusFallbackIdentityMismatch(t *testing.T) {
	s := &dynServer{opus: []string{fixture(t, "bili-opus-fallback-"+dynFixtureID)}, detail: strings.Replace(fixture(t, "bili-dynamic-forward-"+dynFixtureID), `"id_str": "`+dynFixtureID+`"`, `"id_str": "1"`, 1)}
	_, err := fetchDyn(t, s, "https://www.bilibili.com/opus/"+dynFixtureID)
	wantError(t, err, "identity_mismatch")
}

// 编造的各类型：每个都要能出作者和正文，原动态被删、主体类型认不出也不整条失败。
func dynBody(extra string) string {
	return `{"code":0,"data":{"item":{"id_str":"` + dynFixtureID + `","type":"DYNAMIC_TYPE_FORWARD","visible":true,` + extra + `}}}`
}

const dynAuthor = `"module_author":{"name":"测试作者甲","mid":90002,"face":"http://i0.hdslb.com/f.jpg","pub_time":"","pub_ts":1793620800}`

func dynModules(desc, major, additional string) string {
	return `"modules":{` + dynAuthor + `,"module_dynamic":{"desc":` + desc + `,"major":` + major + `,"additional":` + additional + `},"module_stat":{"like":{"count":5},"comment":{"count":6},"forward":{"count":7}}}`
}

func TestDynamicTypes(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		check  func(t *testing.T, r Result)
		reason string
	}{
		{"转发视频", dynBody(dynModules(`{"text":"转发动态"}`, `null`, `null`) + `,"orig":{"id_str":"2001","type":"DYNAMIC_TYPE_AV","visible":true,` +
			dynModules(`{"text":"视频简介"}`, `{"type":"MAJOR_TYPE_ARCHIVE","archive":{"title":"编的视频","bvid":"BV1xx411c7mD","cover":"http://i0.hdslb.com/c.jpg","desc":"摘要"}}`, `null`) + `}`),
			func(t *testing.T, r Result) {
				q := r.Quote.Value
				if r.Text.Value != "转发动态" || q == nil || !strings.Contains(q.Text.Value, "[视频] 编的视频") || !strings.Contains(q.Text.Value, "https://www.bilibili.com/video/BV1xx411c7mD") || len(q.Media.Value) != 1 || q.Media.Value[0].URL.Value != "https://i0.hdslb.com/c.jpg" {
					t.Fatalf("%+v %+v", r.Text, q)
				}
			}, ""},
		{"原动态已删除", dynBody(dynModules(`{"text":"转发动态"}`, `null`, `null`) + `,"orig":{"id_str":"","type":"DYNAMIC_TYPE_NONE","visible":false,` +
			dynModules(`null`, `{"type":"MAJOR_TYPE_NONE","none":{"tips":"源动态已被作者删除"}}`, `null`) + `}`),
			func(t *testing.T, r Result) {
				if r.Quote.State == "available" || r.Quote.Reason != "deleted" || !strings.Contains(r.Text.Value, "源动态已被作者删除") || r.Text.Value[:12] != "转发动态" {
					t.Fatalf("%+v %q", r.Quote, r.Text.Value)
				}
			}, ""},
		{"纯文字加预约", `{"code":0,"data":{"item":{"id_str":"` + dynFixtureID + `","type":"DYNAMIC_TYPE_WORD",` +
			dynModules(`{"rich_text_nodes":[{"type":"RICH_TEXT_NODE_TYPE_TEXT","text":"一二"},{"type":"RICH_TEXT_NODE_TYPE_EMOJI","text":"[表情]"}]}`, `null`, `{"type":"ADDITIONAL_TYPE_RESERVE","reserve":{"title":"直播预约","desc1":{"text":"明天 20:00"}}}`) + `}}}`,
			func(t *testing.T, r Result) {
				if r.Text.Value != "一二[表情]\n直播预约\n明天 20:00" || r.Quote.State == "available" {
					t.Fatalf("%q", r.Text.Value)
				}
			}, ""},
		{"旧图文 DRAW", dynBody(dynModules(`{"text":"配图"}`, `{"type":"MAJOR_TYPE_DRAW","draw":{"items":[{"src":"http://i0.hdslb.com/1.jpg","width":10,"height":20},{"src":"http://i0.hdslb.com/2.jpg"}]}}`, `null`)),
			func(t *testing.T, r Result) {
				if len(r.Media.Value) != 2 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/1.jpg" || r.Media.Value[0].Height.Value != 20 {
					t.Fatalf("%+v", r.Media)
				}
			}, ""},
		{"未知主体类型照常出文字", dynBody(dynModules(`{"text":"只有文字能读"}`, `{"type":"MAJOR_TYPE_NEW_THING","new_thing":{"x":1}}`, `{"type":"ADDITIONAL_TYPE_NEW","new":{}}`)),
			func(t *testing.T, r Result) {
				parts, _ := r.Flags["unrendered_parts"].Value.([]string)
				if r.Text.Value != "只有文字能读" || r.Author.Name.Value != "测试作者甲" || len(parts) != 2 || parts[0] != "major:MAJOR_TYPE_NEW_THING" {
					t.Fatalf("%q %+v", r.Text.Value, r.Flags)
				}
			}, ""},
		{"字段类型错了不整条失败", dynBody(`"modules":{` + dynAuthor + `,"module_dynamic":{"desc":{"text":"能读"},"major":{"type":"MAJOR_TYPE_ARCHIVE","archive":{"title":123,"cover":["x"]}}}}`),
			func(t *testing.T, r Result) {
				if r.Text.Value != "能读" {
					t.Fatalf("%q", r.Text.Value)
				}
			}, ""},
		{"嵌套转发只展开一层", dynBody(dynModules(`{"text":"外"}`, `null`, `null`) + `,"orig":{"id_str":"2001","type":"DYNAMIC_TYPE_FORWARD","visible":true,` +
			dynModules(`{"text":"中"}`, `null`, `null`) + `,"orig":{"id_str":"2002","type":"DYNAMIC_TYPE_WORD","visible":true,` + dynModules(`{"text":"内"}`, `null`, `null`) + `}}`),
			func(t *testing.T, r Result) {
				q := r.Quote.Value
				if q == nil || q.Text.Value != "中\n还转发了另一条动态，看原文" || q.Quote.State == "available" {
					t.Fatalf("%+v", q)
				}
			}, ""},
		{"什么都没有", dynBody(`"modules":{"module_dynamic":{"desc":null,"major":{"type":"MAJOR_TYPE_NEW_THING"}}}`), nil, "invalid_response"},
		{"没有 modules", dynBody(`"basic":{}`), nil, "invalid_response"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &dynServer{opus: []string{fixture(t, "bili-opus-fallback-"+dynFixtureID)}, detail: c.body}
			r, err := fetchDyn(t, s, "https://www.bilibili.com/opus/"+dynFixtureID)
			if c.reason != "" {
				wantError(t, err, c.reason)
				var fe *FetchError
				if errors := asFetchError(err, &fe); errors && !strings.Contains(fe.Detail, "动态类型") {
					t.Fatalf("日志里要写清类型：%q", fe.Detail)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, r)
		})
	}
}

func asFetchError(err error, target **FetchError) bool {
	fe, ok := err.(*FetchError)
	if ok {
		*target = fe
	}
	return ok
}

// opus 文字节点里没见过的类型：跳过并计数，不让整条动态失败；公式、只有 orig_text 的富文本照常出。
func TestOpusUnknownTextNodeSkipped(t *testing.T) {
	body := `{"code":0,"data":{"item":{"id_str":"90001","type":0,"modules":[{"module_type":"MODULE_TYPE_AUTHOR","module_author":{"name":"甲","mid":90002,"pub_ts":"1793620800"}},{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[{"para_type":1,"text":{"nodes":[{"word":{"words":"前"}},{"type":"TEXT_NODE_TYPE_NEW","new_node":{"x":1}},{"rich":{"orig_text":"@乙"}},{"formula":{"latex_content":"x^2"}},{"word":{"words":"后"}}]}}]}}]}}}`
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) { return response(200, body), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/opus/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Text.Value != "前@乙x^2后" || r.Flags["skipped_text_nodes"].Value != 1 || r.Text.Truncated {
		t.Fatalf("%q %+v", r.Text.Value, r.Flags)
	}
}
