package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func syndOnly(t *testing.T, body string) Result {
	t.Helper()
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "cdn.syndication.twimg.com" {
			return response(200, body), nil
		}
		return response(503, ""), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// display_text_range 按 UTF-16 单位算：正文里每个 4 字节 emoji 多占 1 个单位。按码点截会在末尾多留 t.co 的开头（“ h”）。
func TestXSyndicationRangeCountsUTF16(t *testing.T) {
	body := `{"__typename":"Tweet","id_str":"90001","text":"编的衣服🥹🥹 https://t.co/aaaa","display_text_range":[0,8],` +
		`"entities":{"media":[{"url":"https://t.co/aaaa","indices":[7,30]}]},"user":{"name":"纸云","screen_name":"paper_cloud"},` +
		`"quoted_tweet":{"__typename":"Tweet","id_str":"90002","text":"复活🫣\n编的魔法衣服🗡️ https://t.co/bbbb","display_text_range":[0,14],` +
		`"entities":{"media":[{"url":"https://t.co/bbbb","indices":[13,36]}]},"user":{"name":"纸灯","screen_name":"paper_lamp"},"mediaDetails":[]}}`
	r := syndOnly(t, body)
	if r.Text.Value != "编的衣服🥹🥹" {
		t.Fatalf("主推正文：%q", r.Text.Value)
	}
	q := r.Quote.Value
	if r.Quote.State != "available" || q.Text.Value != "复活🫣\n编的魔法衣服🗡️" {
		t.Fatalf("被引推文正文：%+v", r.Quote)
	}
	// 被引推文的链接带作者账号，不写 /i/status/
	if q.CleanURL != "https://x.com/paper_lamp/status/90002" {
		t.Fatalf("被引推文链接：%s", q.CleanURL)
	}
	// 只有 emoji 没有媒体：range 末尾不留空格
	r = syndOnly(t, `{"__typename":"Tweet","id_str":"90001","text":"编的 👻 https://t.co/cccc","display_text_range":[0,6],"user":{"screen_name":"paper_cloud"}}`)
	if r.Text.Value != "编的 👻" {
		t.Fatalf("末尾空白：%q", r.Text.Value)
	}
}

// 嵌入接口里的长文只有标题、封面和开头：出标题和开头，长文记成没取全，不把 article 链接当标题。
func TestXSyndicationArticle(t *testing.T) {
	body := `{"__typename":"Tweet","id_str":"90001","text":"https://t.co/dddd","display_text_range":[0,17],` +
		`"entities":{"urls":[{"url":"https://t.co/dddd","expanded_url":"https://x.com/i/article/90009"}]},"user":{"screen_name":"paper_cloud"},` +
		`"article":{"rest_id":"90009","title":"编的长文标题","preview_text":"编的长文开头一段","cover_media":{"media_info":{"__typename":"ApiImage","original_img_url":"https://pbs.twimg.com/media/c.jpg","original_img_width":400,"original_img_height":200}}}}`
	r := syndOnly(t, body)
	if r.Title.Value != "编的长文标题" || r.Article.State != "available" || !r.Article.Truncated || r.Article.Value != "编的长文开头一段" {
		t.Fatalf("长文：标题 %+v 正文 %+v", r.Title, r.Article)
	}
	var kinds []string
	for _, b := range r.Blocks {
		kinds = append(kinds, b.Type)
	}
	if got := strings.Join(kinds, ","); got != "text,article_title,article_cover,text" {
		t.Fatalf("块：%s", got)
	}
}

// syndication 只在 fx、vx 都失败时取：哪怕 fx、vx 近期成功率比它低，它也不进并发的那一轮。
func TestXSyndicationNeverRaces(t *testing.T) {
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(503, ""), nil })
	for range 10 {
		f.xStats.record(RouteFX, false)
		f.xStats.record(RouteVX, false)
		f.xStats.record(RouteSyndication, true)
	}
	routes := f.xRoutes()
	if len(routes) != 3 || routes[2].name != RouteSyndication {
		t.Fatalf("线路顺序：%v %v %v", routes[0].name, routes[1].name, routes[2].name)
	}
}

// 嵌入接口的长文字段坏了：推文照常出、长文记成没取到，不让整条 syndication 报 invalid_response
// （fx、vx 都挂时它是最后一条线路）。
func TestXSyndicationBadArticleKeepsTweet(t *testing.T) {
	body := `{"__typename":"Tweet","id_str":"90001","text":"编的推文正文","display_text_range":[0,6],"user":{"name":"纸云","screen_name":"paper_cloud"},` +
		`"article":"不是对象"}`
	r := syndOnly(t, body)
	if r.Text.Value != "编的推文正文" || r.Article.State == "available" || r.Article.Reason != "article_invalid_response" {
		t.Fatalf("正文 %+v 长文 %+v", r.Text, r.Article)
	}
	for _, b := range r.Blocks {
		if b.Type == "article_title" || b.Type == "article_cover" {
			t.Fatalf("解析失败时不该留下长文的块：%+v", r.Blocks)
		}
	}
}

// 长文的标题、封面、头一段都解析好了，后面的块坏了：退回解析前的样子，不留半截长文（标题、封面块、封面图）。
func TestXSyndicationArticleFailsMidwayRestores(t *testing.T) {
	body := `{"__typename":"Tweet","id_str":"90001","text":"编的推文正文","display_text_range":[0,6],"user":{"name":"纸云","screen_name":"paper_cloud"},` +
		`"article":{"rest_id":"90009","title":"编的长文标题","cover_media":{"media_info":{"__typename":"ApiImage","original_img_url":"https://pbs.twimg.com/media/c.jpg","original_img_width":400,"original_img_height":200}},` +
		`"content":{"blocks":[{"type":"unstyled","text":"第一段"},{"type":"unstyled"}]}}}`
	r := syndOnly(t, body)
	if r.Text.Value != "编的推文正文" || r.Title.Value == "编的长文标题" || r.Article.Reason != "article_invalid_response" {
		t.Fatalf("正文 %+v 标题 %+v 长文 %+v", r.Text, r.Title, r.Article)
	}
	for _, b := range r.Blocks {
		if b.Type == "article_title" || b.Type == "article_cover" || b.Text == "第一段" {
			t.Fatalf("留下了半截长文：%+v", r.Blocks)
		}
	}
	for _, m := range r.Media.Value {
		if m.URL.Value == "https://pbs.twimg.com/media/c.jpg" {
			t.Fatalf("封面图留在媒体里：%+v", r.Media.Value)
		}
	}
}
