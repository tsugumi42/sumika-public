package linkreader

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const bskySampleDID = "did:plc:samplelabsamplelabsample"

// bskyNoAuth 确认请求只走匿名公开 AppView，不带 Cookie 或 Authorization。
func bskyNoAuth(t *testing.T, req *http.Request) {
	t.Helper()
	if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
		t.Fatal("bluesky request carries credentials")
	}
	if req.URL.Host != "public.api.bsky.app" || !strings.HasPrefix(req.URL.Path, "/xrpc/app.bsky.") {
		t.Fatalf("unexpected endpoint: %s", req.URL)
	}
}

func bskyFetch(t *testing.T, rawURL string, body string, status int) (Result, error) {
	t.Helper()
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		bskyNoAuth(t, req)
		return response(status, body), nil
	})
	return f.Fetch(context.Background(), link(t, rawURL))
}

func TestBlueskyPostReplay(t *testing.T) {
	for _, c := range []struct {
		name, rkey string
		media      int
	}{{"images", "3mzhhvhjfwk22", 1}, {"gallery", "3mz263w23ik2f", 5}, {"video", "3myi2ihquwk26", 1}} {
		t.Run(c.name, func(t *testing.T) {
			r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/"+c.rkey, fixture(t, "bsky-post-"+c.name), 200)
			if err != nil {
				t.Fatal(err)
			}
			if r.Platform != "bluesky" || r.Kind != "post" || r.Source != bskySource || r.Author.ID.Value != bskySampleDID || r.Author.Name.Value != "编造作者甲" || r.Author.Account.Value != "example-lab.bsky.social" {
				t.Fatalf("author: %+v", r.Author)
			}
			if r.Published.State != "available" || r.Title.Value != "编造的正文第一行" || r.Text.Value != "编造的正文第一行\nsecond line of invented text" {
				t.Fatalf("text: title=%+v text=%+v", r.Title, r.Text)
			}
			if r.Stats["likes"].State != "available" || r.Stats["likes"].Value == 0 || r.Flags["possibly_sensitive"].Value != false || r.Links.Value == nil || r.Media.State != "available" || len(r.Media.Value) != c.media {
				t.Fatalf("fields: stats=%+v flags=%+v media=%d", r.Stats, r.Flags, len(r.Media.Value))
			}
			if r.ReplyTo.State != "not_returned" {
				t.Fatalf("reply: %+v", r.ReplyTo)
			}
		})
	}
	t.Run("images_dimensions", func(t *testing.T) {
		r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/3mzhhvhjfwk22", fixture(t, "bsky-post-images"), 200)
		if err != nil {
			t.Fatal(err)
		}
		m := r.Media.Value[0]
		if m.Type != "image" || m.URL.State != "available" || !strings.Contains(m.URL.Value, "cdn.bsky.app/img/feed_fullsize") || m.Width.Value != 2328 || m.Height.Value != 1570 {
			t.Fatalf("image: %+v", m)
		}
		if len(r.Blocks) == 0 || r.Blocks[0].Type != "text" {
			t.Fatalf("block order: %+v", r.Blocks)
		}
	})
	t.Run("gallery_order", func(t *testing.T) {
		r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/3mz263w23ik2f", fixture(t, "bsky-post-gallery"), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Media.Value[0].Width.Value != 1206 || r.Media.Value[1].Height.Value != 4000 {
			t.Fatalf("gallery order: %+v", r.Media.Value)
		}
	})
	t.Run("video_hls_only", func(t *testing.T) {
		r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/3myi2ihquwk26", fixture(t, "bsky-post-video"), 200)
		if err != nil {
			t.Fatal(err)
		}
		m := r.Media.Value[0]
		if m.Type != "video" || !strings.HasSuffix(m.URL.Value, "/playlist.m3u8") || !strings.Contains(m.URL.Value, "video.bsky.app/watch/") || m.Variants.Value[0].Container != "hls" {
			t.Fatalf("video: %+v", m)
		}
		if m.Duration.State != "not_returned" || m.Size.State != "not_returned" || m.Width.Value != 1080 || m.Height.Value != 1920 {
			t.Fatalf("video fields: %+v", m)
		}
	})
	t.Run("external_card", func(t *testing.T) {
		r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/3mxwnz4gbzk2r", fixture(t, "bsky-post-external"), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.LinkCard.State != "available" || r.LinkCard.Value.URL.Value == "" || r.LinkCard.Value.Site.Value == "" || len(r.Media.Value) != 0 {
			t.Fatalf("card: %+v media=%d", r.LinkCard, len(r.Media.Value))
		}
		// 外链只在 embed 里：正文后面要有一段写卡片标题和链接，不然群里只看到半句话
		last := r.Blocks[len(r.Blocks)-1]
		if last.Type != "link_card" || !strings.Contains(last.Text, r.LinkCard.Value.URL.Value) {
			t.Fatalf("link card block: %+v", r.Blocks)
		}
	})
	t.Run("quote_one_level", func(t *testing.T) {
		r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/3muwxhygya22x", fixture(t, "bsky-post-quote"), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Quote.State != "available" || r.Quote.Value == nil {
			t.Fatalf("quote: %+v", r.Quote)
		}
		q := r.Quote.Value
		if q.Kind != "post" || q.ID != bskySampleDID+"/3muwxbtebrk2x" || q.CleanURL != "https://bsky.app/profile/"+bskySampleDID+"/post/3muwxbtebrk2x" || q.Text.State != "available" {
			t.Fatalf("quoted post: %+v", q)
		}
		if len(r.Blocks) < 2 || r.Blocks[len(r.Blocks)-1].Type != "quote" {
			t.Fatalf("quote block: %+v", r.Blocks)
		}
	})
	t.Run("recordwithmedia", func(t *testing.T) {
		r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/3mxucjrmulk2x", fixture(t, "bsky-post-recwm"), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Quote.State != "available" || r.LinkCard.State != "available" {
			t.Fatalf("recordWithMedia: quote=%+v card=%+v", r.Quote, r.LinkCard)
		}
	})
	t.Run("text_zero_media", func(t *testing.T) {
		r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/3mzjmn67mak2z", fixture(t, "bsky-post-text"), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Media.State != "available" || len(r.Media.Value) != 0 || r.Quote.State == "available" {
			t.Fatalf("zero media: %+v", r.Media)
		}
	})
}

