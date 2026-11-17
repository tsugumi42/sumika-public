package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 真闸门、真出口和临时库，内核由现有假内核控制结局。号、名字和话都是编的。
func newOutcomeGate(t *testing.T, x *ro) *Gate {
	t.Helper()
	g, err := NewGate(GateConfig{Stores: store.Single(x.st), Roster: testRoster(), Release: x.r.Release, Jitter: -1, Logf: x.logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	x.r.cfg.OnTurnDone = g.TurnDone
	return g
}

func waitOutcomeRoute(t *testing.T, r *Responder) {
	t.Helper()
	select {
	case <-r.arrive.idleChan():
	case <-time.After(10 * time.Second):
		t.Fatal("放行批次的路由没有做完")
	}
}

// 每次相隔两分钟，避开密集 @ 合并；called 控制放行批的听众，以便分别走 Stop 和 Steer。
func releaseOutcomeBatch(t *testing.T, x *ro, g *Gate, called bool) int64 {
	t.Helper()
	m := x.say(convGroup, whoFriend, "编的消息", called)
	m.Time = baseTime.Add(time.Duration(m.Seq) * 2 * time.Minute)
	if called {
		segs, err := json.Marshal([]types.Seg{{Kind: types.SegAt, Target: acctA}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := x.st.Events.Exec(`UPDATE events SET segments_json = ? WHERE seq = ?`, string(segs), m.Seq); err != nil {
			t.Fatal(err)
		}
	}
	// 此测试只控制放行条件；未叫到的批次用低门槛状态放行，路由仍按真实听众判断。
	if !called {
		_, since, _, err := x.st.LoadGateState(gateKey("ca", convGroup))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(lowRounds())
		if err != nil {
			t.Fatal(err)
		}
		if err := x.st.SaveGateState(gateKey("ca", convGroup), string(raw), since, m.Time.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	g.Handle(Dispatch{Batch: batchFor(convGroup, m)})
	waitOutcomeRoute(t, x.r)
	rows, err := x.st.GateDecisions(convGroup.String(), 1)
	if err != nil || len(rows) != 1 || (rows[0].Verdict != VerdictForced && rows[0].Verdict != VerdictEnter) {
		t.Fatalf("没有放行判断：%+v，%v", rows, err)
	}
	return rows[0].ID
}

func checkGateOutcomes(t *testing.T, x *ro, want map[int64]string, reason string) {
	t.Helper()
	rows, err := x.st.GateDecisions(convGroup.String(), 100)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[int64]string, len(rows))
	for _, row := range rows {
		got[row.ID] = row.Outcome
		if expected, ok := want[row.ID]; ok && (row.Outcome != expected || !strings.Contains(row.OutcomeReason, reason)) {
			t.Errorf("判断 %d：结局 %q、原因 %q，想要 %q、原因含 %q", row.ID, row.Outcome, row.OutcomeReason, expected, reason)
		}
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("判断 %d 丢了", id)
		}
	}
}

func TestGateSettledOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode, settled, outcome, reason string
		said                                 []turn.Said
		setup                                func(*ro)
	}{
		{name: "开口", outcome: store.GateOutcomeSpoke, said: []turn.Said{{Step: 0, Text: "在的"}}},
		{name: "试运行开口", mode: ModeDryRun, outcome: store.GateOutcomeDry, said: []turn.Said{{Step: 0, Text: "在的"}}},
		{name: "空回复", outcome: store.GateOutcomeSelfSkip, reason: "看完没出声"},
		{name: "字数拦下", outcome: store.GateOutcomeFuse, reason: "保险丝", said: []turn.Said{{Step: 0, Text: strings.Repeat("字", 201)}}},
		{name: "改写为空", outcome: store.GateOutcomeBlocked, reason: "改写以后为空", said: []turn.Said{{Step: 0, Text: "编的待改写文本"}}, setup: func(x *ro) {
			x.r.cfg.Shapers = map[string]Shaper{"qq": {Rewrite: func(string) string { return "" }}}
		}},
		{name: "守卫拦下", outcome: store.GateOutcomeBlocked, reason: "守卫拦下", said: []turn.Said{{Step: 0, Text: "编的私事"}}, setup: func(x *ro) { x.guard.set(func(g *fakeGuard) { g.blockOn = []string{"编的私事"} }) }},
		{name: "被打断", settled: turn.OutcomeStopped, outcome: store.GateOutcomeInterrupted, reason: "被新消息打断", said: []turn.Said{{Step: 0, Text: "说到一半"}}},
		{name: "内核出错", settled: turn.OutcomeError, outcome: store.GateOutcomeError, reason: "编的模型错误"},
		{name: "写库出错", outcome: store.GateOutcomeError, reason: "写库失败", said: []turn.Said{{Step: 0, Text: "在的"}}, setup: func(x *ro) {
			if _, err := x.st.Events.Exec(`CREATE TRIGGER reject_reply BEFORE INSERT ON events WHEN NEW.actor_kind = 'character' BEGIN SELECT RAISE(FAIL, '编的写库错误'); END`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "部分被拦仍开口", outcome: store.GateOutcomeSpoke, said: []turn.Said{{Step: 0, Text: "在的"}, {Step: 1, Text: strings.Repeat("字", 201)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := newRO(t)
			g := newOutcomeGate(t, x)
			if tc.mode != "" {
				x.setMode(tc.mode)
			}
			if tc.setup != nil {
				tc.setup(x)
			}
			id := releaseOutcomeBatch(t, x, g, true)
			settled := tc.settled
			if settled == "" {
				settled = turn.OutcomeDone
			}
			x.k.finishErr("ca", convGroup.String(), settled, fmt.Errorf("编的模型错误"), tc.said...)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := x.r.Idle(ctx); err != nil {
				t.Fatal(err)
			}
			checkGateOutcomes(t, x, map[int64]string{id: tc.outcome}, tc.reason)
		})
	}
}

func TestGateMergedDecisionOutcomes(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	x.k.mu.Lock()
	x.k.steerOK = false
	x.k.mu.Unlock()
	first := releaseOutcomeBatch(t, x, g, true)
	second := releaseOutcomeBatch(t, x, g, false)
	third := releaseOutcomeBatch(t, x, g, false)
	x.settleAndWait(convGroup, turn.OutcomeDone)
	checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeSelfSkip, second: "", third: ""}, "")
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "一起回"})
	checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeSelfSkip, second: store.GateOutcomeSpoke, third: store.GateOutcomeSpoke}, "")
}

