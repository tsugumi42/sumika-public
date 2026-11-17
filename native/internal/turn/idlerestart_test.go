package turn

// P2-9 第 2 步小修：检查点回退时停掉在跑的后台压缩。

import (
	"net/http"
	"strings"
	"testing"
)

// 检查点回退走 RestartIn：在跑的压缩请求要当场停掉（作废的快照没用了，不等它回来），也不记失败
func TestRestartInCancelsCompact(t *testing.T) {
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
	h.hub.RestartIn(h.c.CharacterID, consoleConv(h.c.CharacterID))
	recv(t, canceled, "检查点回退取消压缩请求")
	h.hub.Idle()
	if eps := h.epochs(); len(eps) != 0 {
		t.Fatalf("作废的那次不写快照，也不写失败行：%+v", eps)
	}
	conv := consoleConv(h.c.CharacterID)
	h.hub.mu.Lock()
	_, failed := h.hub.compactFailures[convKey(h.c.CharacterID, conv)]
	h.hub.mu.Unlock()
	if failed {
		t.Fatal("作废不算失败：不该记失败退避")
	}
	if h.hub.idleFailedRecently(h.c.CharacterID, conv) {
		t.Fatal("作废以后空闲压缩不该退避 6 小时")
	}
}
