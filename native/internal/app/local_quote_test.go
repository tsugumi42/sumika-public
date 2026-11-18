package app

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/history"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/store"
)

// 引用和分块消息（规格-引用）的后台测试：都走正式写接口，临时数据目录和假模型，不碰真实 data/。

type quoteMsg struct{ Role, Content string }

var workdirRE = regexp.MustCompile("默认工作目录是 [^，]+，")
var stateVerRE = regexp.MustCompile("状态版本：[0-9a-f]+")
var trustPathRE = regexp.MustCompile("工作目录 [^ ]+ 还没有信任") // 项目说明的状态句里带工作目录，两次的临时目录不同

// requestMsgs 把第 n 次（从 0 数）模型请求里的消息按顺序取出来，内容拍平成字符串。
func requestMsgs(t *testing.T, e *testEnv, n int) []quoteMsg {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if n >= len(e.bodies) {
		t.Fatalf("只有 %d 次模型请求，要第 %d 次", len(e.bodies), n)
	}
	var b struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(e.bodies[n]), &b); err != nil {
		t.Fatal(err)
	}
	var out []quoteMsg
	for _, m := range b.Messages {
		var s string
		if json.Unmarshal(m.Content, &s) != nil {
			s = string(m.Content)
		}
		out = append(out, quoteMsg{m.Role, s})
	}
	return out
}

func lastUser(t *testing.T, e *testEnv, n int) string {
	t.Helper()
	m := requestMsgs(t, e, n)
	return m[len(m)-1].Content
}

func bodyCount(e *testEnv) int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.bodies) }

func readAll(t *testing.T, d *Desktop, k api.ConversationKey) []api.MessageView {
	t.Helper()
	page, err := d.Reader.ReadMessages(context.Background(), api.ConversationRequest{Key: k, Limit: 50}, "")
	if err != nil {
		t.Fatal(err)
	}
	return page.Value.Items
}

func refOf(k api.ConversationKey, id string) api.EventRef {
	return api.EventRef{Conversation: k, Event: api.EventKey{Platform: k.Platform, EventID: id}}
}

func quotePart(k api.ConversationKey, m api.MessageView, sel, text string) api.LocalPart {
	return api.LocalPart{Quote: &api.LocalQuote{Ref: refOf(k, m.Ref.Event.EventID), Text: sel}, Text: text}
}

func clockOf(m api.MessageView) string { return time.UnixMilli(m.CreatedAt).Format("15:04") }

func ownerCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE actor_kind='owner' AND kind='message'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const quoteReply = "第一句在这。第二句在那。第三句收尾。"
const quoteHeader1 = "（所有者这条消息分了 1 个部分，按顺序排列。每个【回复 N】只针对它自己引用的那一段，【另外】的话不针对任何引用。）"

// 先聊一轮，拿到她那句回复的事件作被引用的目标。
func quoteFixture(t *testing.T, reply string) (*testEnv, *App, *Desktop, api.ConversationKey, api.MessageView) {
	t.Helper()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text(reply)})
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(context.Background(), key, "先说一段", nil); err != nil {
		t.Fatal(err)
	}
	items := readAll(t, d, key)
	if len(items) != 2 || items[1].ActorKind != "character" {
		t.Fatalf("前置对话不对：%+v", items)
	}
	return e, a, d, key, items[1]
}

