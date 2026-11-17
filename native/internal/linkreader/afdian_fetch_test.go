package linkreader

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// afdianReplay 按接口路径返回夹具；请求头断言匿名访问（无 Cookie、无 Authorization）。
// afdianPublished 是测试用的发布时间（Unix 秒），运行时计算：夹具里不放 10 位数字，避免被号码规则误报。
var afdianPublished = time.Date(2020, 6, 29, 8, 37, 49, 0, time.UTC).Unix()

// afdianFixture 读取夹具；动态夹具在 post_id 前插入发布时间（夹具里已去掉该字段）。
func afdianFixture(t *testing.T, name string) string {
	t.Helper()
	body := fixture(t, name)
	if strings.HasPrefix(name, "afdian-post-") {
		body = strings.Replace(body, `"post_id"`, `"publish_time": `+strconv.FormatInt(afdianPublished, 10)+`, "post_id"`, 1)
	}
	return body
}

func afdianReplay(t *testing.T, routes map[string]string) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatal("afdian request must be anonymous")
		}
		if req.Header.Get("Referer") != "https://afdian.com/" {
			t.Fatalf("referer: %q", req.Header.Get("Referer"))
		}
		for path, name := range routes {
			if req.URL.Path == path {
				return response(200, afdianFixture(t, name)), nil
			}
		}
		t.Fatalf("unexpected request %s", req.URL.String())
		return nil, nil
	}
}

