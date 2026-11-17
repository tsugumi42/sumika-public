package linkreader

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fediReply 是一条路由的离线回复；请求没有配置路由就失败，防止多发请求还不被发现。
type fediReply struct {
	status int
	body   string
}

func fediKey(t *testing.T, req *http.Request) string {
	t.Helper()
	if req.Method == http.MethodPost {
		b, _ := io.ReadAll(req.Body)
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			t.Fatalf("POST body is not JSON: %s", b)
		}
		key := req.URL.Path
		if v, ok := m["noteId"].(string); ok {
			key += "|" + v
		}
		if v, ok := m["username"].(string); ok {
			key += "|" + v
		}
		return key
	}
	return req.URL.Path + "?" + req.URL.RawQuery
}

func fediFetcher(t *testing.T, routes map[string]fediReply) (*Fetcher, *int) {
	t.Helper()
	calls := 0
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" || req.Header.Get("Referer") != "" {
			t.Fatal("fediverse request carries Cookie, Authorization or Referer")
		}
		key := fediKey(t, req)
		rep, ok := routes[key]
		if !ok {
			t.Fatalf("unexpected request %s", key)
		}
		if req.Method == http.MethodPost && req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("POST without JSON content type: %s", key)
		}
		return response(rep.status, rep.body), nil
	})
	return f, &calls
}