func TestLocalQuoteSelectedPart(t *testing.T) {
	t.Parallel()
	e, a, d, key, target := quoteFixture(t, quoteReply)
	sel := "第二句在那"
	start := len([]rune("第一句在这。"))
	p := quotePart(key, target, sel, "这句是什么意思")
	p.Quote.Start, p.Quote.End = start, start+len([]rune(sel))
	if err := d.Reader.SendLocalParts(context.Background(), key, []api.LocalPart{p}, nil); err != nil {
		t.Fatal(err)
	}
	want := quoteHeader1 + "\n【回复 1】引用了 " + target.DisplayName + " 在 " + clockOf(target) + " 说的：「第二句在那」\n所有者对此说：这句是什么意思"
	last := lastUser(t, e, 1)
	if !strings.HasSuffix(last, want) || strings.Count(last, "【回复 1】") != 1 {
		t.Fatalf("给模型的文字不对：%q\n想要结尾 %q", last, want)
	}
	all := requestMsgs(t, e, 1)
	for _, m := range all[:len(all)-1] {
		if strings.Contains(m.Content, "所有者这条消息分了") {
			t.Fatalf("结构化文字出现在历史里：%+v", m)
		}
	}
	owner := readAll(t, d, key)[2]
	if owner.Text != "这句是什么意思" || owner.ReplyTo == nil || owner.ReplyTo.Event.EventID != target.Ref.Event.EventID {
		t.Fatalf("引用关系没存：%+v", owner)
	}
	v := owner.Reply.Value
	if owner.Reply.State != api.Ready || v.Snippet != sel || v.SnippetStart != start || v.SnippetEnd != start+5 || v.SnippetWhole || v.SnippetFallback || v.SnippetTruncated || v.Text != quoteReply {
		t.Fatalf("读回的引用：%+v", owner.Reply)
	}
	if len(owner.Parts) != 1 || !owner.Parts[0].HasQuote || owner.Parts[0].Quote.Value.Snippet != sel || owner.Parts[0].Text != "这句是什么意思" {
		t.Fatalf("读回的块：%+v", owner.Parts)
	}
	// 只靠存档重建历史，和发送当时给模型的一模一样（重启、压缩后重建不会变）
	st, _ := a.set.For("console")
	_, rebuilt, _, err := history.For(st.Events, testChar, key.Conversation, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := rebuilt[len(rebuilt)-2].Text; got != want {
		t.Fatalf("重建的文字 %q 和发送时 %q 不一致", got, want)
	}
}

func TestLocalQuoteWholeEvent(t *testing.T) {
	t.Parallel()
	e, _, d, key, target := quoteFixture(t, quoteReply)
	if err := d.Reader.SendLocalParts(context.Background(), key, []api.LocalPart{quotePart(key, target, "", "整条都看看")}, nil); err != nil {
		t.Fatal(err)
	}
	if last := lastUser(t, e, 1); !strings.HasSuffix(last, "说的：「"+quoteReply+"」\n所有者对此说：整条都看看") {
		t.Fatalf("整条引用：%q", last)
	}
	v := readAll(t, d, key)[2].Reply.Value
	if !v.SnippetWhole || v.Snippet != quoteReply || v.SnippetStart != 0 || v.SnippetEnd != 0 {
		t.Fatalf("整条引用读回：%+v", v)
	}
}

func TestLocalQuoteSearchesWhenOffsetsWrong(t *testing.T) {
	t.Parallel()
	_, _, d, key, target := quoteFixture(t, quoteReply)
	p := quotePart(key, target, "第三句", "找一下")
	p.Quote.Start, p.Quote.End = 0, 3 // 偏移对不上选中的文字：改成在原文里找第一处
	if err := d.Reader.SendLocalParts(context.Background(), key, []api.LocalPart{p}, nil); err != nil {
		t.Fatal(err)
	}
	v := readAll(t, d, key)[2].Reply.Value
	s := len([]rune("第一句在这。第二句在那。"))
	if v.Snippet != "第三句" || v.SnippetStart != s || v.SnippetEnd != s+3 || v.SnippetFallback {
		t.Fatalf("按文字找位置：%+v", v)
	}
}

func TestLocalQuoteSnippetNotInOriginalFallsBack(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("甲乙丙丁戊己庚辛壬癸", 30) // 300 字
	e, _, d, key, target := quoteFixture(t, long)
	if err := d.Reader.SendLocalParts(context.Background(), key, []api.LocalPart{quotePart(key, target, "原文里根本没有这段", "引一段不存在的")}, nil); err != nil {
		t.Fatalf("找不到片段不该拒绝：%v", err)
	}
	v := readAll(t, d, key)[2].Reply.Value
	if !v.SnippetFallback || v.Snippet != string([]rune(long)[:200]) || v.SnippetStart != 0 || v.SnippetEnd != 0 {
		t.Fatalf("退回原文前 200 字：fallback=%v len=%d", v.SnippetFallback, len([]rune(v.Snippet)))
	}
	e.logMu.Lock()
	defer e.logMu.Unlock()
	found := false
	for _, l := range e.logs {
		if strings.Contains(l, "引用：选中的文字不在原文里") {
			found = true
			if strings.Contains(l, "原文里根本没有这段") || strings.Contains(l, "甲乙丙") {
				t.Fatalf("日志里带了正文：%s", l)
			}
		}
	}
	if !found {
		t.Fatalf("没记日志：%v", e.logs)
	}
}

func TestLocalQuoteTooLongIsTruncated(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("长", 1500)
	e, _, d, key, target := quoteFixture(t, long)
	if err := d.Reader.SendLocalParts(context.Background(), key, []api.LocalPart{quotePart(key, target, long, "很长的引用")}, nil); err != nil {
		t.Fatal(err)
	}
	v := readAll(t, d, key)[2].Reply.Value
	if !v.SnippetTruncated || len([]rune(v.Snippet)) != history.QuoteMaxRunes || v.SnippetEnd-v.SnippetStart != history.QuoteMaxRunes {
		t.Fatalf("超长截断：truncated=%v len=%d [%d,%d)", v.SnippetTruncated, len([]rune(v.Snippet)), v.SnippetStart, v.SnippetEnd)
	}
	if last := lastUser(t, e, 1); !strings.Contains(last, "…（已截断）」\n所有者对此说：很长的引用") || strings.Contains(last, strings.Repeat("长", 1001)) {
		t.Fatalf("给模型的超长引用：%d 字", len([]rune(last)))
	}
}

// 两个引用块 + 一段普通文字，各自带附件：存取往返、给模型的文字逐字核对、重建一致。
func TestLocalPartsTwoQuotesAndPlain(t *testing.T) {
	t.Parallel()
	e, a, d, key, reply := quoteFixture(t, quoteReply)
	first := readAll(t, d, key)[0] // 主人先前那句“先说一段”
	parts := []api.LocalPart{
		{Quote: &api.LocalQuote{Ref: refOf(key, reply.Ref.Event.EventID), Text: "第二句在那"}, Text: "这句是什么意思", Attachments: []api.LocalAttachment{{Kind: "image", Path: "D:/files/截图.png", Name: "截图"}}},
		{Quote: &api.LocalQuote{Ref: refOf(key, first.Ref.Event.EventID)}, Text: "这句我是随口说的", Attachments: []api.LocalAttachment{{Kind: "file", Path: "D:/files/笔记.txt"}}},
		{Text: "另外今晚吃什么", Attachments: []api.LocalAttachment{{Kind: "image", Path: "D:/files/菜单.png"}}},
	}
	if err := d.Reader.SendLocalParts(context.Background(), key, parts, nil); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"（所有者这条消息分了 3 个部分，按顺序排列。每个【回复 N】只针对它自己引用的那一段，【另外】的话不针对任何引用。）",
		"【回复 1】引用了 " + reply.DisplayName + " 在 " + clockOf(reply) + " 说的：「第二句在那」",
		"所有者对此说：这句是什么意思 [图片：截图]",
		"【回复 2】引用了 " + first.DisplayName + " 在 " + clockOf(first) + " 说的：「先说一段」",
		"所有者对此说：这句我是随口说的 [文件：D:/files/笔记.txt]",
		"【另外】另外今晚吃什么 [图片]",
	}, "\n")
	if got := lastUser(t, e, 1); !strings.HasSuffix(got, want) {
		t.Fatalf("给模型的文字不对：\n%s\n想要结尾：\n%s", got, want)
	}
	owner := readAll(t, d, key)[2]
	if owner.Text != "这句是什么意思\n这句我是随口说的\n另外今晚吃什么" {
		t.Fatalf("事件文字应是各块文字连起来：%q", owner.Text)
	}
	if owner.ReplyTo == nil || owner.ReplyTo.Event.EventID != reply.Ref.Event.EventID || owner.Reply.Value.Snippet != "第二句在那" {
		t.Fatalf("reply_to 应是第一个引用块：%+v", owner.ReplyTo)
	}
	if len(owner.Parts) != 3 {
		t.Fatalf("块数：%+v", owner.Parts)
	}
	p0, p1, p2 := owner.Parts[0], owner.Parts[1], owner.Parts[2]
	if !p0.HasQuote || p0.Quote.State != api.Ready || p0.Quote.Value.Snippet != "第二句在那" || p0.Quote.Value.Text != quoteReply || p0.Ref.Event.EventID != reply.Ref.Event.EventID ||
		p0.Text != "这句是什么意思" || len(p0.Attachments) != 1 || p0.Attachments[0] != (api.LocalAttachment{Kind: "image", Path: "D:/files/截图.png", Name: "截图"}) {
		t.Fatalf("第 1 块：%+v", p0)
	}
	if !p1.HasQuote || !p1.Quote.Value.SnippetWhole || p1.Quote.Value.Snippet != "先说一段" || p1.Quote.Value.ActorKind != "owner" || p1.Text != "这句我是随口说的" ||
		len(p1.Attachments) != 1 || p1.Attachments[0].Kind != "file" || p1.Attachments[0].Path != "D:/files/笔记.txt" {
		t.Fatalf("第 2 块：%+v", p1)
	}
	if p2.HasQuote || p2.Ref != nil || p2.Text != "另外今晚吃什么" || len(p2.Attachments) != 1 || p2.Attachments[0].Path != "D:/files/菜单.png" {
		t.Fatalf("第 3 块：%+v", p2)
	}
	st, _ := a.set.For("console")
	_, rebuilt, _, err := history.For(st.Events, testChar, key.Conversation, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := rebuilt[len(rebuilt)-2].Text; got != want {
		t.Fatalf("重建的文字和发送时不一致：\n%s", got)
	}
}