func TestAfdianReplay(t *testing.T) {
	const album = "90000000000000000000000000000006"
	t.Run("creator", func(t *testing.T) {
		f := testFetcher(t, afdianReplay(t, map[string]string{"/api/user/get-profile-by-slug": "afdian-profile"}))
		r, err := f.Fetch(context.Background(), link(t, "https://afdian.com/a/example_creator"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Kind != "creator" || r.Title.Value != "编造创作者" || r.Author.ID.Value != "90000000000000000000000000000008" || r.Author.Account.Value != "example_creator" || r.Flags["verified"].Value != true {
			t.Fatalf("creator: %+v", r)
		}
		if len(r.Media.Value) != 1 || r.Media.Value[0].Type != "image" || r.Text.State != "available" {
			t.Fatalf("creator media/text: %+v", r)
		}
	})
	t.Run("image_post", func(t *testing.T) {
		f := testFetcher(t, afdianReplay(t, map[string]string{"/api/post/get-detail": "afdian-post-image"}))
		r, err := f.Fetch(context.Background(), link(t, "https://afdian.com/p/90000000000000000000000000000001"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Title.Value != "编造的图文动态" || r.Author.Name.Value != "编造创作者" || r.Published.Value.Unix() != afdianPublished || r.Stats["likes"].Value != 917 || r.Stats["comments"].Value != 0 {
			t.Fatalf("image post: %+v", r)
		}
		if len(r.Media.Value) != 7 || r.Blocks[0].Type != "text" || len(r.Blocks) != 8 {
			t.Fatalf("image blocks: media=%d blocks=%d", len(r.Media.Value), len(r.Blocks))
		}
		for i, b := range r.Blocks[1:] {
			if b.Type != "image" || b.Media == nil || b.Media.URL.State != "available" {
				t.Fatalf("block %d: %+v", i, b)
			}
		}
	})
	t.Run("article_html", func(t *testing.T) {
		f := testFetcher(t, afdianReplay(t, map[string]string{"/api/post/get-detail": "afdian-post-article"}))
		r, err := f.Fetch(context.Background(), link(t, "https://afdian.com/p/90000000000000000000000000000002"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Text.State != "available" || !r.Text.Truncated || utf8.RuneCountInString(r.Text.Value) != 800 {
			t.Fatalf("article text: %+v", r.Text)
		}
		if strings.Contains(r.Text.Value, "<") || strings.Contains(r.Text.Value, "&ldquo;") || !strings.Contains(r.Text.Value, "编造的品牌色") {
			t.Fatalf("article html not converted: %.120s", r.Text.Value)
		}
		if r.Stats["likes"].Value != 113 {
			t.Fatalf("article stats: %+v", r.Stats)
		}
	})
	t.Run("free_video", func(t *testing.T) {
		f := testFetcher(t, afdianReplay(t, map[string]string{"/api/post/get-detail": "afdian-post-video"}))
		r, err := f.Fetch(context.Background(), link(t, "https://afdian.com/p/90000000000000000000000000000003"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Media.Value) != 1 || r.Media.Value[0].Type != "video" || r.Media.Value[0].Thumbnail.State != "available" || r.Media.Value[0].Duration.State != "not_returned" {
			t.Fatalf("video media: %+v", r.Media)
		}
		if r.Media.Value[0].URL.Value == "" || r.Stats["likes"].Value != 88 {
			t.Fatalf("video fields: %+v", r.Media.Value[0])
		}
	})
	t.Run("paid_locked", func(t *testing.T) {
		f := testFetcher(t, afdianReplay(t, map[string]string{"/api/post/get-detail": "afdian-post-paid"}))
		r, err := f.Fetch(context.Background(), link(t, "https://afdian.com/p/90000000000000000000000000000004"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Title.State != "available" || r.Author.Name.Value != "编造作者乙" || r.Text.State != "login_required" || r.Media.State != "login_required" {
			t.Fatalf("paid states: %+v", r)
		}
		if r.Flags["sponsor_required"].Value != true || r.Flags["sponsor_min_price"].Value != "60.00" || !strings.Contains(r.Flags["sponsor_notice"].Value.(string), "60.00") {
			t.Fatalf("paid flags: %+v", r.Flags)
		}
		if len(r.Blocks) != 0 {
			t.Fatalf("locked post must not expose blocks: %+v", r.Blocks)
		}
	})
	t.Run("album_post", func(t *testing.T) {
		var seen string
		f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
			seen = req.URL.RawQuery
			return response(200, afdianFixture(t, "afdian-post-album")), nil
		})
		r, err := f.Fetch(context.Background(), link(t, "https://afdian.com/album/"+album+"/90000000000000000000000000000005"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Kind != "album_post" || !strings.Contains(seen, "album_id="+album) || r.Text.Value != "这是编造的爱发电示例正文" {
			t.Fatalf("album post: kind=%s query=%s text=%+v", r.Kind, seen, r.Text)
		}
	})
	t.Run("album", func(t *testing.T) {
		f := testFetcher(t, afdianReplay(t, map[string]string{
			"/api/user/get-album-info": "afdian-album-info",
			"/api/user/get-album-post": "afdian-album-list",
		}))
		r, err := f.Fetch(context.Background(), link(t, "https://afdian.com/album/"+album))
		if err != nil {
			t.Fatal(err)
		}
		if r.Title.Value != "编造的更新记录" || r.Stats["posts"].Value != 49 || r.Author.Name.Value != "编造作者丙" {
			t.Fatalf("album: %+v", r)
		}
		if r.Links.State != "available" || !r.Links.Truncated || len(r.Links.Value) != 2 {
			t.Fatalf("album links: %+v", r.Links)
		}
		if r.Links.Value[0].URL.Value != "https://afdian.com/album/"+album+"/90000000000000000000000000000005" || r.Links.Value[0].Display.Value != "编造的更新日志" {
			t.Fatalf("album link 0: %+v", r.Links.Value[0])
		}
	})
	t.Run("item_unsupported_without_request", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("item must not send a request")
			return nil, nil
		})
		_, err := f.Fetch(context.Background(), link(t, "https://afdian.com/item/90000000000000000000000000000007"))
		wantError(t, err, "unsupported")
		if f.Requests() != 0 {
			t.Fatalf("requests=%d", f.Requests())
		}
	})
}

func TestAfdianFailureShapes(t *testing.T) {
	const post = "https://afdian.com/p/90000000000000000000000000000001"
	const album = "https://afdian.com/album/90000000000000000000000000000006"
	for _, c := range []struct {
		name, url string
		status    int
		body      string
		kind      string
	}{
		{"http_404", post, 404, `{}`, "not_found"},
		{"http_410", post, 410, `{}`, "not_found"},
		{"http_403", post, 403, `{}`, "login_required"},
		{"http_429", post, 429, `{}`, "rate_limited"},
		{"ec_404_post_missing", post, 200, `{"ec":404,"em":"动态不存在"}`, "not_found"},
		{"ec_400_user_missing", "https://afdian.com/a/nobody-here", 200, `{"ec":400,"em":"用户不存在"}`, "not_found"},
		{"ec_401", post, 200, `{"ec":401,"em":"请登录"}`, "login_required"},
		{"ec_missing", post, 200, `{"data":{}}`, "invalid_response"},
		{"malformed_json", post, 200, `<html>challenge</html>`, "invalid_response"},
		{"has_right_string", post, 200, `{"ec":200,"data":{"post":{"post_id":"90000000000000000000000000000001","title":"x","has_right":"yes"}}}`, "invalid_response"},
		{"count_object", post, 200, `{"ec":200,"data":{"post":{"post_id":"90000000000000000000000000000001","title":"x","has_right":1,"like_count":{}}}}`, "invalid_response"},
		{"post_null", post, 200, `{"ec":200,"data":{"post":null}}`, "not_returned"},
		{"post_id_mismatch", post, 200, `{"ec":200,"data":{"post":{"post_id":"00000000000000000000000000000000","title":"x","has_right":1}}}`, "identity_mismatch"},
		{"title_missing", post, 200, `{"ec":200,"data":{"post":{"post_id":"90000000000000000000000000000001","has_right":1}}}`, "invalid_response"},
		{"album_id_mismatch", album, 200, `{"ec":200,"data":{"album":{"album_id":"00000000000000000000000000000000","title":"x"}}}`, "identity_mismatch"},
		{"profile_slug_mismatch", "https://afdian.com/a/example_creator", 200, `{"ec":200,"data":{"user":{"name":"x","url_slug":"other"}}}`, "identity_mismatch"},
		{"profile_name_missing", "https://afdian.com/a/example_creator", 200, `{"ec":200,"data":{"user":{"url_slug":"example_creator"}}}`, "invalid_response"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(c.status, c.body), nil })
			_, err := f.Fetch(context.Background(), link(t, c.url))
			wantError(t, err, c.kind)
		})
	}
}

func TestAfdianOptionalAndLimitShapes(t *testing.T) {
	const url = "https://afdian.com/p/90000000000000000000000000000001"
	base := `{"ec":200,"data":{"post":{"post_id":"90000000000000000000000000000001","title":"t","has_right":1,"type":0,"content":"正文","pics":null,"video":"","video_thumb":"","like_count":"917","publish_time":"PUBLISHED"}}}`
	base = strings.Replace(base, "PUBLISHED", strconv.FormatInt(afdianPublished, 10), 1)
	t.Run("pics_null_is_zero_media", func(t *testing.T) {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, base), nil })
		r, err := f.Fetch(context.Background(), link(t, url))
		if err != nil {
			t.Fatal(err)
		}
		if r.Media.State != "available" || len(r.Media.Value) != 0 || r.Stats["likes"].Value != 917 || r.Stats["comments"].State != "not_returned" || r.Published.Value.Unix() != afdianPublished {
			t.Fatalf("zero media / string counts: %+v", r)
		}
		if r.Text.Value != "正文" {
			t.Fatalf("text: %+v", r.Text)
		}
	})
	t.Run("long_text_is_cut_with_flag", func(t *testing.T) {
		long := strings.Repeat("长", 5000)
		body := strings.Replace(base, `"content":"正文"`, `"content":"`+long+`"`, 1)
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		r, err := f.Fetch(context.Background(), link(t, url))
		if err != nil {
			t.Fatal(err)
		}
		if !r.Text.Truncated || utf8.RuneCountInString(r.Text.Value) != 800 {
			t.Fatalf("long text: truncated=%v runes=%d", r.Text.Truncated, utf8.RuneCountInString(r.Text.Value))
		}
	})
	t.Run("article_drops_script_and_decodes_entities", func(t *testing.T) {
		body := `{"ec":200,"data":{"post":{"post_id":"90000000000000000000000000000001","title":"t","has_right":1,"type":1,"content":"<p>甲&amp;乙&ldquo;丙&rdquo;</p><script>alert(1)</script><style>p{}</style><p>丁<br>戊</p>","pics":[],"video":""}}}`
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		r, err := f.Fetch(context.Background(), link(t, url))
		if err != nil {
			t.Fatal(err)
		}
		if r.Text.Value != "甲&乙“丙”\n丁\n戊" {
			t.Fatalf("plain text: %q", r.Text.Value)
		}
	})
	t.Run("empty_content_is_not_returned", func(t *testing.T) {
		body := `{"ec":200,"data":{"post":{"post_id":"90000000000000000000000000000001","title":"t","has_right":1,"type":0,"content":"  ","pics":[]}}}`
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		r, err := f.Fetch(context.Background(), link(t, url))
		if err != nil {
			t.Fatal(err)
		}
		if r.Text.State != "not_returned" || r.Text.Reason != "empty_content" {
			t.Fatalf("empty content: %+v", r.Text)
		}
	})
	t.Run("album_list_failure_is_partial", func(t *testing.T) {
		const album = "https://afdian.com/album/90000000000000000000000000000006"
		f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "get-album-post") {
				return response(429, `{}`), nil
			}
			return response(200, fixture(t, "afdian-album-info")), nil
		})
		r, err := f.Fetch(context.Background(), link(t, album))
		if err != nil {
			t.Fatal(err)
		}
		if r.Title.Value != "编造的更新记录" || r.Links.State != "failed" || r.Links.Reason != "rate_limited" {
			t.Fatalf("partial album: %+v links=%+v", r.Title, r.Links)
		}
	})
	t.Run("rate_budget_is_independent_of_x", func(t *testing.T) {
		now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
		f, err := NewFetcher(FetchOptions{Rate: RateOptions{RequestsPerMinute: 1, Now: func() time.Time { return now }}, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(200, fixture(t, "afdian-profile")), nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Fetch(context.Background(), link(t, "https://afdian.com/a/example_creator")); err != nil {
			t.Fatal(err)
		}
		if err := f.rate.allow("x", true); err != nil {
			t.Fatal("afdian budget leaked into x", err)
		}
	})
}
