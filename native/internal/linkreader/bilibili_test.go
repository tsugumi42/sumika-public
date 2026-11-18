package linkreader

import (
	"context"
	"net/http"
	"testing"
)

func TestBiliMediaDimensionSources(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/x/web-interface/view":
			return response(200, fixture(t, "bili-multi")), nil
		case "/x/tag/archive/tags":
			return response(200, `{"code":0,"data":[]}`), nil
		case "/x/v2/reply":
			return response(200, `{"code":0,"data":{"page":{"count":0},"replies":null}}`), nil
		default:
			t.Fatalf("unexpected playback request: %s", req.URL.Path)
			return nil, nil
		}
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/video/av900000000000090002?p=2"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("cover", func(t *testing.T) {
		for _, m := range r.Media.Value {
			if m.Type == "image" {
				if m.Width.State != "not_returned" || m.Height.State != "not_returned" {
					t.Fatalf("video dimensions assigned to cover: %+v", m)
				}
				return
			}
		}
		t.Fatal("cover missing")
	})
	t.Run("video", func(t *testing.T) {
		for _, m := range r.Media.Value {
			if m.Type == "video" {
				if m.Width.Value != 640 || m.Height.Value != 360 || m.Duration.Value != 205 || m.URL.State != "unsupported" || m.URL.Reason != "playback_not_requested" || m.Size.State != "not_returned" {
					t.Fatalf("selected video metadata: %+v", m)
				}
				return
			}
		}
		t.Fatal("video metadata missing")
	})
}