// 只有一个没有引用、没有附件的普通块，等价于旧的纯文字输入：原话、没有存档、没有 reply_to。
func TestLocalPartsSinglePlainEqualsOldBehavior(t *testing.T) {
	t.Parallel()
	e, a, d, key, _ := quoteFixture(t, quoteReply)
	ctx := context.Background()
	if err := d.Reader.SendLocalParts(ctx, key, []api.LocalPart{{Text: "普通一句"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := lastUser(t, e, 1); got != "普通一句" {
		t.Fatalf("单个普通块必须是原话：%q", got)
	}
	if _, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "又一句", Mode: "start", ClientKey: "plain-1"}); err != nil {
		t.Fatal(err)
	}
	st, _ := a.set.For("console")
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE reply_to IS NOT NULL OR segments_json LIKE '%local_parts%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("没引用却有 reply_to 或分块存档：%d %v", n, err)
	}
	items := readAll(t, d, key)
	if m := items[2]; m.ReplyTo != nil || len(m.Parts) != 0 || m.Text != "普通一句" {
		t.Fatalf("普通消息读回：%+v", m)
	}
}

// 任何一块不合格，整条都不发（写明第几块）：不写主人的事件、不请求模型。
func TestLocalPartsRejectedWhole(t *testing.T) {
	t.Parallel()
	e, a, d, key, target := quoteFixture(t, quoteReply)
	ctx := context.Background()
	st, _ := a.set.For("console")
	other := localKey(key.Conversation + ":topic:owned")
	if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES ('owned',?,1)`, testChar); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.SendLocal(ctx, other, "另一个话题", nil); err != nil {
		t.Fatal(err)
	}
	otherTarget := readAll(t, d, other)[1]
	hidden, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: key.Conversation, ActorKind: "character", ActorID: testChar, Origin: "automation:dry-run", Kind: "message", Text: "没说出口的话", CreatedAt: 5}, []string{testChar})
	if err != nil {
		t.Fatal(err)
	}
	var hiddenID, noticeID string
	if err := st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, hidden).Scan(&hiddenID); err != nil {
		t.Fatal(err)
	}
	notice, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: key.Conversation, ActorKind: "owner", ActorID: "owner", Kind: "notice", Text: "通知", CreatedAt: 6}, []string{testChar})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, notice).Scan(&noticeID); err != nil {
		t.Fatal(err)
	}
	good := quotePart(key, target, "第二句", "这块没问题")
	badQuote := func(q api.LocalQuote) api.LocalPart { return api.LocalPart{Quote: &q, Text: "这块有问题"} }
	img := func(a api.LocalAttachment) api.LocalPart {
		return api.LocalPart{Text: "带附件", Attachments: []api.LocalAttachment{a}}
	}
	fakeHash := strings.Repeat("a", 64)
	cases := []struct {
		name string
		bad  api.LocalPart
	}{
		{"别的会话的事件（引用里写别处）", badQuote(api.LocalQuote{Ref: refOf(other, otherTarget.Ref.Event.EventID), Text: "x"})},
		{"别的会话的事件（引用里写本会话）", badQuote(api.LocalQuote{Ref: refOf(key, otherTarget.Ref.Event.EventID)})},
		{"她没说出口的事件", badQuote(api.LocalQuote{Ref: refOf(key, hiddenID)})},
		{"不是聊天消息的事件", badQuote(api.LocalQuote{Ref: refOf(key, noticeID)})},
		{"不存在的事件", badQuote(api.LocalQuote{Ref: refOf(key, "invented-id")})},
		{"空 id", badQuote(api.LocalQuote{Ref: refOf(key, "")})},
		{"平台对不上", badQuote(api.LocalQuote{Ref: api.EventRef{Conversation: key, Event: api.EventKey{Platform: "qq", EventID: target.Ref.Event.EventID}}})},
		{"空块", api.LocalPart{}},
		{"附件种类不对", img(api.LocalAttachment{Kind: "video", Path: "D:/x"})},
		{"附件没有来源", img(api.LocalAttachment{Kind: "image"})},
		{"附件凭据和路径都给", img(api.LocalAttachment{Kind: "image", Path: "D:/x", Media: &api.MediaRef{Source: refOf(key, target.Ref.Event.EventID), Hash: fakeHash}})},
		{"附件媒体凭据编的", img(api.LocalAttachment{Kind: "image", Media: &api.MediaRef{Source: refOf(key, target.Ref.Event.EventID), Hash: fakeHash}})},
		{"附件媒体凭据指别的会话", img(api.LocalAttachment{Kind: "image", Media: &api.MediaRef{Source: refOf(other, otherTarget.Ref.Event.EventID), Hash: fakeHash}})},
	}
	before, calls := ownerCount(t, st), bodyCount(e)
	for i, c := range cases {
		parts := []api.LocalPart{good, c.bad, {Text: "后面的话"}}
		err := d.Reader.SendLocalParts(ctx, key, parts, nil)
		if err == nil {
			t.Errorf("%s：放行了", c.name)
		} else if !strings.Contains(err.Error(), "第 2 块") {
			t.Errorf("%s：错误没写明是第几块：%v", c.name, err)
		}
		if i == 4 && !errors.Is(err, desktopread.ErrQuoteTarget) {
			t.Errorf("不存在的事件应报引用目标错误：%v", err)
		}
		if _, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Mode: "start", ClientKey: "rej-" + string(rune('a'+i)), Parts: parts}); err == nil {
			t.Errorf("%s：SubmitLocal 放行了", c.name)
		}
	}
	// 块太多、Text 和 Parts 同时给、没有块
	many := make([]api.LocalPart, 21)
	for i := range many {
		many[i] = api.LocalPart{Text: "x"}
	}
	if err := d.Reader.SendLocalParts(ctx, key, many, nil); err == nil {
		t.Error("21 块放行了")
	}
	if _, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "字", Parts: []api.LocalPart{{Text: "块"}}, Mode: "start", ClientKey: "both"}); err == nil {
		t.Error("Text 和 Parts 同时给放行了")
	}
	if err := d.Reader.SendLocalParts(ctx, key, nil, nil); err == nil {
		t.Error("没有块放行了")
	}
	if n, b := ownerCount(t, st), bodyCount(e); n != before || b != calls {
		t.Fatalf("被拒的输入还是写了事件或请求了模型：主人事件 %d→%d，请求 %d→%d", before, n, calls, b)
	}
	qqKey := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:1:2"}
	if err := d.Reader.SendLocalParts(ctx, qqKey, []api.LocalPart{good}, nil); err == nil {
		t.Fatal("QQ 会话放行了")
	}
}

// SubmitLocal 入口：同样存档、同样给模型；同键同输入重试幂等，换了块算冲突。
func TestLocalPartsSubmitLocal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text(quoteReply)})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "先说一段", nil); err != nil {
		t.Fatal(err)
	}
	target := readAll(t, d, key)[1]
	submit := func(sel string) (api.InputReceipt, error) {
		return d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Mode: "start", ClientKey: "quote-1",
			Parts: []api.LocalPart{quotePart(key, target, sel, "回应一下这句"), {Text: "顺便说一句"}}})
	}
	rec, err := submit("第三句收尾")
	if err != nil {
		t.Fatal(err)
	}
	watch, err := d.Reader.WatchLocalRun(ctx, key, rec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	for !localRecv(t, watch.Updates()).Value.Drained {
	}
	want := strings.Join([]string{
		"（所有者这条消息分了 2 个部分，按顺序排列。每个【回复 N】只针对它自己引用的那一段，【另外】的话不针对任何引用。）",
		"【回复 1】引用了 " + target.DisplayName + " 在 " + clockOf(target) + " 说的：「第三句收尾」",
		"所有者对此说：回应一下这句",
		"【另外】顺便说一句",
	}, "\n")
	if got := lastUser(t, e, 1); !strings.HasSuffix(got, want) {
		t.Fatalf("SubmitLocal 给模型的文字：%q", got)
	}
	owner := readAll(t, d, key)[2]
	if owner.Reply.Value.Snippet != "第三句收尾" || owner.Text != "回应一下这句\n顺便说一句" || len(owner.Parts) != 2 || owner.Parts[1].HasQuote {
		t.Fatalf("SubmitLocal 读回：%+v", owner)
	}
	if _, err := submit("第三句收尾"); err != nil {
		t.Fatalf("同键同输入重试：%v", err)
	}
	if _, err := submit("第二句在那"); err == nil {
		t.Fatal("同键换了引用应冲突")
	}
	if n := len(readAll(t, d, key)); n != 4 {
		t.Fatalf("重试后消息数 %d，想要 4", n)
	}
}

// 缓存只追加：同一段对话，第二句带分块和不带，发给模型的请求历史前缀逐字相同，只有最后一条新消息不同。
func TestLocalPartsOnlyAffectTheNewMessage(t *testing.T) {
	t.Parallel()
	run := func(withParts bool) ([]quoteMsg, api.MessageView) {
		e, _, d, key, target := quoteFixture(t, quoteReply)
		parts := []api.LocalPart{{Text: "新的一句"}}
		if withParts {
			parts = []api.LocalPart{quotePart(key, target, "第二句在那", "新的一句"), {Text: "另外一句"}}
		}
		if err := d.Reader.SendLocalParts(context.Background(), key, parts, nil); err != nil {
			t.Fatal(err)
		}
		msgs := requestMsgs(t, e, 1)
		for i := range msgs { // 两次的临时工作目录和由它算出的状态版本不同，和引用无关
			msgs[i].Content = trustPathRE.ReplaceAllString(stateVerRE.ReplaceAllString(workdirRE.ReplaceAllString(msgs[i].Content, "默认工作目录是 X，"), "状态版本：X"), "工作目录 X 还没有信任")
		}
		return msgs, target
	}
	plain, _ := run(false)
	quoted, target := run(true)
	if len(plain) != len(quoted) {
		t.Fatalf("消息条数变了：%d → %d", len(plain), len(quoted))
	}
	for i := 0; i < len(plain)-1; i++ {
		if plain[i] != quoted[i] {
			t.Fatalf("历史前缀第 %d 条被改动：\n%+v\n%+v", i, plain[i], quoted[i])
		}
	}
	if plain[len(plain)-1].Content != "新的一句" || !strings.Contains(quoted[len(quoted)-1].Content, "引用了 "+target.DisplayName) {
		t.Fatalf("最后一条：%q / %q", plain[len(plain)-1].Content, quoted[len(quoted)-1].Content)
	}
}

// 自检话题里的分块消息照样打 selftest 标记，同时保留分块存档和 reply_to（和 feat/ctl-chat 的标记合在同一条存档里）。
func TestLocalPartsInSelftestTopicKeepTagAndParts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text(quoteReply)})
	ctx := context.Background()
	st, _ := a.set.For("console")
	topic := "selftest-" + testChar
	if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES (?,?,1)`, topic, testChar); err != nil {
		t.Fatal(err)
	}
	key := localKey("console:" + testChar + ":topic:" + topic)
	if err := d.Reader.SendLocal(ctx, key, "自检先说一句", nil); err != nil {
		t.Fatal(err)
	}
	target := readAll(t, d, key)[1]
	if err := d.Reader.SendLocalParts(ctx, key, []api.LocalPart{quotePart(key, target, "第二句在那", "自检回复"), {Text: "另外"}}, nil); err != nil {
		t.Fatal(err)
	}
	var seg string
	var replyTo *string
	if err := st.Events.QueryRow(`SELECT segments_json, reply_to FROM events WHERE actor_kind='owner' AND conversation=? ORDER BY seq DESC LIMIT 1`, key.Conversation).Scan(&seg, &replyTo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seg, `"tags":["selftest"]`) || !strings.Contains(seg, `"local_parts"`) || replyTo == nil || *replyTo != target.Ref.Event.EventID {
		t.Fatalf("自检话题的分块消息：%s reply_to=%v", seg, replyTo)
	}
	if owner := readAll(t, d, key)[2]; len(owner.Parts) != 2 || owner.Parts[0].Quote.Value.Snippet != "第二句在那" {
		t.Fatalf("读回：%+v", owner.Parts)
	}
}
