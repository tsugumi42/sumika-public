package turn

// P2-9 第 2 步小修：空闲压缩的回合代次检查和整理提示的会话范围。

import (
	"net/http"
	"strings"
	"testing"

	"sumika-native/internal/audience"
)

// 第 5 条：计时到点、压缩排进会话队列以后又跑完一轮（turnGen 变了），这一次不再压
func TestIdleCompactStaleTurnGen(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h, _, _ := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `]`, intercept: func(_ int, body string, _ http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(body, compactMarker) {
			entered <- struct{}{}
			<-release
		}
		return false
	}}, 1000)
	h.seedRounds(8)
	char, conv := h.c.CharacterID, consoleConv(h.c.CharacterID)
	ck := convKey(char, conv)
	out := h.hub.Send(char, "跑一轮")
	recv(t, entered, "回合的模型请求")
	l := h.hub.liveOf(char)
	state := &idleCompactTimer{live: l}
	h.hub.mu.Lock()
	state.gen = h.hub.turnGen[ck]
	h.hub.idleTimers[ck] = state
	h.hub.mu.Unlock()
	h.hub.idleTimerFired(ck, state) // 计时到点，核对任务排在这个会话后面
	close(release)
	wait(t, out)
	h.hub.Idle()
	if c := h.countBodies(compactMarker); c != 0 {
		t.Fatalf("回合数变过以后不该再压：请求了 %d 次摘要", c)
	}
	if eps := h.epochs(); len(eps) != 0 {
		t.Fatalf("不该写快照：%+v", eps)
	}
}

// 第 5 条：话题、自检、悄悄话会话就算走到同一段也不写整理提示（会话名检查挡住）
func TestCompactNoticeOnlyOwnerConsole(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `]`})
	char := h.c.CharacterID
	owner := consoleConv(char)
	others := []string{
		owner + ":topic:t1",
		audience.SelftestConversation(char, audience.SelftestTopicID(char, "")),
		audience.WhisperConversation(char, owner),
	}
	for _, conv := range others {
		h.hub.noteCompact(Input{CharacterID: char, Conversation: conv, Audience: audience.Console(conv)}, "更早的 3 条聊天这次放不下了")
	}
	h.hub.noteCompact(consoleInput(char, 0, ""), "之前的聊天有点长，回复前先整理了一下")
	for _, conv := range others {
		if got := h.compactNotices(conv); len(got) != 0 {
			t.Fatalf("%s 不该写整理提示：%v", conv, got)
		}
	}
	if got := h.compactNotices(owner); len(got) != 1 {
		t.Fatalf("本地私聊写一行：%v", got)
	}
}
