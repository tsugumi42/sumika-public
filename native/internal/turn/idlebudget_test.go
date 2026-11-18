package turn

// P2-9 第 2 步小修：空闲压缩单独开后台预算根。

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/turnbudget"
)

// 第 2 条：最后一轮是自动回合（根只有 15 分钟），闲 50 分钟后空闲压缩用自己新开的根，照常成功、不进退避
func TestIdleCompactAfterAutoTurn(t *testing.T) {
	t.Parallel()
	h, clock, timers := newIdleHarness(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `]`}, 1000)
	h.seedRounds(8)
	conv := consoleConv(h.c.CharacterID)
	seq := h.storeConv(conv, "console", "owner", "owner", "自动续轮这句")
	wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: "自动续轮这句",
		Audience: audience.Console(conv), Addressed: true, Scope: ConsoleScope, Auto: true}))
	timer := timers.next(t)
	// 自动回合的根只有 15 分钟：过 50 分钟以后按已过期处理（停掉那一个根；不用拨真钟）
	if l, ok := h.hub.Cfg.Budget.(*turnbudget.Ledger); ok {
		if err := l.Stop(context.Background(), h.hub.liveOf(h.c.CharacterID).in.Identity.RootID); err != nil {
			t.Fatal(err)
		}
	}
	clock.add(50 * time.Minute)
	h.fireIdle(t, timer)
	eps := h.epochs()
	if len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "idle" {
		t.Fatalf("自动回合以后空闲压缩照常写一份 idle 快照：%+v", eps)
	}
	if got := h.compactRunTriggers(); len(got) != 1 || got[0] != "idle" {
		t.Fatalf("运行页记 idle：%v", got)
	}
	h.hub.mu.Lock()
	_, failed := h.hub.compactFailures[convKey(h.c.CharacterID, conv)]
	h.hub.mu.Unlock()
	if failed {
		t.Fatal("成功了不该记失败退避")
	}
}

// 第 3 条：顶层那组上限不再压住大实例——实例 30 万 token 时，空闲压缩的根按实例放大令牌额，摘要请求照常发出去
func TestIdleCompactBudgetFollowsInstanceSize(t *testing.T) {
	t.Parallel()
	// 上一轮服务商报 30 万输入（实例按它算 30 万）；实例里的原文另有 17.8 万汉字（约 53 万字节，超过顶层那组 524288，
	// 不到实例三倍的 90 万）：旧上限会在这里拦下，新的按实例放大以后放行
	bigUsage := `{"content":["好的"],"usage":{"prompt_tokens":300000,"completion_tokens":10}}`
	h, _, timers := newIdleHarness(t, harnessOpts{replies: `[` + bigUsage + `,` + summaryReply + `]`, mutate: func(c *Config) {
		// 30 万的实例要建得起来、也不会先触发回复前的现场压缩
		c.Context = ContextLimits{Window: 2_000_000, SoftRatio: 0.7, HardRatio: 0.85, Reserve: 1, KeepTokens: 50}
		// 原文用多条短话凑（一条几万字会触发 events_short 的逐字配对，慢）；都在实例里
		c.RecentMessages = 250
	}}, 100_000)
	conv := consoleConv(h.c.CharacterID)
	// 200 条 × 890 个汉字 = 17.8 万；主人和她交替，切点找得到
	for i := range 200 {
		kind, id := "owner", "owner"
		if i%2 == 1 {
			kind, id = "character", h.c.CharacterID
		}
		h.storeConv(conv, "console", kind, id, strings.Repeat(string([]rune("甲乙丙丁戊己庚辛壬癸")[i%10]), 890))
	}
	h.send("先建实例")
	l := h.hub.liveOf(h.c.CharacterID)
	if size := max(int(l.lastTotalInput), sizeOf(l)); size < 300_000 {
		t.Fatalf("实例要够 30 万才测得到这条：%d", size)
	}
	if h.logged("被预算拦下") {
		t.Fatalf("还没压就报了预算拦下：%v", h.logs)
	}
	h.fireIdle(t, timers.next(t))
	if h.logged("被预算拦下") {
		t.Fatalf("实例 30 万时空闲压缩不该被预算拦下：%v", h.logs)
	}
	if eps := h.epochs(); len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "idle" {
		t.Fatalf("摘要照常写完：%+v", eps)
	}
}
