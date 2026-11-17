package linkreader

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestXCompletePublicReplay(t *testing.T) {
	for _, c := range []struct {
		id, types, expansion string
		chars, articleBlocks int
	}{
		{"1900000000000000024", "video", "short", 163, 0},
		{"1900000000000000022", "image", "short", 29, 0},
		{"1900000000000000023", "image,image", "long", 4, 0},
		{"1900000000000000021", "", "long", 255, 0},
		{"1900000000000000020", "image,video,image", "long", 566, 0},
		{"1900000000000000019", "", "long", 1805, 0},
		{"1900000000000000016", "video", "long", 28, 0},
		{"1900000000000000017", "", "short", 226, 0},
		{"1900000000000000015", "video,video", "long", 51, 0},
		{"1900000000000000018", "", "long", 43, 172},
		{"1900000000000000006", "", "long", 126, 286},
	} {
		t.Run(c.id, func(t *testing.T) {
			f := testFetcher(t, fxOnly(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
					t.Fatal("non-anonymous")
				}
				id := strings.TrimPrefix(req.URL.Path, "/2/status/")
				name := "x-public-" + id
				if id == "1900000000000000017" {
					// 这张也用来验证外链卡的坏协议，夹具按用途命名。
					name = "x-linkcard"
				}
				if _, err := os.Stat("testdata/" + name + ".json"); err != nil {
					// 被引的那一条没有夹具：按取不到处理（补“引用里还有引用”时会多取一次）
					return response(404, `{}`), nil
				}
				return response(200, fixture(t, name)), nil
			}))
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/example/status/"+c.id+"?a=share&s=20"))
			if err != nil {
				t.Fatal(err)
			}
			if r.CleanURL != "https://x.com/example/status/"+c.id || r.Author.Avatar.State != "available" || r.Author.Account.State != "available" || r.Published.State != "available" || utf8.RuneCountInString(r.Text.Value) != c.chars || r.Text.Truncated || r.Expansion.Value != c.expansion {
				t.Fatalf("post fields: %+v", r)
			}
			for _, key := range []string{"likes", "reposts", "replies", "views", "bookmarks"} {
				if r.Stats[key].State != "available" {
					t.Fatal(key, r.Stats[key])
				}
			}
			if c.articleBlocks == 0 {
				types := []string{}
				for _, m := range r.Media.Value {
					types = append(types, m.Type)
				}
				if strings.Join(types, ",") != c.types {
					t.Fatal("media order", types)
				}
			} else {
				if r.Article.State != "available" || r.Article.Truncated || len(r.Blocks) != c.articleBlocks+3 || r.Blocks[1].Type != "article_title" || r.Blocks[2].Type != "article_cover" || r.Blocks[2].Media.Width.State != "available" {
					t.Fatalf("article state/order: %+v", r.Article)
				}
			}
			if c.id == "1900000000000000019" && !strings.HasSuffix(r.Text.Value, "这是编造的") {
				t.Fatal("Note tail lost")
			}
			if c.id == "1900000000000000017" {
				card := r.LinkCard.Value
				if !strings.Contains(card.Title.Value, "编造的示例") || card.Site.Value != "example.com" || card.Description.State != "available" || card.Image.Value.Width.Value != 800 || len(r.Links.Value) != 1 || r.Links.Value[0].URL.Value != "https://example.com/fixture/11" || strings.Contains(r.Text.Value, "t.co/") {
					t.Fatal("external URL or preview lost", card.Title.Value, card.Site.Value, card.Image.Value.Width.Value, r.Links)
				}
			}
			if c.id == "1900000000000000020" {
				m := r.Media.Value[1]
				if len(m.Variants.Value) != 5 || m.Variants.Value[4].Width.Value != 1448 || m.Variants.Value[4].Height.Value != 2046 || m.Duration.Value != 28.8 || m.Size.State != "not_returned" || m.Thumbnail.State != "available" || len(r.Links.Value) != 0 {
					t.Fatal("variants or media facets", m)
				}
			}
			if c.id == "1900000000000000006" {
				ids := []string{}
				for _, b := range r.Blocks {
					if b.Type == "quote" {
						if b.State != "available" || b.Quote.Author.Account.State != "available" || b.Quote.Article.State != "available" {
							t.Fatal("inline card", b)
						}
						ids = append(ids, b.Quote.ID)
					}
				}
				if !reflect.DeepEqual(ids, []string{"1900000000000000003", "1900000000000000002"}) || f.Requests() != 4 { // fx 主请求、两条引用卡，加上同时发出的 vx 一个
					t.Fatal("inline order/requests", ids, f.Requests())
				}
			}
		})
	}
}

const xArticleSynthetic = `{"code":200,"status":{"id":"90001","text":"引导","media":{},"article":{"title":"标题","content":{"blocks":[{"type":"unstyled","text":"第一段"},{"type":"atomic","text":" ","entityRanges":[{"key":1}]},{"type":"atomic","text":" ","entityRanges":[{"key":2}]},{"type":"atomic","text":" ","entityRanges":[{"key":2}]},{"type":"unstyled","text":"尾段"}],"entityMap":[{"key":"2","value":{"type":"TWEET","data":{"tweetId":"90002"}}},{"key":"1","value":{"type":"MEDIA","data":{"mediaItems":[{"mediaId":"90004"},{"mediaId":"90003"}]}}}]},"media_entities":[{"media_id":"90003","media_info":{"__typename":"ApiImage","original_img_url":"https://pbs.twimg.com/b.jpg","original_img_width":300}},{"media_id":"90004","media_info":{"__typename":"ApiImage","original_img_url":"https://pbs.twimg.com/a.jpg","original_img_width":400}}]}}}`