// fediEdit 复制一份真实夹具，允许改写其中的字段（用于形状测试）；原文件不动。
func fediEdit(t *testing.T, name string, edit func(map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(fixture(t, name)), &m); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fediOK(t *testing.T, name string, edit func(map[string]any)) fediReply {
	return fediReply{status: 200, body: fediEdit(t, name, edit)}
}

// 夹具来自 2026-11-07 的匿名实测（经代理，共 12 次请求），只保留解析读取的字段。
var fediLiveRoutes = func(t *testing.T) map[string]fediReply {
	return map[string]fediReply{
		"/api/notes/show|fakenote00000001":          fediOK(t, "fediverse-misskey-text", nil),
		"/api/notes/show|fakenote00000002":          fediOK(t, "fediverse-misskey-video", nil),
		"/api/notes/show|fakenote00000003":          fediOK(t, "fediverse-misskey-gif", nil),
		"/api/notes/show|fakenote00000004":          fediOK(t, "fediverse-misskey-poll", nil),
		"/api/notes/show|fakenote00000005":          fediOK(t, "fediverse-misskey-quote", nil),
		"/api/notes/show|zzzzzzzzzz":                {status: 400, body: fixture(t, "fediverse-misskey-nosuchnote")},
		"/api/users/show|example_user":              fediOK(t, "fediverse-misskey-user", nil),
		"/api/v1/statuses/9000000000000000001?":     fediOK(t, "fediverse-mastodon-image", nil),
		"/api/v1/statuses/9000000000000000002?":     fediOK(t, "fediverse-mastodon-video", nil),
		"/api/v1/statuses/9000000000000000003?":     fediOK(t, "fediverse-mastodon-gifv", nil),
		"/api/v1/statuses/9000000000000000004?":     fediOK(t, "fediverse-mastodon-card", nil),
		"/api/v1/accounts/lookup?acct=example_user": fediOK(t, "fediverse-mastodon-account", nil),
	}
}

func TestFediverseLiveFixtures(t *testing.T) {
	routes := fediLiveRoutes(t)
	f, calls := fediFetcher(t, routes)
	ctx := context.Background()
	fetch := func(raw string) Result {
		t.Helper()
		r, err := f.Fetch(ctx, link(t, raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		return r
	}

	text := fetch("https://misskey.io/notes/fakenote00000001")
	if text.Title.State != "not_returned" || text.Title.Reason != "platform_has_no_title" || !strings.Contains(text.Text.Value, "编造的联邦宇宙示例正文") ||
		text.Author.Name.Value != "编造用户" || text.Author.Account.Value != "example_user" || text.Author.ID.Value != "fakeuser00000001" || text.Stats["reactions"].Value != 4 ||
		text.Flags["content_warning"].Value != "" || text.Media.State != "available" || len(text.Media.Value) != 0 || len(text.Blocks) != 1 {
		t.Fatalf("text note: %+v", text)
	}

	video := fetch("https://misskey.io/notes/fakenote00000002")
	if len(video.Media.Value) != 1 || video.Media.Value[0].Type != "video" || video.Media.Value[0].Sensitive.Value != true ||
		video.Media.Value[0].Width.State != "not_returned" || video.Flags["sensitive"].Value != true || !strings.Contains(video.Text.Value, "编造的联邦宇宙示例正文") ||
		len(video.Blocks) != 2 || video.Blocks[1].Type != "video" {
		t.Fatalf("video note: %+v", video.Media)
	}
	gif := fetch("https://misskey.io/notes/fakenote00000003")
	if len(gif.Media.Value) != 1 || gif.Media.Value[0].Type != "gif" || gif.Media.Value[0].Width.Value != 358 || gif.Media.Value[0].Height.Value != 358 ||
		!strings.Contains(gif.Flags["content_warning"].Value.(string), "编造的联邦宇宙") || gif.Flags["sensitive"].Value != false {
		t.Fatalf("gif note: %+v", gif.Media)
	}
	poll := fetch("https://misskey.io/notes/fakenote00000004")
	if !strings.Contains(poll.Text.Value, "投票：这是编造的联邦宇宙示") || poll.Flags["poll_expires_at"].Value != "2026-11-06T16:03:27.402Z" {
		t.Fatalf("poll: %+v", poll.Text)
	}
	quote := fetch("https://misskey.io/notes/fakenote00000005")
	if quote.Quote.State != "available" || quote.Quote.Value.ID != "misskey.io/fakenote00000004" || !strings.Contains(quote.Quote.Value.Text.Value, "投票") ||
		quote.MoreQuotes.Value != false || len(quote.Blocks) != 2 || quote.Blocks[0].Type != "text" || quote.Blocks[1].Type != "quote" || !strings.Contains(quote.Flags["content_warning"].Value.(string), "编造的联邦宇宙") {
		t.Fatalf("quote: %+v", quote.Quote)
	}
	user := fetch("https://misskey.io/@example_user")
	if user.Title.Value != "编造用户" || user.Stats["notes"].Value != 285720 || user.Published.Value.Format(time.RFC3339Nano) != "2019-04-14T17:11:39.168Z" || user.Author.Avatar.State != "available" {
		t.Fatalf("user: %+v", user)
	}
	// 简介里的 Markdown 链接换成“文字 地址”，punycode 域名换回原字
	if strings.Contains(user.Text.Value, "](") || strings.Contains(user.Text.Value, "xn--") {
		t.Fatalf("user description markup: %q", user.Text.Value)
	}

	img := fetch("https://mastodon.social/@example_user/9000000000000000001")
	if img.Author.Account.Value != "example_user" || len(img.Media.Value) != 1 || img.Media.Value[0].Type != "image" || img.Media.Value[0].Width.Value != 3356 ||
		img.Media.Value[0].Height.Value != 2225 || img.Stats["favourites"].Value != 155 || img.Flags["sensitive"].Value != false || len(img.Blocks) != 2 {
		t.Fatalf("mastodon image: %+v", img.Media)
	}
	vid := fetch("https://mastodon.social/@example_viewer/9000000000000000002")
	if len(vid.Media.Value) != 1 || vid.Media.Value[0].Type != "video" || vid.Media.Value[0].Duration.Value != 14.5 || vid.Media.Value[0].Width.Value != 1918 {
		t.Fatalf("mastodon video: %+v", vid.Media)
	}
	gifv := fetch("https://mastodon.social/@example_org/9000000000000000003")
	if len(gifv.Media.Value) != 1 || gifv.Media.Value[0].Type != "gif" || gifv.Media.Value[0].Duration.Value != 38.85 {
		t.Fatalf("mastodon gifv: %+v", gifv.Media)
	}
	card := fetch("https://mastodon.social/@example_user/9000000000000000004")
	if card.LinkCard.State != "available" || card.LinkCard.Value.URL.Value != "https://example.com/watch?v=fixture" || card.LinkCard.Value.Site.Value != "示例视频站" || card.Text.Value != "https://example.com/watch?v=fixture" {
		t.Fatalf("mastodon card: %+v", card.LinkCard)
	}
	// 链接已在正文里：只补卡片标题一行，不重复链接
	if b := card.Blocks[len(card.Blocks)-1]; b.Type != "link_card" || b.Text != card.LinkCard.Value.Title.Value || b.Text == "" {
		t.Fatalf("mastodon card block: %+v", card.Blocks)
	}
	acct := fetch("https://mastodon.social/@example_user")
	if acct.Title.Value != "编造用户" || acct.Stats["statuses"].Value != 82766 || !strings.HasPrefix(acct.Text.Value, "这是编造的") {
		t.Fatalf("mastodon account: %+v", acct)
	}
	if *calls != 11 {
		t.Fatalf("requests = %d; want 11 successful fetches", *calls)
	}

	_, err := f.Fetch(ctx, link(t, "https://misskey.io/notes/zzzzzzzzzz"))
	wantError(t, err, "not_found")
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Reason != "NO_SUCH_NOTE" || fe.Status != 400 {
		t.Fatalf("no such note: %+v", err)
	}
}

func TestFediverseFailureShapes(t *testing.T) {
	note := "https://misskey.io/notes/fakenote00000001"
	status := "https://mastodon.social/@example_user/9000000000000000001"
	account := "https://mastodon.social/@example_user"
	noteKey := "/api/notes/show|fakenote00000001"
	statusKey := "/api/v1/statuses/9000000000000000001?"
	acctKey := "/api/v1/accounts/lookup?acct=example_user"

	for _, c := range []struct {
		name, url, key string
		reply          fediReply
		kind           string
	}{
		{"misskey_404", note, noteKey, fediReply{404, ""}, "not_found"},
		{"misskey_410", note, noteKey, fediReply{410, ""}, "not_found"},
		{"misskey_access_denied", note, noteKey, fediReply{403, `{"error":{"code":"ACCESS_DENIED","message":"denied"}}`}, "login_required"},
		{"misskey_403_plain", note, noteKey, fediReply{403, ""}, "login_required"},
		{"misskey_malformed", note, noteKey, fediReply{200, `{"id":`}, "invalid_response"},
		{"misskey_html_challenge", note, noteKey, fediReply{200, "<html>wait</html>"}, "invalid_response"},
		{"mastodon_404", status, statusKey, fediReply{404, `{"error":"Record not found"}`}, "not_found"},
		{"mastodon_401", status, statusKey, fediReply{401, `{"error":"The access token is invalid"}`}, "login_required"},
		{"mastodon_malformed", status, statusKey, fediReply{200, `[`}, "invalid_response"},
		{"mastodon_account_malformed", account, acctKey, fediReply{200, `{"username":`}, "invalid_response"},
		{"misskey_note_id_mismatch", note, noteKey, fediOK(t, "fediverse-misskey-quote", nil), "identity_mismatch"},
		{"mastodon_account_name_mismatch", status, statusKey, fediOK(t, "fediverse-mastodon-image", func(m map[string]any) { m["account"].(map[string]any)["username"] = "Other" }), "identity_mismatch"},
		{"mastodon_account_acct_mismatch", account, acctKey, fediOK(t, "fediverse-mastodon-account", func(m map[string]any) { m["acct"] = "example_user@other.example" }), "identity_mismatch"},
		{"mastodon_status_id_mismatch", status, statusKey, fediOK(t, "fediverse-mastodon-image", func(m map[string]any) { m["id"] = "9000000000000000099" }), "identity_mismatch"},
		{"misskey_user_remote_host", "https://misskey.io/@example_user", "/api/users/show|example_user", fediOK(t, "fediverse-misskey-user", func(m map[string]any) { m["host"] = "other.example" }), "identity_mismatch"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := fediFetcher(t, map[string]fediReply{c.key: c.reply})
			_, err := f.Fetch(context.Background(), link(t, c.url))
			wantError(t, err, c.kind)
		})
	}

	// 429：平台冷却，同一平台的后续请求不再发出；另一平台不受影响。
	t.Run("rate_limited_cooldown", func(t *testing.T) {
		now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
		routes := map[string]fediReply{
			noteKey:                        {429, ""},
			"/api/users/show|example_user": fediOK(t, "fediverse-misskey-user", nil),
			statusKey:                      fediOK(t, "fediverse-mastodon-image", nil),
		}
		calls := 0
		f, err := NewFetcher(FetchOptions{Rate: RateOptions{Now: func() time.Time { return now }}, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			rep := routes[fediKey(t, req)]
			return response(rep.status, rep.body), nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Fetch(context.Background(), link(t, note))
		wantError(t, err, "rate_limited")
		_, err = f.Fetch(context.Background(), link(t, "https://misskey.io/@example_user"))
		wantError(t, err, "rate_limited")
		if calls != 1 {
			t.Fatalf("cooldown sent another misskey request: %d", calls)
		}
		if _, err := f.Fetch(context.Background(), link(t, status)); err != nil {
			t.Fatal("mastodon cooldown leaked from misskey", err)
		}
	})
}

func TestFediverseFieldShapes(t *testing.T) {
	note := "https://misskey.io/notes/fakenote00000001"
	noteKey := "/api/notes/show|fakenote00000001"
	fetchWith := func(t *testing.T, edit func(map[string]any)) Result {
		t.Helper()
		f, _ := fediFetcher(t, map[string]fediReply{noteKey: fediOK(t, "fediverse-misskey-text", edit)})
		r, err := f.Fetch(context.Background(), link(t, note))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	// 超长正文：截到 800 rune，并标记截断。
	long := strings.Repeat("字", 2000)
	r := fetchWith(t, func(m map[string]any) { m["text"] = long })
	if !r.Text.Truncated || utf8.RuneCountInString(r.Text.Value) != 800 {
		t.Fatalf("long text: truncated=%v runes=%d", r.Text.Truncated, utf8.RuneCountInString(r.Text.Value))
	}

	// 文件字段缺失与空数组不同：缺失是 not_returned，空数组是 available 的零媒体。
	r = fetchWith(t, func(m map[string]any) { delete(m, "files") })
	if r.Media.State != "not_returned" {
		t.Fatalf("missing files: %+v", r.Media)
	}
	r = fetchWith(t, func(m map[string]any) { m["files"] = []any{} })
	if r.Media.State != "available" || len(r.Media.Value) != 0 {
		t.Fatalf("zero media: %+v", r.Media)
	}

	// 正文 null 且无媒体：正文为 not_returned，不当成空字符串成功。
	r = fetchWith(t, func(m map[string]any) { m["text"] = nil })
	if r.Text.State != "not_returned" || r.Text.Reason != "empty_text" {
		t.Fatalf("null text: %+v", r.Text)
	}

	// 无法识别的 MIME 不展示，计数保留。
	r = fetchWith(t, func(m map[string]any) {
		m["files"] = []any{map[string]any{"type": "application/pdf", "url": "https://media.misskeyusercontent.jp/io/a.pdf", "isSensitive": false}}
	})
	if len(r.Media.Value) != 0 || r.Flags["unsupported_files"].Value != int64(1) {
		t.Fatalf("unsupported mime: media=%+v flags=%+v", r.Media, r.Flags["unsupported_files"])
	}

	// 敏感标记：文件缺 isSensitive 时不能当成 false。
	r = fetchWith(t, func(m map[string]any) {
		m["files"] = []any{map[string]any{"type": "image/png", "url": "https://media.misskeyusercontent.jp/io/b.png"}}
	})
	if r.Flags["sensitive"].State != "not_returned" || r.Media.Value[0].Sensitive.State != "not_returned" {
		t.Fatalf("unknown sensitive became known: %+v", r.Flags["sensitive"])
	}

	// 没有 reactionCount 时对 reactions 字典求和；空字典是 0。
	r = fetchWith(t, func(m map[string]any) {
		delete(m, "reactionCount")
		m["reactions"] = map[string]any{"a": 2, "b": 3}
	})
	if r.Stats["reactions"].Value != 5 {
		t.Fatalf("reaction sum: %+v", r.Stats["reactions"])
	}

	// 引用：renoteId 有值但 renote 缺失，记 renote_omitted。
	f, _ := fediFetcher(t, map[string]fediReply{"/api/notes/show|fakenote00000005": fediOK(t, "fediverse-misskey-quote", func(m map[string]any) { delete(m, "renote") })})
	r2, err := f.Fetch(context.Background(), link(t, "https://misskey.io/notes/fakenote00000005"))
	if err != nil || r2.Quote.State != "not_returned" || r2.Quote.Reason != "renote_omitted" {
		t.Fatalf("renote omitted: %v %+v", err, r2.Quote)
	}
	// 嵌套引用的 id 与 renoteId 不一致：只标这一格 identity_mismatch，不让整条失败。
	f, _ = fediFetcher(t, map[string]fediReply{"/api/notes/show|fakenote00000005": fediOK(t, "fediverse-misskey-quote", func(m map[string]any) { m["renoteId"] = "zzzzzzzzzz" })})
	r2, err = f.Fetch(context.Background(), link(t, "https://misskey.io/notes/fakenote00000005"))
	if err != nil || r2.Quote.State != "failed" || r2.Quote.Reason != "identity_mismatch" {
		t.Fatalf("quote mismatch: %v %+v", err, r2.Quote)
	}

	// 头像和媒体主机：未知主机必须不安全；两个已知代理主机放行。
	r = fetchWith(t, func(m map[string]any) {
		m["files"] = []any{map[string]any{"type": "image/png", "url": "https://evil.example/x.png", "isSensitive": false}}
	})
	if r.Media.Value[0].URL.State != "failed" || r.Media.Value[0].URL.Reason != "unsafe_target" {
		t.Fatalf("unknown media host accepted: %+v", r.Media.Value[0].URL)
	}
	if got := mastodonText("<p>a<br />b</p><p>c &amp; d</p>"); got != "a\nb\nc & d" {
		t.Fatalf("mastodon html text: %q", got)
	}
}

func TestFediverseMastodonReblog(t *testing.T) {
	routes := map[string]fediReply{
		"/api/v1/statuses/9000000000000000002?": fediOK(t, "fediverse-mastodon-video", func(m map[string]any) {
			var inner map[string]any
			_ = json.Unmarshal([]byte(fixture(t, "fediverse-mastodon-image")), &inner)
			m["reblog"] = inner
			m["content"] = ""
			m["media_attachments"] = []any{}
		}),
	}
	f, _ := fediFetcher(t, routes)
	r, err := f.Fetch(context.Background(), link(t, "https://mastodon.social/@example_viewer/9000000000000000002"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Quote.State != "available" || r.Quote.Value.ID != "mastodon.social/9000000000000000001" || r.Quote.Value.Text.State != "available" || r.MoreQuotes.Value != false {
		t.Fatalf("reblog quote: %+v", r.Quote)
	}
	if len(r.Blocks) != 1 || r.Blocks[0].Type != "quote" {
		t.Fatalf("reblog blocks: %+v", r.Blocks)
	}
}
