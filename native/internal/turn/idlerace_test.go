package turn

// P2-9 第 2 步小修：关机和空闲计时到点同时发生。

import (
	"sync"
	"testing"
)

// 第 6 条：关机和计时到点同时发生（-race 下跑），不误用 WaitGroup、不写快照
func TestIdleCompactShutdownRace(t *testing.T) {
	t.Parallel()
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`}, 1000)
	h.seedRounds(8)
	h.send("先建实例")
	timer := timers.next(t)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); timer.fire() }()
	go func() { defer wg.Done(); h.cancel() }()
	wg.Wait()
	h.hub.Idle()
	for _, e := range h.epochs() {
		if e.status == "ok" {
			t.Fatalf("关机路上不提交快照：%+v", e)
		}
	}
}
