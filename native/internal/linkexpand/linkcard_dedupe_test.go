package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

func withCard(r linkreader.Result, url, title string) linkreader.Result {
	r.LinkCard = av(linkreader.LinkPreview{URL: av(url), Title: av(title)})
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "link_card", Text: title})
	return r
}

// 新 3-2 BS-04：外层帖和被引用的帖子带着同一个地址的外链卡，卡片文字写了两遍。按地址去重，留先出现的。
func TestSameLinkCardInQuoteAndPostWrittenOnce(t *testing.T) {
	q := siteResult("bluesky", "post", "90130/b")
	q.Author.Name, q.Author.Account = av("纸云"), av("paper.example.invalid")
	q.Text = av("被引的话")
	q.Blocks = []linkreader.Block{{Type: "text", Text: "被引的话"}}
	q = withCard(q, "https://delve.example.invalid/", "Delvetown")
	r := siteResult("bluesky", "post", "90130/a")
	r.Author.Name, r.Author.Account = av("纸灯"), av("lamp.example.invalid")
	r.Text = av("主帖")
	r.Blocks = []linkreader.Block{{Type: "text", Text: "主帖"}}
	r.Quote = av(&q)
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "quote", Quote: &q})
	r = withCard(r, "https://delve.example.invalid/", "Delvetown")
	if got := describe(t, r); strings.Count(got, "Delvetown") != 1 {
		t.Fatalf("同一张外链卡写了 %d 次：\n%s", strings.Count(got, "Delvetown"), got)
	}
	// 地址不同的两张卡都要留
	r2 := r
	r2.Blocks = append([]linkreader.Block(nil), r.Blocks[:len(r.Blocks)-1]...)
	r2 = withCard(r2, "https://other.example.invalid/", "别处的卡")
	r2.LinkCard = av(linkreader.LinkPreview{URL: av("https://other.example.invalid/"), Title: av("别处的卡")})
	if got := describe(t, r2); !strings.Contains(got, "Delvetown") || !strings.Contains(got, "别处的卡") {
		t.Fatalf("不同地址的卡该各留一张：\n%s", got)
	}
}

// 新 2-3 X-21：被引推文自己还引用了别人时，“还有更深引用，看原文”紧跟被引推文的文字（同一个节点），在它的视频前面、主推文前面；
// 主推文仍指向第 1 个节点。
func TestDeeperQuoteNoteStaysWithQuotedTweet(t *testing.T) {
	q := caseNamed(t, "x-quote").Quote.Value
	cp := *q
	cp.MoreQuotes = av(true)
	cp.Media = av([]linkreader.Media{goldenVideo()})
	cp.Blocks = []linkreader.Block{{Type: "text", Text: "被引的话"}, {Type: "video", Media: &cp.Media.Value[0]}}
	r := caseNamed(t, "x-quote")
	r.Quote = av(&cp)
	for i := range r.Blocks {
		if r.Blocks[i].Type == "quote" {
			r.Blocks[i].Quote = &cp
		}
	}
	d := build(r, Options{ForwardVideo: true})
	nodes := d.Parts[0].Nodes
	if len(nodes) != 3 {
		t.Fatalf("被引文字、被引视频、主推文共 3 个节点：\n%s", strings.Join(d.Describe(), "\n"))
	}
	if !strings.Contains(itemsTextOf(nodes[0]), "还有更深引用，看原文") || strings.Index(itemsTextOf(nodes[0]), "被引的话") > strings.Index(itemsTextOf(nodes[0]), "还有更深引用") {
		t.Fatalf("提示该跟在被引推文文字的同一节点里：%q", itemsTextOf(nodes[0]))
	}
	if strings.Contains(itemsTextOf(nodes[2]), "还有更深引用") || nodes[2].QuoteOf != 1 {
		t.Fatalf("主推文不该带这句，且指向第 1 个节点：%q %d", itemsTextOf(nodes[2]), nodes[2].QuoteOf)
	}
}