func TestBlueskyProfileReplay(t *testing.T) {
	r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social", fixture(t, "bsky-profile"), 200)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "profile" || r.Title.Value != "编造作者甲" || r.Author.ID.Value != bskySampleDID || r.Text.Value != "编造的简介" {
		t.Fatalf("profile: %+v", r)
	}
	if r.Stats["followers"].Value != 101038 || r.Stats["follows"].Value != 12 || r.Stats["posts"].Value != 322 || r.Published.State == "available" {
		t.Fatalf("profile stats: %+v published=%+v", r.Stats, r.Published)
	}
}

func TestBlueskyFailureShapes(t *testing.T) {
	post := "https://bsky.app/profile/example-lab.bsky.social/post/3mzhhvhjfwk22"
	profile := "https://bsky.app/profile/example-lab.bsky.social"
	fixtureText := fixture(t, "bsky-post-images")
	for _, c := range []struct {
		name, url, body, kind string
		status                int
	}{
		{"not_found_400", post, `{"error":"NotFound","message":"Post not found"}`, "not_found", 400},
		{"not_found_404", post, `{}`, "not_found", 404},
		{"gone_410", post, `{}`, "not_found", 410},
		{"not_found_post", post, `{"thread":{"$type":"app.bsky.feed.defs#notFoundPost","uri":"at://x","notFound":true}}`, "not_found", 200},
		{"blocked_post", post, `{"thread":{"$type":"app.bsky.feed.defs#blockedPost","uri":"at://x","blocked":true}}`, "not_found", 200},
		{"login_wall_403", post, `{"error":"AuthRequired"}`, "login_required", 403},
		{"login_wall_401", post, `{"error":"AuthRequired"}`, "login_required", 401},
		{"malformed_json", post, `{"thread":`, "invalid_response", 200},
		{"missing_thread", post, `{}`, "invalid_response", 200},
		{"unexpected_thread_type", post, `{"thread":{"$type":"app.bsky.feed.defs#something"}}`, "invalid_response", 200},
		{"identity_post", post, strings.Replace(fixtureText, "example-lab.bsky.social", "other.bsky.social", 1), "identity_mismatch", 200},
		{"identity_uri_did", post, strings.Replace(fixtureText, "at://"+bskySampleDID+"/", "at://did:plc:otherlabotherlabother/", 1), "identity_mismatch", 200},
		{"identity_profile", profile, strings.Replace(fixture(t, "bsky-profile"), "example-lab.bsky.social", "other.bsky.social", 1), "identity_mismatch", 200},
		{"bad_record_text", post, strings.Replace(fixtureText, `"text": "编造的正文第一行\nsecond line of invented text"`, `"text": 7`, 1), "invalid_response", 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := bskyFetch(t, c.url, c.body, c.status)
			wantError(t, err, c.kind)
		})
	}
	t.Run("rate_limited_with_reset", func(t *testing.T) {
		// RateLimit-Reset 是 Unix 时间戳；运行时算出一个远期值，避免写死长数字。
		stamp := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
		f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
			bskyNoAuth(t, req)
			resp := response(429, `{}`)
			resp.Header.Set("RateLimit-Reset", strconv.FormatInt(stamp, 10))
			return resp, nil
		})
		_, err := f.Fetch(context.Background(), link(t, post))
		var fe *FetchError
		if err == nil || !strings.Contains(err.Error(), "rate_limited") {
			t.Fatalf("want rate_limited: %v", err)
		}
		if e, ok := err.(*FetchError); ok {
			fe = e
		}
		if fe == nil {
			t.Fatalf("want *FetchError: %v", err)
		}
		at, perr := http.ParseTime(fe.RetryAfter)
		if perr != nil || !at.Equal(time.Unix(stamp, 0)) {
			t.Fatalf("reset hint %q: want the reset epoch as an HTTP date", fe.RetryAfter)
		}
	})
	t.Run("blocked_reason", func(t *testing.T) {
		_, err := bskyFetch(t, post, `{"thread":{"$type":"app.bsky.feed.defs#blockedPost"}}`, 200)
		if fe, ok := err.(*FetchError); !ok || fe.Reason != "blocked_post" {
			t.Fatalf("blocked reason: %v", err)
		}
	})
	t.Run("nested_quote_not_expanded", func(t *testing.T) {
		r, err := bskyFetch(t, "https://bsky.app/profile/example-lab.bsky.social/post/3muwxhygya22x", fixture(t, "bsky-post-quote"), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Quote.Value == nil || r.Quote.Value.Quote.State == "available" {
			t.Fatalf("quote of quote must not expand: %+v", r.Quote.Value)
		}
	})
}

