package linkreader

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestOpusPublicReplay(t *testing.T) {
	for _, c := range []struct {
		id, author, title, contains       string
		kind, paragraphs, pictures, lists int
	}{
		{"1900000000000000111", "编造作者", "编造的B站标题", "编造的B站示例正文", 1, 22, 1, 4},
		{"1900000000000000112", "编造作者", "", "编造的B站示例正文", 0, 1, 0, 0},
		{"1900000000000000113", "编造作者", "编造的B站标题", "编造的B站示例正文", 0, 2, 1, 0},
		{"1900000000000000114", "编造作者", "编造的B站标题", "编造的B站示例正文", 1, 168, 9, 0},
	} {
		t.Run(c.id, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, fixture(t, "bili-opus-"+c.id)), nil })
			r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/opus/"+c.id+"?spm_id_from=share"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Author.Name.Value != c.author || r.Title.Value != c.title || r.DynamicType.State != "available" || r.DynamicType.Value != c.kind || r.Published.State != "available" || r.Author.ID.State != "available" || r.Author.Avatar.State != "available" || r.Text.Truncated || !strings.Contains(r.Text.Value, c.contains) {
				t.Fatalf("public content: %+v", r)
			}
			if c.title == "" && r.Title.State != "not_returned" {
				t.Fatal("missing title fabricated")
			}
			lists := 0
			for _, b := range r.Blocks {
				if b.Type == "unsupported" {
					t.Fatal("known paragraph discarded")
				}
				if b.ParagraphType != nil && *b.ParagraphType == 5 {
					lists++
				}
			}
			if len(r.Blocks) != c.paragraphs || len(r.Media.Value) != c.pictures || lists != c.lists {
				t.Fatalf("blocks=%d images=%d lists=%d", len(r.Blocks), len(r.Media.Value), lists)
			}
			if c.id == "1900000000000000111" && (r.Blocks[11].Media.Width.Value != 592 || !strings.Contains(r.Blocks[15].Text, "编造的B站示例正文")) {
				t.Fatal("interleaved image/list lost")
			}
			if c.id == "1900000000000000113" && (r.Media.Value[0].Width.Value != 743 || r.Media.Value[0].Height.Value != 209) {
				t.Fatal("image dimensions lost")
			}
		})
	}
}

func TestBiliPublicVideoFields(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/x/tag/archive/tags" {
			return response(200, `{"code":0,"data":[]}`), nil
		}
		return response(200, fixture(t, "bili-video-20261103")), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://www.bilibili.com/video/BV1xx411c7mD/?spm_id_from=share"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Author.Name.Value != "编造作者" || r.Author.Avatar.Value != "https://i2.hdslb.com/bfs/fixture/2.jpg" || r.Published.Value.Unix() != 1577835803 || r.SelectedPart.Value.Duration.Value != 213 || r.Media.Value[1].Duration.Value != 213 || len(r.Parts.Value) != 1 || r.Media.Value[0].URL.Value != "http://i1.hdslb.com/bfs/fixture/1.jpg" {
		t.Fatalf("video fields: %+v", r)
	}
	want := map[string]Field[int64]{"view": available(int64(107987590)), "danmaku": available(int64(151611)), "like": available(int64(2970151)), "coin": available(int64(1290555)), "favorite": available(int64(1562798)), "share": available(int64(505201)), "reply": available(int64(239763))}
	if !reflect.DeepEqual(r.Stats, want) {
		t.Fatalf("stats: %+v", r.Stats)
	}
}
