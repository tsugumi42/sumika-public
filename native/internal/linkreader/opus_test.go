package linkreader

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const opusSynthetic = `{"code":0,"data":{"item":{"id_str":"90001","type":0,"modules":[{"module_type":"MODULE_TYPE_AUTHOR","module_author":{"name":"测试作者甲","mid":90002,"face":"https://i0.hdslb.com/a.jpg","pub_time":"2026年11月3日","pub_ts":"1793620800"}},{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[{"para_type":1,"text":{"nodes":[{"word":{"words":"第一段"}},{"rich":{"text":"@测试作者乙"}},{"word":{"words":"\n后半"}}]}},{"para_type":2,"pic":{"pics":[{"url":"https://i0.hdslb.com/1.jpg","width":640,"height":480},{"url":"https://i1.hdslb.com/2.jpg","width":800,"height":600}]}},{"para_type":99,"link_card":{"title":"未支持的卡片"}},{"para_type":1,"text":{"nodes":[{"word":{"words":"最后一段"}}]}}]}},{"module_type":"MODULE_TYPE_STAT","module_stat":{"like":{"count":0},"comment":{"count":12},"forward":{"count":3}}}]}}}`

func TestOpusOrderedContent(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatal("non-anonymous request")
		}
		if req.URL.Path != "/x/polymer/web-dynamic/v1/opus/detail" || req.URL.Query().Get("id") != "90001" {
			t.Fatal(req.URL)
		}
		return response(200, opusSynthetic), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/opus/90001?spm_id_from=share"))
	if err != nil {
		t.Fatal(err)
	}
	if r.CleanURL != "https://www.bilibili.com/opus/90001" || r.DynamicType.State != "available" || r.DynamicType.Value != 0 || r.Title.State != "not_returned" || r.Author.Name.Value != "测试作者甲" || r.Author.ID.Value != "90002" || r.Author.Avatar.Value != "https://i0.hdslb.com/a.jpg" || r.Published.Value.Unix() != 1793620800 || r.PublishedText.Value != "2026年11月3日" {
		t.Fatalf("metadata: %+v", r)
	}
	if r.Text.Value != "第一段@测试作者乙\n后半\n最后一段" || !r.Text.Truncated {
		t.Fatalf("text: %+v", r.Text)
	}
	types := make([]string, len(r.Blocks))
	for i, b := range r.Blocks {
		types[i] = b.Type
	}
	if !reflect.DeepEqual(types, []string{"text", "image", "image", "unsupported", "text"}) || *r.Blocks[3].ParagraphType != 99 || r.Blocks[4].Text != "最后一段" {
		t.Fatalf("paragraph order: %+v", r.Blocks)
	}
	if r.Media.Value[0].Width.Value != 640 || r.Blocks[2].Media.Height.Value != 600 || r.Stats["like"].State != "available" || r.Stats["like"].Value != 0 || r.Stats["comment"].Value != 12 || r.Stats["forward"].Value != 3 {
		t.Fatalf("media/stat: %+v", r)
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var stored Result
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, r) {
		t.Fatal("ordered content changed across cache JSON")
	}
}

func TestOpusInputBoundaries(t *testing.T) {
	for _, c := range []struct{ name, body, kind string }{
		{"absent_code", `{}`, "invalid_response"}, {"risk", `{"code":-352}`, "risk_control"}, {"missing", `{"code":-404}`, "not_found"},
		{"absent_item", `{"code":0,"data":{}}`, "invalid_response"},
		{"wrong_id", strings.Replace(opusSynthetic, `"id_str":"90001"`, `"id_str":"90009"`, 1), "identity_mismatch"},
		{"absent_content", `{"code":0,"data":{"item":{"id_str":"90001","modules":[]}}}`, "invalid_response"},
		{"null_paragraphs", `{"code":0,"data":{"item":{"id_str":"90001","modules":[{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":null}}]}}}`, "invalid_response"},
		{"wrong_count", strings.Replace(opusSynthetic, `"count":12`, `"count":"12"`, 1), "invalid_response"},
		{"wrong_timestamp", strings.Replace(opusSynthetic, `"pub_ts":"1793620800"`, `"pub_ts":"bad"`, 1), "invalid_response"},
		{"missing_paragraph_type", strings.Replace(opusSynthetic, `"para_type":99,`, ``, 1), "invalid_response"},
		// 没见过的文字节点不再整条失败，改为跳过并计数，见 bilibili_dynamic_test.go 的 TestOpusUnknownTextNodeSkipped
		{"null_nodes", strings.Replace(opusSynthetic, `"nodes":[{"word":{"words":"最后一段"}}]`, `"nodes":null`, 1), "invalid_response"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, c.body), nil })
			_, err := f.Fetch(context.Background(), link(t, "https://t.bilibili.com/90001"))
			wantError(t, err, c.kind)
		})
	}
	t.Run("article_empty_and_missing_fields", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			return response(200, `{"code":0,"data":{"item":{"id_str":"90001","type":1,"modules":[{"module_type":"MODULE_TYPE_TITLE","module_title":{"text":"测试专栏"}},{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[]}}]}}}`), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/opus/90001"))
		if err != nil {
			t.Fatal(err)
		}
		if r.DynamicType.Value != 1 || r.Title.Value != "测试专栏" || r.Text.State != "available" || r.Text.Value != "" || r.Published.State != "not_returned" || r.Stats["like"].State != "not_returned" {
			t.Fatalf("empty/missing: %+v", r)
		}
	})
	t.Run("top_album", func(t *testing.T) {
		body := `{"code":0,"data":{"item":{"id_str":"90001","modules":[{"module_type":"MODULE_TYPE_TOP","module_top":{"display":{"album":{"pics":[{"url":"https://i0.hdslb.com/album.jpg","width":300,"height":400}]}}}},{"module_type":"MODULE_TYPE_CONTENT","module_content":{"paragraphs":[{"para_type":1,"text":{"nodes":[{"word":{"words":"正文"}}]}}]}}]}}}`
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		r, err := f.Fetch(context.Background(), link(t, "https://t.bilibili.com/90001"))
		if err != nil || len(r.Blocks) != 2 || r.Blocks[0].Text != "正文" || r.Blocks[1].Media.Width.Value != 300 {
			t.Fatalf("album: %+v %v", r, err)
		}
	})
}

func TestOpusShortLinks(t *testing.T) {
	for _, target := range []string{"https://www.bilibili.com/opus/90001", "https://t.bilibili.com/90001", "https://m.bilibili.com/dynamic/90001", "https://mobile.bilibili.com/opus/90001"} {
		t.Run(target, func(t *testing.T) {
			f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "b23.tv" {
					r := response(302, "")
					r.Header.Set("Location", target)
					return r, nil
				}
				return response(200, opusSynthetic), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://b23.tv/example"))
			if err != nil || r.ID != "90001" || r.Text.Value != "第一段@测试作者乙\n后半\n最后一段" || r.CleanURL != Clean(target) || f.Requests() != 2 {
				t.Fatalf("short opus: %+v %v", r, err)
			}
		})
	}
}
