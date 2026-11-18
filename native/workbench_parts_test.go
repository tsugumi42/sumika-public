package main

import (
	"fmt"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/segview"
)

func partsView(key api.ConversationKey) api.MessageView {
	ref := func(id string) *api.EventRef {
		return &api.EventRef{Conversation: key, Event: api.EventKey{Platform: key.Platform, EventID: id}}
	}
	ready := func(q api.QuoteView) api.Result[api.QuoteView] {
		return api.Result[api.QuoteView]{State: api.Ready, Value: q}
	}
	return api.MessageView{
		Ref: *ref("me1"), Origin: "owner", ActorKind: "owner", Text: "回复一\n回复二\n另外",
		ReplyTo: ref("t1"), Reply: ready(api.QuoteView{Text: "第一句在这。第二句在那。"}),
		Parts: []api.MessagePart{
			{HasQuote: true, Ref: ref("t1"), Quote: ready(api.QuoteView{DisplayName: " 编造角色 ", Text: "第一句在这。第二句在那。", Snippet: "第二句在那", SnippetStart: 6, SnippetEnd: 11}),
				Text: "回复一", Attachments: []api.LocalAttachment{{Kind: "image", Path: "D:/库/abc.png"}, {Kind: "file", Path: "D:/资料/笔记.txt", Name: "笔记.txt"}}},
			{HasQuote: true, Ref: ref("gone"), Quote: api.Result[api.QuoteView]{State: api.Unsupported}, Text: "回复二"},
			{Text: "另外"},
		},
	}
}

func TestChatPartsConvertsReadBackBlocks(t *testing.T) {
	t.Parallel()
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}
	got := chatParts(partsView(key))
	if len(got) != 3 {
		t.Fatalf("块数不对：%+v", got)
	}
	q := got[0].Quote
	if q == nil || q.Gone || q.Target != "t1" || q.Name != "编造角色" || q.Snippet != "第二句在那" || q.Original != "第一句在这。第二句在那。" || q.Start != 6 || q.End != 11 {
		t.Fatalf("第一块的出处不对：%+v", q)
	}
	if len(got[0].Atts) != 2 || got[0].Atts[0] != (chatui.PartAtt{Name: "abc.png", Image: true}) || got[0].Atts[1] != (chatui.PartAtt{Name: "笔记.txt"}) {
		t.Fatalf("附件不对：%+v", got[0].Atts)
	}
	if g := got[1].Quote; g == nil || !g.Gone || g.Target != "gone" || got[1].Text != "回复二" {
		t.Fatalf("看不到的引用该标 Gone 并留回复文字：%+v", got[1])
	}
	if got[2].Quote != nil || got[2].Text != "另外" {
		t.Fatalf("普通块不对：%+v", got[2])
	}
	if chatParts(api.MessageView{Text: "普通消息"}) != nil {
		t.Fatal("没有分块的消息该返回 nil")
	}
	whole := partsView(key)
	whole.Parts = whole.Parts[:1]
	v := whole.Parts[0].Quote.Value
	v.Snippet, v.SnippetFallback, v.SnippetWhole, v.SnippetStart, v.SnippetEnd = "", true, true, 0, 0
	whole.Parts[0].Quote.Value = v
	if q := chatParts(whole)[0].Quote; q.Snippet != v.Text || !q.Fallback || !q.Whole {
		t.Fatalf("后台没给片段时该退回原文：%+v", q)
	}
}

func TestMsgFromViewShowsPartsInsteadOfTheFirstQuoteAndJoinedText(t *testing.T) {
	t.Parallel()
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}
	m := msgFromView(key, partsView(key), func(string) (character, bool) { return character{}, false })
	v := chatMessage(m)
	if len(v.Parts) != 3 || v.Quote != nil || len(v.Blocks) != 0 {
		t.Fatalf("分块消息该按块画，不再画第一个引用和拼起来的正文：parts %d quote %v blocks %d", len(v.Parts), v.Quote, len(v.Blocks))
	}
	if m.text != "回复一\n回复二\n另外" || m.quote == nil {
		t.Fatalf("旧界面用的字段该原样保留：%q %v", m.text, m.quote)
	}
}

func partsRigWithHistory(t *testing.T, f *fakeInputs, quote chatui.Part) *wbRig {
	t.Helper()
	r := newWbRig(t, f)
	const text = "第一句在这。第二句在那。"
	r.a.msgs = []*msg{{id: "t1", who: "编造角色", isChar: true, actor: "char00", at: 1000, text: text, blocks: segview.TextBlocks(text)}}
	for i := range 60 {
		id := fmt.Sprintf("f%d", i)
		r.a.msgs = append(r.a.msgs, &msg{id: id, who: "编造角色", isChar: true, actor: "char00", at: int64(2000 + i), text: "中间的话 " + id, blocks: segview.TextBlocks("中间的话 " + id)})
	}
	last := &msg{id: "me1", mine: true, who: "我", actor: "owner", at: 9000, text: "回复", chatParts: []chatui.Part{quote}}
	r.a.msgs = append(r.a.msgs, last)
	r.a.list.ScrollToEnd()
	r.settle()
	return r
}

func TestWorkbenchClickQuoteScrollsBackAndHighlights(t *testing.T) {
	f := &fakeInputs{}
	r := partsRigWithHistory(t, f, chatui.Part{
		Quote: &chatui.PartQuote{Target: "t1", Name: "编造角色", Snippet: "第二句在那", Original: "第一句在这。第二句在那。", Start: 6, End: 11},
		Text:  "这句什么意思",
	})
	if first, _ := r.a.list.Visible(); first == 0 {
		t.Fatal("前提：被引的那条该在屏幕外")
	}
	if err := r.tt.Click("引用 编造角色：点一下回到原消息"); err != nil {
		t.Fatalf("点引用块：%v；%v", err, r.tt.Texts())
	}
	r.settle()
	first, last := r.a.list.Visible()
	if first > 0 || last < 0 {
		t.Fatalf("点引用该滚回原消息：可见 %d..%d", first, last)
	}
	hl := r.a.quoteHL
	if hl.MessageID != "t1" || hl.Text != "第二句在那" || hl.Start != 6 || hl.End != 11 || hl.Whole {
		t.Fatalf("该高亮被引的那一段：%+v", hl)
	}
}

func TestWorkbenchClickQuoteDoesNotHighlightFallbackAndSaysWhenOriginalIsGone(t *testing.T) {
	f := &fakeInputs{}
	r := partsRigWithHistory(t, f, chatui.Part{
		Quote: &chatui.PartQuote{Target: "t1", Name: "编造角色", Snippet: "第一句在这。", Original: "第一句在这。第二句在那。", Fallback: true},
		Text:  "回复",
	})
	if err := r.tt.Click("引用 编造角色：点一下回到原消息"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	if first, _ := r.a.list.Visible(); first > 0 {
		t.Fatal("退回开头的引用也该能滚回原消息")
	}
	if r.a.quoteHL != (chatui.Highlight{}) {
		t.Fatalf("退回开头的引用不高亮：%+v", r.a.quoteHL)
	}

	r2 := partsRigWithHistory(t, f, chatui.Part{Quote: &chatui.PartQuote{Target: "不在这一页的消息", Name: "编造角色", Snippet: "x"}, Text: "回复"})
	if err := r2.tt.Click("引用 编造角色：点一下回到原消息"); err != nil {
		t.Fatal(err)
	}
	r2.settle()
	if r2.a.status != "原消息不在已有的记录里" {
		t.Fatalf("原消息找不到时该说一声：%q", r2.a.status)
	}
}
