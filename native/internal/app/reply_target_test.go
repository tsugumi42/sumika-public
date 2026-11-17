package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/store"
)

// 收到链接 → 解析 → 发出 → 回声：发出去的那条事件的 reply_to 必须等于原消息的事件 id，
// 界面读到的 ReplyTo、Reply 都有内容（以前 reply_to 是空的，引用块显示“原消息不在本地”）
//
// 两种形状都要连上（规格-链接解析模板第十三节）：短帖直发的第一条消息带回复段（线上引用源消息）；
// 整条进合并转发时 QQ 的转发带不了回复段（规格-链接发送改版 A5，线上不引用、靠源消息上的进度表情），入库仍按计划的源事件连上。
// bug（合上 B4 后）：转发前的导语没了，出口只从带引用的消息段找 reply_to，转发打头的展开入库时 reply_to 为空
func TestLinkReplyEventConnectsToSourceMessage(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		fetcher linkreader.ContentFetcher
		action  string
	}{{"转发打头", nil, "send_group_forward_msg"}, {"直发", onePic(), "send_group_msg"}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			linkReplyConnects(t, c.fetcher, c.action)
		})
	}
}

func linkReplyConnects(t *testing.T, fetcher linkreader.ContentFetcher, action string) {
	e, a, wire, _ := newExpandHost(t, "off", fetcher)
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "971", testOwner, "https://x.com/paper/status/75001"))
	waitFor(t, "结果发出", func() bool { return len(wire.snapshot()) >= 3 })
	sent := ""
	for _, f := range wire.snapshot() {
		var fi frameInfo
		_ = json.Unmarshal([]byte(f), &fi)
		if fi.Action == "send_group_msg" || fi.Action == "send_group_forward_msg" {
			sent = f
			break
		}
	}
	if !strings.Contains(sent, `"action":"`+action+`"`) {
		t.Fatalf("第一条结果应是 %s：%s", action, sent)
	}
	if quoted := strings.Contains(sent, `"type":"reply"`); quoted != (action == "send_group_msg") {
		t.Fatalf("直发的第一条带回复段、合并转发不带（QQ 转发带不了）：带了=%v %s", quoted, sent)
	}
	conv := "qq:group:" + testGroup
	st, err := a.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	src, ok, err := st.EventByPlatformID("qq", conv, "971")
	if err != nil || !ok {
		t.Fatalf("原消息没入库：%v %v", ok, err)
	}
	var replyTo sql.NullString
	var origin string
	waitFor(t, "发出的事件入库", func() bool {
		err := st.Events.QueryRow(`SELECT COALESCE(reply_to,''), origin FROM events WHERE conversation=? AND actor_kind='character' AND kind='message' ORDER BY seq DESC LIMIT 1`, conv).Scan(&replyTo, &origin)
		return err == nil
	})
	if origin != "automation:link-reader" || replyTo.String != src.ID {
		t.Fatalf("发出的事件 origin=%q reply_to=%q，应为原消息的事件 id %q", origin, replyTo.String, src.ID)
	}
	// 界面读的那一层（store.ReadDisplay → desktopread.project）：发出的行带着 ReplyTo，被引用那条的发言人和文字也读到了
	page, err := st.ReadDisplay(context.Background(), store.DisplayScope{Platform: "qq", Conversation: conv, Character: testChar, Characters: []string{testChar}}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range page.Items {
		if r.Origin == "automation:link-reader" {
			found = true
			if r.ReplyTo != src.ID || r.Reply == nil || strings.TrimSpace(r.Reply.Text) == "" {
				t.Fatalf("界面读到的引用没连上：ReplyTo=%q Reply=%+v", r.ReplyTo, r.Reply)
			}
		}
	}
	if !found {
		t.Fatal("读不到发出的链接消息")
	}
}

// textQuoteFetcher 回一条纯文字推文，引用另一条纯文字推文（两边都没有图）
type textQuoteFetcher struct{ picsFetcher }

func (f textQuoteFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	r, err := f.picsFetcher.Fetch(ctx, l)
	q, _ := f.picsFetcher.Fetch(ctx, linkreader.Link{ID: "99", Clean: "https://x.com/q/status/99"})
	q.Text = linkreader.Field[string]{State: "available", Value: "被引的话"}
	r.Quote = linkreader.Field[*linkreader.Result]{State: "available", Value: &q}
	r.Blocks = []linkreader.Block{{Type: "text", Text: "编的推文正文"}, {Type: "quote", Quote: &q}}
	return r, err
}

// 纯文字推文引用纯文字推文（默认 forwardQuoteReply 开、SnowLuma 本地拒转发节点里的引用）：
// 转发里没有媒体也要去掉引用重发一次，只发一帧不带回复段的合并转发，贴完成表情，入库连上源消息。
// bug：failedPart 先判“纯文字没有可换的形式”，到不了去引用重发，一帧都没发出去，最后贴失败表情
func TestTextQuoteTweetForwardDropsReply(t *testing.T) {
	t.Parallel()
	e, a, wire, _ := newExpandHost(t, "off", textQuoteFetcher{pics()}, "    doneEmoji: \"124\"\n    failEmoji: \"9\"\n")
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "981", testOwner, "https://x.com/paper/status/76001"))
	waitFor(t, "完成或失败表情", func() bool {
		trail, _ := reactionTrail(wire.snapshot())
		return len(trail) > 0 && (trail[len(trail)-1] == "贴 124" || trail[len(trail)-1] == "贴 9")
	})
	trail, _ := reactionTrail(wire.snapshot())
	if trail[len(trail)-1] != "贴 124" {
		t.Fatalf("应贴完成表情：%v", trail)
	}
	var sent []string
	for _, f := range wire.snapshot() {
		var fi frameInfo
		_ = json.Unmarshal([]byte(f), &fi)
		if fi.Action == "send_group_msg" || fi.Action == "send_group_forward_msg" {
			sent = append(sent, f)
		}
	}
	if len(sent) != 1 || !strings.Contains(sent[0], `"action":"send_group_forward_msg"`) || strings.Contains(sent[0], `"type":"reply"`) {
		t.Fatalf("只发一帧不带回复段的合并转发：%v", sent)
	}
	logged := false
	for _, l := range e.logSnapshot() {
		logged = logged || strings.Contains(l, "合并转发里的引用被拒，去掉引用重发")
	}
	if !logged {
		t.Fatalf("日志里要记去掉引用重发：%v", e.logSnapshot())
	}
	conv := "qq:group:" + testGroup
	st, err := a.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	src, ok, err := st.EventByPlatformID("qq", conv, "981")
	if err != nil || !ok {
		t.Fatalf("原消息没入库：%v %v", ok, err)
	}
	var replyTo sql.NullString
	waitFor(t, "发出的事件入库", func() bool {
		err := st.Events.QueryRow(`SELECT COALESCE(reply_to,'') FROM events WHERE conversation=? AND actor_kind='character' AND kind='message' AND origin='automation:link-reader' ORDER BY seq DESC LIMIT 1`, conv).Scan(&replyTo)
		return err == nil
	})
	if replyTo.String != src.ID {
		t.Fatalf("发出的事件 reply_to=%q，应为原消息的事件 id %q", replyTo.String, src.ID)
	}
}
