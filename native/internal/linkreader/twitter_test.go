package linkreader

import (
	"context"
	"net/http"
	"regexp"
	"testing"
)

func TestVXReplay(t *testing.T) {
	for _, c := range []struct{ name, id string }{{"plain", "1900000000000000014"}, {"photo", "1900000000000000008"}, {"four", "1900000000000000011"}, {"video", "1900000000000000013"}, {"quote", "1900000000000000005"}, {"thread", "1900000000000000012"}} {
		t.Run(c.name, func(t *testing.T) {
			f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "api.fxtwitter.com" {
					return response(503, ""), nil
				}
				return response(200, fixture(t, "vx-"+c.name)), nil
			})
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/"+c.id))
			if err != nil {
				t.Fatal(err)
			}
			if r.ID != c.id || r.Source != "vx" || r.Author.ID.State != "not_returned" || r.Text.State != "available" {
				t.Fatalf("invalid VX result: %+v", r)
			}
			if c.name == "four" && (len([]rune(r.Text.Value)) != 2156 || len(r.Media.Value) != 4) {
				t.Fatal("VX Note truncated")
			}
			if c.name == "quote" && (r.Quote.State != "available" || r.Quote.Value.ID != "1900000000000000004" || r.Quote.Value.CleanURL != "https://x.com/ex_author22/status/1900000000000000004") {
				t.Fatal("VX quote lost")
			}
			if c.name == "thread" && r.ReplyTo.Value != "1900000000000000010" {
				t.Fatal("VX parent lost")
			}
		})
	}
	t.Run("missing_html", func(t *testing.T) {
		f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "api.fxtwitter.com" {
				return response(503, ""), nil
			}
			return response(200, fixture(t, "vx-missing")), nil
		})
		_, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/2115477952339999999"))
		wantError(t, err, "invalid_response")
	})
}

var cardURLPattern = regexp.MustCompile(`("url": )"https://example\.com/fixture/11",(\s*)"title"`)

// 外链卡的地址不是 http、https 时不出卡：状态 failed、原因 unsafe_target，卡里不带任何内容。
func TestXLinkCardRejectsNonWebScheme(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "ftp://example.invalid/x", "data:text/html,hi", "//example.invalid/x", "https:///nohost"} {
		t.Run(bad, func(t *testing.T) {
			page := cardURLPattern.ReplaceAllString(fixture(t, "x-linkcard"), `${1}"`+bad+`",${2}"title"`)
			if page == fixture(t, "x-linkcard") {
				t.Fatal("夹具没替换成功")
			}
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, page), nil })
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/example/status/1900000000000000017"))
			if err != nil {
				t.Fatal(err)
			}
			if r.LinkCard.State != "failed" || r.LinkCard.Reason != "unsafe_target" || r.LinkCard.Value.Title.State != "" {
				t.Fatalf("坏协议的卡不该出：%+v", r.LinkCard)
			}
		})
	}
}
