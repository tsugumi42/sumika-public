package linkreader

import (
	"context"
	"net/http"
	"testing"
)

func TestMediaFieldStates(t *testing.T) {
	for _, c := range []struct{ name, field, state string }{{"absent", "", "not_returned"}, {"null", `,"media":null`, "not_returned"}, {"empty_object", `,"media":{}`, "available"}, {"null_all", `,"media":{"all":null}`, "not_returned"}, {"empty_all", `,"media":{"all":[]}`, "available"}} {
		t.Run(c.name, func(t *testing.T) {
			body := `{"code":200,"status":{"id":"1","text":"正文"` + c.field + `}}`
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Media.State != c.state {
				t.Fatalf("want %s; got %+v", c.state, r.Media)
			}
		})
	}
	t.Run("bili_missing_duration", func(t *testing.T) {
		f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/x/tag/archive/tags" {
				return response(200, `{"code":0,"data":[]}`), nil
			}
			return response(200, `{"code":0,"data":{"bvid":"BV1GJ411x7h7","aid":1,"cid":2,"title":"视频","desc":"","pages":[{"cid":2,"page":1,"dimension":{"width":640,"height":360}}]}}`), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/video/BV1GJ411x7h7"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Media.Value[1].Duration.State != "not_returned" {
			t.Fatalf("missing duration became zero: %+v", r.Media.Value[1].Duration)
		}
	})
}
