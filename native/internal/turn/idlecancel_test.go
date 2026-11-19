package turn

// P2-9 第 2 步小修：开新话题时停掉在跑的后台压缩。

import (
	"net/http"
	"strings"
	"testing"
)

// 第 8 条：开新话题时停掉这个会话在跑的后台压缩，不等它回来
func TestStartNewTopicCancelsCompact(t *testing.T) {
	t.Parallel()
	entered, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`, intercept: func(_ int, body string, _ http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(body, compactMarker) {
			entered <- struct{}{}
			<-r.Context().Done()
			canceled <- struct{}{}
			return true
		}
		return false
	}}, 1000)
	h.seedRounds(8)
	h.send("先建实例")
	timer := timers.next(t)
	timer.fire()
	recv(t, entered, "空闲压缩的摘要请求")
	if err := h.hub.StartNewTopic(h.c.CharacterID, consoleConv(h.c.CharacterID)); err != nil {
		t.Fatal(err)
	}
	recv(t, canceled, "开新话题取消压缩请求")
	h.hub.Idle()
	if eps := h.epochs(); len(eps) != 0 {
		t.Fatalf("取消的那次不写快照，也不写失败行：%+v", eps)
	}
	conv := consoleConv(h.c.CharacterID)
	h.hub.mu.Lock()
	_, failed := h.hub.compactFailures[convKey(h.c.CharacterID, conv)]
	h.hub.mu.Unlock()
	if failed {
		t.Fatal("取消不算失败：不该记失败退避")
	}
	if h.hub.idleFailedRecently(h.c.CharacterID, conv) {
		t.Fatal("取消以后空闲压缩不该退避 6 小时")
	}
}
