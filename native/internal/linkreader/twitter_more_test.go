package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// 以下 ID、名字都是编的；字段形状照 fx 的 OpenAPI（2.0.0）和公开样本。

const fxPoll = `{"code":200,"status":{"id":"90001","text":"投哪个","media":{},"author":{"name":"纸云","screen_name":"paper_cloud"},` +
	`"poll":{"choices":[{"label":"纸船","count":3118,"percentage":81.9},{"label":"纸鸢","count":690,"percentage":18.1}],"total_votes":3808,"ends_at":"2026-10-08T18:00:21Z","time_left_en":"Final results"}}}`

func TestXPollFromFX(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, fxPoll), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Poll == nil || len(r.Poll.Choices) != 2 || r.Poll.Total.Value != 3808 || r.Poll.Choices[0].Label != "纸船" || r.Poll.Choices[0].Count.Value != 3118 || r.Poll.Choices[1].Percent.Value != 18.1 || r.Poll.EndsAt.State != "available" {
		t.Fatalf("投票没映射：%+v", r.Poll)
	}
	if r.Blocks[1].Type != "poll" {
		t.Fatalf("投票块没排在正文后：%+v", r.Blocks)
	}
}

func TestXPollFieldsMayBeMissing(t *testing.T) {
	body := `{"code":200,"status":{"id":"90001","text":"t","media":{},"poll":{"choices":[{"label":"甲"},{"count":2}]}}}`
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Poll == nil || r.Poll.Total.State == "available" || r.Poll.EndsAt.State == "available" || r.Poll.Choices[0].Count.State == "available" || r.Poll.Choices[1].Percent.State == "available" {
		t.Fatalf("缺的字段被当成 0：%+v", r.Poll)
	}
	// 空选项等于没有投票
	empty := `{"code":200,"status":{"id":"90001","text":"t","media":{},"poll":{"choices":[]}}}`
	f = testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, empty), nil })
	r, err = f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil || r.Poll != nil {
		t.Fatalf("空投票：%v %+v", err, r.Poll)
	}
}

func TestXPollFromVX(t *testing.T) {
	fx500 := func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.fxtwitter.com" {
			return response(500, `{}`), nil
		}
		return response(200, `{"tweetID":"90001","text":"投哪个","user_name":"纸云","user_screen_name":"paper_cloud","pollData":{"options":[{"name":"纸船","percent":81.88,"votes":3118},{"name":"纸鸢","percent":18.12,"votes":690}]}}`), nil
	}
	f := testFetcher(t, fx500)
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "vx" || r.Poll == nil || r.Poll.Total.Value != 3808 || r.Poll.EndsAt.State == "available" || r.Poll.Choices[1].Count.Value != 690 {
		t.Fatalf("VX 投票：%+v %s", r.Poll, r.Source)
	}
}

const fxTombstoneQuote = `{"code":200,"status":{"id":"90001","text":"转一下","media":{},"quote":{"type":"tombstone","provider":"twitter","reason":"deleted","message":"gone","id":"90002"}}}`

func TestXQuoteTombstone(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, fxTombstoneQuote), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Quote.State == "available" || r.Quote.Reason != "quote_deleted" {
		t.Fatalf("墓碑被当成引用：%+v", r.Quote)
	}
	var found bool
	for _, b := range r.Blocks {
		if b.Type == "quote_unavailable" && b.Reason == "deleted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("引用已删没有留下块：%+v", r.Blocks)
	}
}

func TestXStatusTombstone(t *testing.T) {
	for reason, kind := range map[string]string{"deleted": "not_found", "unavailable": "not_found", "suspended": "not_found", "private": "login_required", "blocked": "login_required"} {
		body := `{"code":200,"status":{"type":"tombstone","provider":"twitter","reason":"` + reason + `","message":"m","id":"90001"}}`
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		_, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
		wantError(t, err, kind)
		if fe := err.(*FetchError); fe.Reason != "tombstone_"+reason {
			t.Errorf("%s 的原因丢了：%+v", reason, fe)
		}
	}
}

// fx 的 status 只带一层引用：被引的帖子自己还引用别人，要单独取一次才知道。
func TestXQuoteDepthRefetch(t *testing.T) {
	outer := `{"code":200,"status":{"id":"90001","text":"外","media":{},"quote":{"id":"90002","text":"中","media":{}}}}`
	for _, c := range []struct {
		name     string
		inner    func() *http.Response
		more     string // available 或 not_returned
		wantMore bool
	}{
		{"有更深引用", func() *http.Response {
			return response(200, `{"code":200,"status":{"id":"90002","text":"中","media":{},"quote":{"id":"90003","text":"内","media":{}}}}`)
		}, "available", true},
		{"没有更深引用", func() *http.Response {
			return response(200, `{"code":200,"status":{"id":"90002","text":"中","media":{}}}`)
		}, "available", false},
		{"再取失败", func() *http.Response { return response(500, `{}`) }, "not_returned", false},
		{"再取回墓碑", func() *http.Response { return response(200, `{"code":404,"status":null}`) }, "not_returned", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			requests := 0
			f := testFetcher(t, fxOnly(func(req *http.Request) (*http.Response, error) {
				requests++
				if strings.HasSuffix(req.URL.Path, "90002") {
					return c.inner(), nil
				}
				return response(200, outer), nil
			}))
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
			if err != nil {
				t.Fatal(err)
			}
			q := r.Quote.Value
			if q == nil || q.MoreQuotes.State != c.more || q.MoreQuotes.Value != c.wantMore {
				t.Fatalf("更深引用状态：%+v", q)
			}
			if requests != 2 {
				t.Fatalf("多取了不该取的：%d 次", requests)
			}
		})
	}
}
