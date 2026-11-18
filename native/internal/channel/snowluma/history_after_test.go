package snowluma

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// HistoryAfter（ADR-333）：群里带锚点、reverse_order=false 向服务器取“锚点及更新的”；私聊取最新一页。
// 号、群、消息号都是编的。

func afterReply(frames ...string) string {
	return `{"status":"ok","retcode":0,"data":{"messages":[` + strings.Join(frames, ",") + `]}}`
}

func afterHistoryActions(e *sendEnv) []sentAction {
	var out []sentAction
	for _, a := range e.peer.rawActions() {
		if strings.HasSuffix(a.Action, "_msg_history") {
			out = append(out, a)
		}
	}
	return out
}

func TestHistoryAfterGroupAsksForwardFromAnchor(t *testing.T) {
	sec := int64(1761643200)
	e := newSendEnv(t, func(act sentAction) (string, bool) {
		if act.Action != "get_group_msg_history" {
			return "", false
		}
		return afterReply(historyFrame(groupConv(), "7001", sec, false), historyFrame(groupConv(), "7002", sec+1, true)), true
	})
	page, err := e.ad.HistoryAfter(context.Background(), sendAccount, groupConv(), "7001")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || !page.ReachedLatest {
		t.Fatalf("两条、最后一页没满该算翻到了尽头：%+v", page)
	}
	acts := afterHistoryActions(e)
	if len(acts) != 1 || string(acts[0].Params["message_id"]) != "7001" || string(acts[0].Params["reverse_order"]) != "false" {
		t.Fatalf("应带锚点、reverse_order=false（取更新的）：%+v", acts)
	}
	if len(e.peer.actions()) != 0 {
		t.Fatalf("查询不能发出任何消息动作：%+v", e.peer.actions())
	}
}

func TestHistoryAfterGroupRefusesWhenAnchorMissing(t *testing.T) {
	sec := int64(1761643200)
	for name, reply := range map[string]string{
		"空页":     afterReply(),
		"页里没有锚点": afterReply(historyFrame(groupConv(), "7009", sec, true)),
	} {
		t.Run(name, func(t *testing.T) {
			e := newSendEnv(t, func(act sentAction) (string, bool) { return reply, act.Action == "get_group_msg_history" })
			if _, err := e.ad.HistoryAfter(context.Background(), sendAccount, groupConv(), "7001"); err == nil {
				t.Fatal("锚点没回来，什么都证明不了，应返回错误")
			}
		})
	}
}

func TestHistoryAfterGroupRejectsBadAnchorWithoutSending(t *testing.T) {
	e := newSendEnv(t, func(sentAction) (string, bool) { return "", false })
	for _, anchor := range []string{"", "0", "abc", "1.5"} {
		if _, err := e.ad.HistoryAfter(context.Background(), sendAccount, groupConv(), anchor); err == nil {
			t.Fatalf("锚点 %q 应被拒绝", anchor)
		}
	}
	if len(afterHistoryActions(e)) != 0 {
		t.Fatal("坏锚点不该向协议端发请求")
	}
}

func TestHistoryAfterPrivateNeedsSourceInLatestPage(t *testing.T) {
	sec := int64(1761643200)
	e := newSendEnv(t, func(act sentAction) (string, bool) {
		if act.Action != "get_friend_msg_history" {
			return "", false
		}
		return afterReply(historyFrame(privateConv(), "7001", sec, false), historyFrame(privateConv(), "7002", sec+1, true)), true
	})
	page, err := e.ad.HistoryAfter(context.Background(), sendAccount, privateConv(), "7001")
	if err != nil || len(page.Messages) != 2 || !page.ReachedLatest {
		t.Fatalf("私聊：%+v %v", page, err)
	}
	if _, has := afterHistoryActions(e)[0].Params["message_id"]; has {
		t.Fatal("私聊不支持锚点翻页，不能带 message_id")
	}
	e.clk.Advance(queryGap)
	if _, err := e.ad.HistoryAfter(context.Background(), sendAccount, privateConv(), "7777"); err == nil {
		t.Fatal("最新一页里没有源消息，不能当成翻到了尽头")
	}
}

// 满一页就以最新的一条为新锚点接着翻；每页有新消息才继续，总页数有上限
func TestHistoryAfterPagesUntilShortPage(t *testing.T) {
	sec := int64(1761643200)
	calls := 0
	e := newSendEnv(t, func(act sentAction) (string, bool) {
		if act.Action != "get_group_msg_history" {
			return "", false
		}
		calls++
		var frames []string
		if calls == 1 {
			for i := range afterPageCount {
				frames = append(frames, historyFrame(groupConv(), strconv.Itoa(7001+i), sec+int64(i), i%2 == 0))
			}
		} else {
			frames = append(frames, historyFrame(groupConv(), strconv.Itoa(7001+afterPageCount-1), sec+int64(afterPageCount-1), false), historyFrame(groupConv(), "9001", sec+500, true))
		}
		return afterReply(frames...), true
	})
	type result struct {
		page types.AfterPage
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := e.ad.HistoryAfter(context.Background(), sendAccount, groupConv(), "7001")
		done <- result{page, err}
	}()
	var r result
	for waiting := true; waiting; {
		select {
		case r = <-done:
			waiting = false
		case <-time.After(5 * time.Millisecond):
			e.clk.Advance(queryGap) // 页间隔走的是假时钟
		}
	}
	page, err := r.page, r.err
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !page.ReachedLatest || len(page.Messages) != afterPageCount+1 {
		t.Fatalf("应翻两页、去重后 %d 条、翻到尽头：calls=%d %+v", afterPageCount+1, calls, len(page.Messages))
	}
}

// 页里夹着一条翻译不了的消息（发送者为 0）：能翻译的部分照常交回，但不能证明翻到了尽头。
func TestHistoryAfterPartialPageKeepsGoodMessages(t *testing.T) {
	sec := int64(1761643200)
	bad := strings.Replace(historyFrame(groupConv(), "7003", sec+2, true), `"user_id":`+sendAccount, `"user_id":0`, 1)
	if bad == historyFrame(groupConv(), "7003", sec+2, true) {
		t.Fatal("测试数据没改成坏消息")
	}
	for name, conv := range map[string]types.Conversation{"群": groupConv(), "私聊": privateConv()} {
		t.Run(name, func(t *testing.T) {
			badFrame := bad
			if conv.Scene == types.ScenePrivate {
				badFrame = strings.Replace(historyFrame(conv, "7003", sec+2, true), `"user_id":`+sendAccount, `"user_id":0`, 1)
			}
			e := newSendEnv(t, func(act sentAction) (string, bool) {
				if !strings.HasSuffix(act.Action, "_msg_history") {
					return "", false
				}
				return afterReply(historyFrame(conv, "7001", sec, false), badFrame, historyFrame(conv, "7002", sec+1, true)), true
			})
			page, err := e.ad.HistoryAfter(context.Background(), sendAccount, conv, "7001")
			if err != nil {
				t.Fatalf("个别坏消息不该让整次查询失败：%v", err)
			}
			if len(page.Messages) != 2 || !hasMessage(page.Messages, "7002") {
				t.Fatalf("应带回能翻译的两条：%+v", page.Messages)
			}
			if page.ReachedLatest {
				t.Fatal("页残缺，不能证明翻到了尽头")
			}
		})
	}
}