func TestBlueskyFieldBoundaries(t *testing.T) {
	post := "https://bsky.app/profile/example-lab.bsky.social/post/3mzhhvhjfwk22"
	wrap := func(record, author, extra string) string {
		return `{"thread":{"$type":"app.bsky.feed.defs#threadViewPost","post":{"uri":"at://` + bskySampleDID + `/app.bsky.feed.post/3mzhhvhjfwk22",` + extra + `"author":{"did":"` + bskySampleDID + `","handle":"example-lab.bsky.social"` + author + `},"record":` + record + `}}}`
	}
	t.Run("missing_display_name_and_zero_stats", func(t *testing.T) {
		body := wrap(`{"text":"短","createdAt":"2026-11-01T00:00:00Z"}`, "", `"likeCount":0,`)
		r, err := bskyFetch(t, post, body, 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Author.Name.State != "not_returned" || r.Stats["likes"].State != "available" || r.Stats["likes"].Value != 0 || r.Stats["reposts"].State != "not_returned" {
			t.Fatalf("missing vs zero: author=%+v stats=%+v", r.Author.Name, r.Stats)
		}
	})
	t.Run("empty_text_and_no_media", func(t *testing.T) {
		r, err := bskyFetch(t, post, wrap(`{"text":"","createdAt":"2026-11-01T00:00:00Z"}`, "", ""), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Text.State == "available" || r.Title.State == "available" || r.Media.State != "available" {
			t.Fatalf("empty text: %+v", r)
		}
	})
	t.Run("very_long_text", func(t *testing.T) {
		long := strings.Repeat("字", 1000)
		r, err := bskyFetch(t, post, wrap(`{"text":"`+long+`","createdAt":"2026-11-01T00:00:00Z"}`, "", ""), 200)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Text.Truncated || len([]rune(r.Text.Value)) != 800 || len([]rune(r.Title.Value)) != 73 || !strings.HasSuffix(r.Title.Value, "…") {
			t.Fatalf("long text: text=%d title=%d truncated=%v", len([]rune(r.Text.Value)), len([]rune(r.Title.Value)), r.Text.Truncated)
		}
	})
	t.Run("facet_byte_offsets", func(t *testing.T) {
		text := "表情🙂 https://example.com/a 结束"
		start := strings.Index(text, "https://example.com/a")
		end := start + len("https://example.com/a")
		facets := `[{"index":{"byteStart":` + strconv.Itoa(start) + `,"byteEnd":` + strconv.Itoa(end) + `},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://example.com/a"}]},` +
			`{"index":{"byteStart":1,"byteEnd":3},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://example.com/broken"}]},` +
			`{"index":{"byteStart":0,"byteEnd":9999},"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://example.com/out"}]}]`
		body := wrap(`{"text":"`+text+`","createdAt":"2026-11-01T00:00:00Z","facets":`+facets+`}`, "", "")
		r, err := bskyFetch(t, post, body, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Links.Value) != 1 || r.Links.Value[0].URL.Value != "https://example.com/a" || r.Links.Value[0].Original.Value != "https://example.com/a" {
			t.Fatalf("links: %+v", r.Links.Value)
		}
	})
	t.Run("unsafe_link_card_scheme", func(t *testing.T) {
		for _, uri := range []string{"javascript:alert(1)", "file:///etc/passwd", "ftp://example.com/x"} {
			embed := `"embed":{"$type":"app.bsky.embed.external#view","external":{"uri":"` + uri + `","title":"卡片"}},`
			r, err := bskyFetch(t, post, wrap(`{"text":"短","createdAt":"2026-11-01T00:00:00Z"}`, "", embed), 200)
			if err != nil {
				t.Fatal(err)
			}
			if r.LinkCard.State == "available" {
				t.Fatalf("%s must not become a link card: %+v", uri, r.LinkCard)
			}
		}
		embed := `"embed":{"$type":"app.bsky.embed.external#view","external":{"uri":"http://example.com/card","title":"卡片"}},`
		r, err := bskyFetch(t, post, wrap(`{"text":"短","createdAt":"2026-11-01T00:00:00Z"}`, "", embed), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.LinkCard.State != "available" || r.LinkCard.Value.URL.Value != "http://example.com/card" {
			t.Fatalf("http card must stay: %+v", r.LinkCard)
		}
	})
	t.Run("unsafe_facet_scheme_dropped", func(t *testing.T) {
		text := "打开 example 页面"
		start := strings.Index(text, "example")
		end := start + len("example")
		span := `"index":{"byteStart":` + strconv.Itoa(start) + `,"byteEnd":` + strconv.Itoa(end) + `}`
		facets := `[{` + span + `,"features":[{"$type":"app.bsky.richtext.facet#link","uri":"javascript:alert(1)"}]},` +
			`{` + span + `,"features":[{"$type":"app.bsky.richtext.facet#link","uri":"https://example.com/ok"}]}]`
		body := wrap(`{"text":"`+text+`","createdAt":"2026-11-01T00:00:00Z","facets":`+facets+`}`, "", "")
		r, err := bskyFetch(t, post, body, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Links.Value) != 1 || r.Links.Value[0].URL.Value != "https://example.com/ok" {
			t.Fatalf("links: %+v", r.Links.Value)
		}
	})
	t.Run("unknown_embed_unsupported", func(t *testing.T) {
		embed := `"embed":{"$type":"app.bsky.embed.future#view"},`
		r, err := bskyFetch(t, post, wrap(`{"text":"短","createdAt":"2026-11-01T00:00:00Z"}`, "", embed), 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Media.State != "unsupported" || r.Media.Reason != "unsupported_embed" {
			t.Fatalf("unknown embed: state=%s reason=%s", r.Media.State, r.Media.Reason)
		}
	})
	t.Run("self_label_sensitive_values", func(t *testing.T) {
		for _, val := range []string{"porn", "sexual", "nudity", "graphic-media"} {
			body := wrap(`{"text":"短","createdAt":"2026-11-01T00:00:00Z","labels":{"$type":"com.atproto.label.defs#selfLabels","values":[{"val":"`+val+`"}]}}`, "", "")
			r, err := bskyFetch(t, post, body, 200)
			if err != nil {
				t.Fatal(err)
			}
			if r.Flags["possibly_sensitive"].Value != true {
				t.Fatalf("self label %s: %+v", val, r.Flags)
			}
		}
	})
	t.Run("self_and_appview_labels", func(t *testing.T) {
		self := wrap(`{"text":"短","createdAt":"2026-11-01T00:00:00Z","labels":{"$type":"com.atproto.label.defs#selfLabels","values":[{"val":"sexual"}]}}`, "", "")
		r, err := bskyFetch(t, post, self, 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Flags["possibly_sensitive"].Value != true {
			t.Fatalf("self label: %+v", r.Flags)
		}
		view := wrap(`{"text":"短","createdAt":"2026-11-01T00:00:00Z"}`, "", "")
		view = strings.Replace(view, `"post":{`, `"post":{"labels":[{"val":"graphic-media"}],`, 1)
		r, err = bskyFetch(t, post, view, 200)
		if err != nil {
			t.Fatal(err)
		}
		if r.Flags["possibly_sensitive"].Value != true {
			t.Fatalf("appview label: %+v", r.Flags)
		}
	})
}