func TestGateSteerDecisionOutcomes(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"delivered", "dropped", "missing"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			x := newRO(t)
			g := newOutcomeGate(t, x)
			first := releaseOutcomeBatch(t, x, g, true)
			second := releaseOutcomeBatch(t, x, g, false)
			if phase != "missing" {
				x.r.OnSteer(turn.SteerEvent{ID: "s2", Phase: phase, Requeue: phase == "dropped"})
			}
			x.settleAndWait(convGroup, turn.OutcomeStopped)
			if phase == "delivered" {
				checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeInterrupted, second: store.GateOutcomeInterrupted}, "被新消息打断")
			} else {
				checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeInterrupted, second: ""}, "")
				x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "接到下一轮"})
				checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeInterrupted, second: store.GateOutcomeSpoke}, "")
			}
		})
	}
}

func TestGateCountsSettledSteps(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{ModeSpeak, ModeDryRun} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			x := newRO(t)
			x.setMode(mode)
			g := newOutcomeGate(t, x)
			releaseOutcomeBatch(t, x, g, true)
			x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "第一句\n\n第二个气泡"}, turn.Said{Step: 1, Text: "第三个气泡"}, turn.Said{Step: 2, Text: strings.Repeat("字", 201)})
			st, _, err := g.load(gateKey("ca", convGroup))
			if err != nil || len(st.Replies) != 2 || st.Idle != (GateIdle{}) {
				t.Fatalf("两步过裁决、一步被拦，频率该记两句并清退避：%+v，%v", st, err)
			}
		})
	}
}

