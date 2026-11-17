package linkreader

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

// #30：用户发的是没账号的 /i/web/status/、/i/status/，或者账号写错了，发出去的链接按取到的作者账号写；
// CleanURL 仍等于输入链接的 Clean（缓存校验靠它），被引推文的链接走 fx 线路时同样带作者账号。
func TestXShareURLUsesFetchedAuthor(t *testing.T) {
	const id = "1900000000000000016"
	f := testFetcher(t, fxOnly(func(req *http.Request) (*http.Response, error) {
		got := strings.TrimPrefix(req.URL.Path, "/2/status/")
		if _, err := os.Stat("testdata/x-public-" + got + ".json"); err != nil {
			return response(404, `{}`), nil
		}
		return response(200, fixture(t, "x-public-"+got)), nil
	}))
	for _, in := range []string{
		"https://x.com/i/web/status/" + id,
		"https://x.com/i/status/" + id,
		"https://x.com/X/status/" + id, // 账号写错：实际作者是别人
	} {
		t.Run(in, func(t *testing.T) {
			l := link(t, in)
			r, err := f.Fetch(context.Background(), l)
			if err != nil {
				t.Fatal(err)
			}
			if r.CleanURL != l.Clean {
				t.Fatalf("CleanURL 被改了：%s，输入的 Clean 是 %s", r.CleanURL, l.Clean)
			}
			if got := r.ShareURL(); got != "https://x.com/ex_author08/status/"+id {
				t.Fatalf("主推文链接：%s", got)
			}
			if r.Source != "fx_v2" && r.Source != "fx" {
				t.Fatalf("应当走 fx：%s", r.Source)
			}
			if r.Quote.State != "available" || r.Quote.Value.CleanURL != "https://x.com/ex_author09/status/1900000000000000001" {
				t.Fatalf("fx 被引推文链接：%+v", r.Quote)
			}
		})
	}
}

// 取不到账号、账号不像账号、不是推文：照旧用 CleanURL，不拼出 /i/status/ 也不拼坏链接。
func TestXShareURLFallsBack(t *testing.T) {
	base := func() Result {
		r := newResult(Link{Platform: "x", Kind: "post", ID: "1900000000000000001", Clean: "https://x.com/paper_cloud/status/1900000000000000001"}, "fx")
		return r
	}
	r := base()
	if got := r.ShareURL(); got != r.CleanURL {
		t.Fatalf("没取到账号：%s", got)
	}
	r = base()
	r.Author.Account = available("bad-name!")
	if got := r.ShareURL(); got != r.CleanURL {
		t.Fatalf("账号不像账号：%s", got)
	}
	r = base()
	r.Author.Account = available("")
	if got := r.ShareURL(); got != r.CleanURL {
		t.Fatalf("空账号：%s", got)
	}
	r = base()
	r.Author.Account = available("@paper_lamp")
	if got := r.ShareURL(); got != "https://x.com/paper_lamp/status/1900000000000000001" {
		t.Fatalf("带 @ 的账号：%s", got)
	}
	r = base()
	r.Author.Account = available("paper_lamp")
	r.Platform, r.Kind, r.CleanURL = "bluesky", "post", "https://example.invalid/bsky/1"
	if got := r.ShareURL(); got != r.CleanURL {
		t.Fatalf("不是推文：%s", got)
	}
	var none *Result
	if none.ShareURL() != "" {
		t.Fatal("nil 应当是空")
	}
}
