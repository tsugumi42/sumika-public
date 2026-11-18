package channel

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 放大十倍的量法（项目 AGENTS「验收」，规格-渠道中间件 第十六节 F）：十个角色同群、通知风暴。
// 报的是量出来的数（每次 -v 能看到），断言只防「慢了一个数量级」，不卡具体毫秒。
// 上万条 events 里取最近 30 条的耗时在 store 包的 TestRecentMessagesIsFastOnBigTable

// 十个角色（十个号）同在一个群，300 条消息每个号各收一份：事件只存 300 条，"看到了"3000 条，每个角色各过一遍链、各交一次闸门
func TestScaleTenCharactersInOneGroup(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("放大十倍的量法，跑得久一点")
	}
	const chars = 10
	msgs := 300
	if raceBuild {
		msgs = 30 // -race 下慢一个数量级：量法不变，规模缩小
	}
	roster := Roster{Owners: map[string]string{chQQ: owner}}
	var accounts []string
	for i := 1; i <= chars; i++ {
		id := strconv.Itoa(21000 + i)
		accounts = append(accounts, id)
		roster.Accounts = append(roster.Accounts, Account{chQQ, id, fmt.Sprintf("c%d", i)})
	}
	var calls atomic.Int64
	s := &spy{verdict: func(types.Batch) types.Verdict { calls.Add(1); return types.Passed("") }}
	h := newPH(t, []types.Interceptor{s.plugin("spy", 20)}, func(c *Config, p *PipelineConfig, _ *ChainConfig) { c.Roster, p.Roster = roster, roster })
	g := group("5001")

	start := time.Now()
	for i := 1; i <= msgs; i++ {
		from := []string{friend, other}[i%2]
		for _, a := range accounts {
			if err := h.in.Inbound(context.Background(), msg(a, g, strconv.Itoa(i), from, fmt.Sprintf("第%d句", i))); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.idleWithin(120 * time.Second)
	elapsed := time.Since(start)

	var events, observations int
	h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE conversation = ?`, g.String()).Scan(&events)
	h.st.Events.QueryRow(`SELECT count(*) FROM observations`).Scan(&observations)
	t.Logf("（-race：%v）%d 个角色同群，%d 条消息 × %d 个号 = %d 次交来：用时 %v（每次 %v）；events %d 条，看到了 %d 条，链被问 %d 次，闸门收到 %d 次",
		raceBuild, chars, msgs, chars, msgs*chars, elapsed, elapsed/time.Duration(msgs*chars), events, observations, calls.Load(), len(h.dispatches()))
	if events != msgs || observations != msgs*chars || int(calls.Load()) != msgs*chars || len(h.dispatches()) != msgs*chars {
		t.Fatalf("数不对：events %d（想要 %d），看到了 %d（想要 %d），链 %d，闸门 %d（都想要 %d）", events, msgs, observations, msgs*chars, calls.Load(), len(h.dispatches()), msgs*chars)
	}
	if elapsed > 60*time.Second {
		t.Fatalf("慢了一个数量级：%v", elapsed)
	}
}

// 通知风暴：两千个不同的人一口气戳她。每分钟最多戳回 3 次，其余都被限流挡住；两千条通知都存下（各记一行"看到了"）；限流状态不跟着涨
func TestScalePokeStorm(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("放大十倍的量法，跑得久一点")
	}
	n := 2000
	if raceBuild {
		n = 200
	}
	h := newMH(t)
	g := group("5001")
	h.enable("poke", "5001")
	start := time.Now()
	for i := 0; i < n; i++ {
		h.pushNotice(types.Notice{Kind: types.NoticePoke, Account: acctA, Conv: g, Time: baseTime, Actor: strconv.Itoa(40000 + i), Target: acctA})
	}
	h.settleWithin(120 * time.Second)
	elapsed := time.Since(start)
	notices := h.count(`SELECT count(*) FROM events WHERE conversation = ? AND kind = 'notice'`, g.String())
	t.Logf("（-race：%v）%d 个人戳她：用时 %v（每个 %v）；戳回去 %d 次，存下的通知 %d 条，限流的日志 %d 行", raceBuild, n, elapsed, elapsed/time.Duration(n), len(h.ad.Pokes()), notices, strings.Count(h.logText(), "一分钟内已经戳回 3 次"))
	if len(h.ad.Pokes()) != 3 || notices != n {
		t.Fatalf("戳回去 %d 次（想要 3），存下通知 %d 条（想要 %d）", len(h.ad.Pokes()), notices, n)
	}
	if elapsed > 60*time.Second {
		t.Fatalf("慢了一个数量级：%v", elapsed)
	}
}