func TestGateOutcomeWriteFailureKeepsRoundWorking(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	releaseOutcomeBatch(t, x, g, true)
	if _, err := x.st.Trace.Exec(`CREATE TRIGGER reject_outcome BEFORE UPDATE OF outcome ON gate_decisions BEGIN SELECT RAISE(FAIL, '编的结局写库错误'); END`); err != nil {
		t.Fatal(err)
	}
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "第一轮"})
	x.start(convGroup, x.say(convGroup, whoFriend, "再问一次", true))
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "第二轮"})
	x.waitSent(2)
	if got := x.hers(convGroup); len(got) != 2 || got[0].Text != "第一轮" || got[1].Text != "第二轮" || !strings.Contains(x.logText(), "编的结局写库错误") {
		t.Fatalf("结局写库失败也该发完、放开屏障并记日志：%+v\n%s", got, x.logText())
	}
}

// 临时库里的触发器记录每一次 UPDATE，重复写同一个结局也能查出来；不改生产库接口。
func countGateOutcomeWrites(t *testing.T, x *ro) {
	t.Helper()
	if _, err := x.st.Trace.Exec(`CREATE TABLE outcome_writes (decision_id INTEGER NOT NULL);
		CREATE TRIGGER count_outcome AFTER UPDATE OF outcome ON gate_decisions
		BEGIN INSERT INTO outcome_writes (decision_id) VALUES (NEW.id); END`); err != nil {
		t.Fatal(err)
	}
}

