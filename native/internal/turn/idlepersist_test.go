package turn

// P2-9 第 2 步小修：空闲压缩的失败退避重启以后也认。

import (
	"testing"
	"time"

	"sumika-native/internal/store"
)

// restartIdle 在拍下的数据上起一个新 Hub（模拟重启），仍然装空闲压缩的假时钟和假计时器
func restartIdle(t *testing.T, old *harness, replies string) (*harness, *fakeIdleTimers) {
	t.Helper()
	clock := &prefixClock{}
	timers := newFakeIdleTimers()
	h := restart(t, old, harnessOpts{replies: replies, mutate: func(c *Config) {
		clock.now = c.Now()
		c.Now = clock.Now
		c.IdleCompact = true
		c.IdleCompactAfter = 50 * time.Minute
		c.IdleCompactMinTokens = 1000
		c.AfterFunc = timers.AfterFunc
		c.Context = ContextLimits{Window: 200_000, SoftRatio: 0.7, HardRatio: 0.85, Reserve: 1, KeepTokens: 50}
	}})
	return h, timers
}

// 第 3 条：失败退避重启以后也认；库里最近一行 idle 失败不到 6 小时就不排，超过就排
func TestIdleCompactPersistedFailureBackoff(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		age  time.Duration
		want int
	}{
		{"6小时内不排", time.Hour, 0},
		{"超过6小时就排", 7 * time.Hour, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _, _ := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `]`}, 1000)
			h.seedRounds(8)
			h.send("先建实例")
			conv := consoleConv(h.c.CharacterID)
			msg := "上一次没成"
			if err := h.st.AddEpoch(store.Epoch{ID: "made-idle-failed", Conversation: conv, CharacterID: h.c.CharacterID,
				Kind: "chat", Trigger: "idle", Status: "failed", Error: &msg, CreatedAt: h.hub.now().Add(-tc.age).UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			r, timers := restartIdle(t, h, `[`+textReply+`,`+summaryReply+`]`)
			r.send("重启以后")
			if got := timers.count(); got != tc.want {
				t.Fatalf("重启以后排空闲压缩的个数：%d，应该 %d", got, tc.want)
			}
			if tc.want > 0 {
				r.fireIdle(t, timers.next(t))
				eps := r.epochs()
				if len(eps) != 2 || eps[1].status != "ok" || eps[1].trigger != "idle" {
					t.Fatalf("超过 6 小时以后照常压一次：%+v", eps)
				}
			}
		})
	}
}