func TestXArticleEntityOrderAndFailures(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "unavailable_inline"}[fail], func(t *testing.T) {
			f := testFetcher(t, fxOnly(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "90001") {
					return response(200, xArticleSynthetic), nil
				}
				if fail {
					return response(404, `{}`), nil
				}
				return response(200, `{"code":200,"status":{"id":"90002","text":"卡片正文","author":{"name":"测试作者","screen_name":"example"},"media":{}}}`), nil
			}))
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/example/status/90001"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Blocks[3].Media.Width.Value != 400 || r.Blocks[4].Media.Width.Value != 300 || r.Blocks[7].Text != "尾段" || r.Article.Value != "第一段\n尾段" {
				t.Fatal("entity keys/order", r.Blocks)
			}
			if fail {
				if !r.Article.Truncated || r.Blocks[5].State != "not_returned" || r.Blocks[5].Reason != "not_found" || r.Blocks[5].Quote.Text.State != "not_returned" {
					t.Fatal("false inline success", r.Blocks[5])
				}
			} else if r.Article.Truncated || r.Blocks[5].Quote.Text.Value != "卡片正文" || r.Blocks[6].Quote.Text.Value != "卡片正文" || f.Requests() != 3 { // 同一张卡只取一次；vx 同时发的那一个也算
				t.Fatal("inline not reused", r.Blocks)
			}
		})
	}
	for _, c := range []struct{ name, body string }{
		{"missing_entity", strings.Replace(xArticleSynthetic, `"key":"1"`, `"key":"8"`, 1)},
		{"unknown_block", strings.Replace(xArticleSynthetic, `"type":"unstyled"`, `"type":"future"`, 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			var p struct {
				Status *xPost `json:"status"`
			}
			if err := json.Unmarshal([]byte(c.body), &p); err != nil {
				t.Fatal(err)
			}
			r, err := mapX(Link{Platform: "x", Kind: "post", ID: "90001"}, p.Status, 0, map[string]bool{})
			if err != nil || !r.Article.Truncated {
				t.Fatal("missing article data marked complete", r.Article, err)
			}
		})
	}
}

func TestXExpansionBoundaries(t *testing.T) {
	for _, c := range []struct{ name, text, media, extra, want string }{
		{"edge", strings.Repeat("😀", 300), `[{"type":"photo"},{"type":"photo"},{"type":"video"}]`, "", "short"},
		{"long_text", strings.Repeat("字", 301), `[]`, "", "long"},
		{"three_images", "x", `[{"type":"photo"},{"type":"photo"},{"type":"photo"}]`, "", "long"},
		{"two_videos", "x", `[{"type":"gif"},{"type":"video"}]`, "", "long"},
		{"gif", "x", `[{"type":"gif"}]`, "", "short"},
		{"quote", "x", `[]`, `,"quote":{"id":"90002","text":"q","quote":{"id":"90003","text":"deep"}}`, "long"},
		{"preview_article", "x", `[]`, `,"article":{"title":"只有标题"}`, "long"},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := `{"code":200,"status":{"id":"90001","text":` + strconvQuote(c.text) + `,"media":{"all":` + c.media + `}` + c.extra + `}}`
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/example/status/90001"))
			if err != nil || r.Expansion.Value != c.want {
				t.Fatal(r.Expansion, err)
			}
			if c.name == "quote" && (!r.Quote.Value.MoreQuotes.Value || r.Quote.Value.Quote.Value != nil || r.Quote.Value.Quote.Reason != "deeper_quote_omitted") {
				t.Fatal("deep quote retained")
			}
			if c.name == "preview_article" && (r.Article.State != "not_returned" || r.Article.Reason != "article_body_omitted") {
				t.Fatal("preview is not body")
			}
		})
	}
}
func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestXNewMediaBoundaries(t *testing.T) {
	bad := available("http://127.0.0.1/private")
	m := func() Media { return Media{URL: bad, Variants: available([]MediaVariant{{URL: bad}})} }
	r := newResult(Link{Platform: "x", Kind: "post", ID: "90001"}, "offline")
	r.LinkCard = available(LinkPreview{Image: available(m())})
	r.Media = available([]Media{m()})
	q := newResult(Link{Platform: "x", Kind: "post", ID: "90002"}, "offline")
	q.Author.Avatar = bad
	img := m()
	r.Blocks = []Block{{Type: "article_cover", Media: &img}, {Type: "quote", Quote: &q}}
	(&Fetcher{}).inspectMedia(context.Background(), &r)
	for _, field := range []Field[string]{r.LinkCard.Value.Image.Value.URL, r.LinkCard.Value.Image.Value.Variants.Value[0].URL, r.Media.Value[0].Variants.Value[0].URL, r.Blocks[0].Media.URL, q.Author.Avatar} {
		if field.State != "failed" || field.Reason != "unsafe_target" {
			t.Fatal("unsafe new media slot", field)
		}
	}
}

func TestXShareLinkEntrances(t *testing.T) {
	raw := "https://x.com/example/status/90001?a=share&s=20&unknown=keep"
	want := "https://x.com/example/status/90001?unknown=keep"
	any, err := CleanAny(raw)
	cards, _ := FromCard(`{"meta":{"news":{"jumpUrl":"` + raw + `"}}}`)
	extracted := Extract(raw)
	if err != nil || Clean(raw) != want || any != want || len(cards) != 1 || cards[0].Clean != want || len(extracted) != 1 || extracted[0].Clean != want {
		t.Fatal("sharing parameters survive one entrance", any, cards, extracted, err)
	}
	for _, other := range []string{"https://www.bilibili.com/video/av90001?a=keep", "https://example.com/p?a=keep", "https://x.com/search?a=keep"} {
		got, err := CleanAny(other)
		if err != nil || got != other {
			t.Fatal("neighbor changed", got, err)
		}
	}
}