func checkGateOutcomeWrites(t *testing.T, x *ro, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		var n int
		if err := x.st.Trace.QueryRow(`SELECT COUNT(*) FROM outcome_writes WHERE decision_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("判断 %d 写了 %d 次结局，想要恰好 1 次", id, n)
		}
	}
}

func TestGateCloseRunningOutcome(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	id := releaseOutcomeBatch(t, x, g, true)
	x.r.Close()
	x.r.Close()
	checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
	checkGateOutcomeWrites(t, x, id)
}

func TestGateCloseHeldOutcome(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	x.k.steerOK = false
	first := releaseOutcomeBatch(t, x, g, true)
	second := releaseOutcomeBatch(t, x, g, false)
	if !x.r.Holding("ca", convGroup.String()) {
		t.Fatal("第二批该挂在上一回合后面")
	}
	x.r.Close()
	x.r.Close()
	checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeInterrupted, second: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
	checkGateOutcomeWrites(t, x, first, second)
}

func TestGateCloseSteerOutcome(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	first := releaseOutcomeBatch(t, x, g, true)
	second := releaseOutcomeBatch(t, x, g, false)
	x.r.Close()
	x.r.Close()
	checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeInterrupted, second: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
	checkGateOutcomeWrites(t, x, first, second)
}

func TestGateCloseSettledOutcome(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	x.setMode(ModeDryRun)
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	id := releaseOutcomeBatch(t, x, g, true)
	x.settleAndWait(convGroup, turn.OutcomeDone, turn.Said{Step: 0, Text: "编的试运行回复"})
	x.r.Close()
	checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeDry}, "")
	checkGateOutcomeWrites(t, x, id)
}

func TestGateCloseQueuedOutcome(t *testing.T) {
	t.Parallel()
	for _, queue := range []string{"arrive", "settle"} {
		t.Run(queue, func(t *testing.T) {
			t.Parallel()
			x := newRO(t)
			g := newOutcomeGate(t, x)
			countGateOutcomeWrites(t, x)
			var batch types.Batch
			if queue == "arrive" {
				g.cfg.Release = func(b types.Batch) { batch = b }
			}
			id := releaseOutcomeBatch(t, x, g, true)
			q := x.r.arrive
			if queue == "settle" {
				q = x.r.settle
			}
			entered := make(chan struct{})
			q.submit(roundKey("ca", convGroup.String()), job{run: func() {
				close(entered)
				<-x.r.ctx.Done()
			}, drop: func() {}})
			<-entered
			if queue == "arrive" {
				x.r.Release(batch)
			} else {
				x.k.finish("ca", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "编的排队回复"})
			}
			x.r.Close()
			checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
			checkGateOutcomeWrites(t, x, id)
		})
	}
}

func TestGateCloseDuringProcessOutcome(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	x := newRO(t)
	// 裁决已开始，等关闭再让改写返回；出口会拒绝这句，不写计划、不交回内核。
	x.r.cfg.Shapers["qq"] = Shaper{Rewrite: func(s string) string {
		close(entered)
		<-x.r.ctx.Done()
		return s
	}}
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	id := releaseOutcomeBatch(t, x, g, true)
	x.k.finish("ca", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "编的未完成回复"})
	<-entered
	x.r.Close()
	checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeInterrupted}, "渠道已关闭")
	checkGateOutcomeWrites(t, x, id)
}

func TestGateLateReleaseOutcome(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	x.r.Close()
	id := releaseOutcomeBatch(t, x, g, true)
	checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
	checkGateOutcomeWrites(t, x, id)
}

func TestGateCloseDuringStopOutcome(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	first := releaseOutcomeBatch(t, x, g, true)
	steer := releaseOutcomeBatch(t, x, g, false)
	x.k.autoStop = false
	x.k.onStop = func() {
		x.r.cancel()
		x.r.OnSteer(turn.SteerEvent{ID: "s2", Phase: "dropped"})
	}
	// 这一批明确叫到她，保证走 StopIn；这个测试验路由关闭，不依赖闸门重建 @ 段。
	g.cfg.Release = func(b types.Batch) {
		b.Addressed = true
		x.r.Release(b)
	}
	last := releaseOutcomeBatch(t, x, g, true)
	x.r.Close()
	checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeInterrupted, steer: store.GateOutcomeInterrupted, last: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
	checkGateOutcomeWrites(t, x, first, steer, last)
}

func TestGateCloseBeforeProcessOutcome(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	id := releaseOutcomeBatch(t, x, g, true)
	entered, release := make(chan struct{}), make(chan struct{})
	x.r.settle.submit(roundKey("ca", convGroup.String()), job{run: func() {
		close(entered)
		<-release
	}, drop: func() {}})
	<-entered
	x.k.finish("ca", convGroup.String(), turn.OutcomeDone, turn.Said{Step: 0, Text: "编的未裁决回复"})
	// 先取消、再让 process 开始；还不关队列，避免这件活直接被 drop。
	x.r.cancel()
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := x.r.Idle(ctx); err != nil {
		t.Fatal(err)
	}
	x.r.Close()
	checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
	checkGateOutcomeWrites(t, x, id)
}

// 两个入口都走真实判断；fire 先攒第二次 @，再由 FlushBursts 触发，不等真实的一分钟。
func prepareGateHandoff(t *testing.T, x *ro, g *Gate, burst bool) func() {
	t.Helper()
	m := x.say(convGroup, whoFriend, "编的交接消息", true)
	m.Time = baseTime
	if burst {
		g.Handle(Dispatch{Batch: batchFor(convGroup, m)})
		waitOutcomeRoute(t, x.r)
		m = x.say(convGroup, whoFriend, "编的第二次叫到", true)
		m.Time = baseTime.Add(time.Second)
		g.Handle(Dispatch{Batch: batchFor(convGroup, m)})
		return g.FlushBursts
	}
	return func() { g.Handle(Dispatch{Batch: batchFor(convGroup, m)}) }
}

func waitHandoff(t *testing.T, ctx context.Context, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("等 %s 超时", what)
	}
}

func TestGateCloseWaitsForHandoff(t *testing.T) {
	t.Parallel()
	for _, burst := range []bool{false, true} {
		t.Run(fmt.Sprintf("burst=%t", burst), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			x := newRO(t)
			g := newOutcomeGate(t, x)
			run := prepareGateHandoff(t, x, g, burst)
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			g.cfg.Release = func(types.Batch) {
				close(entered)
				<-release
			}
			done, closed := make(chan struct{}), make(chan struct{})
			go func() { run(); close(done) }()
			t.Cleanup(func() {
				unblock()
				waitHandoff(t, ctx, done, "交接退出")
			})
			waitHandoff(t, ctx, entered, "Release 开始")
			go func() { g.Close(); close(closed) }()
			t.Cleanup(func() {
				unblock()
				waitHandoff(t, ctx, closed, "Gate.Close 退出")
			})
			waitHandoff(t, ctx, g.ctx.Done(), "闸门取消")
			select {
			case <-closed:
				t.Error("Release 还没返回，Gate.Close 就返回了")
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			waitHandoff(t, ctx, done, "交接返回")
			waitHandoff(t, ctx, closed, "Gate.Close 返回")
		})
	}
}

func TestGateCanceledHandoffOutcome(t *testing.T) {
	t.Parallel()
	for _, burst := range []bool{false, true} {
		t.Run(fmt.Sprintf("burst=%t", burst), func(t *testing.T) {
			t.Parallel()
			x := newRO(t)
			g := newOutcomeGate(t, x)
			countGateOutcomeWrites(t, x)
			run := prepareGateHandoff(t, x, g, burst)
			released := false
			g.cfg.Release = func(types.Batch) { released = true }
			g.afterDecide = g.stop
			run()
			g.Close()
			rows, err := g.Decisions(convGroup, 1)
			if err != nil || len(rows) != 1 || rows[0].Verdict != VerdictForced {
				t.Fatalf("交接前该已有放行判断：%+v，%v", rows, err)
			}
			if released {
				t.Error("判断写完后已取消，不该再调 Release")
			}
			checkGateOutcomes(t, x, map[int64]string{rows[0].ID: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
			checkGateOutcomeWrites(t, x, rows[0].ID)
		})
	}
}

func TestGateCloseWhileStopInOutcome(t *testing.T) {
	t.Parallel()
	// 所有阶段共用这一个超时，Close 或回调卡住都不能把测试一直挂着。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	x := newRO(t)
	g := newOutcomeGate(t, x)
	countGateOutcomeWrites(t, x)
	first := releaseOutcomeBatch(t, x, g, true)
	steer := releaseOutcomeBatch(t, x, g, false)
	x.k.mu.Lock()
	ft := x.k.run[roundKey("ca", convGroup.String())]
	x.k.autoStop = false
	x.k.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	x.k.onStop = func() {
		close(entered)
		<-release
		x.r.OnSteer(turn.SteerEvent{ID: "s2", Phase: "dropped"})
		x.k.settleTurn(ft, "ca", turn.OutcomeStopped, nil, nil)
	}
	// 明确叫到她，保证路由走 StopIn；先截住批次，便于拿到它的真实判断 ID。
	var batch types.Batch
	g.cfg.Release = func(b types.Batch) { batch = b }
	last := releaseOutcomeBatch(t, x, g, true)
	batch.Addressed = true
	x.r.Release(batch)
	t.Cleanup(unblock)
	waitHandoff(t, ctx, entered, "StopIn 开始")
	closed := make(chan struct{})
	go func() { x.r.Close(); close(closed) }()
	t.Cleanup(func() {
		unblock()
		waitHandoff(t, ctx, closed, "Responder.Close 退出")
	})
	waitHandoff(t, ctx, x.r.ctx.Done(), "Responder 取消")
	unblock()
	waitHandoff(t, ctx, closed, "Responder.Close 返回")
	// StopIn 中的插话和 Settled 回调都同步做完，Close 又等了路由退出，再查重复写入。
	checkGateOutcomes(t, x, map[int64]string{first: store.GateOutcomeInterrupted, steer: store.GateOutcomeInterrupted, last: store.GateOutcomeInterrupted}, "后台关闭，这一轮没做完")
	checkGateOutcomeWrites(t, x, first, steer, last)
}
