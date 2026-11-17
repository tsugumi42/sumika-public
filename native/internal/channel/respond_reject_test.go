package channel

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// rejectKernel 模拟真内核在会话故障期间的 Respond：Submit 在受理这一步就拒了，只回一个带错、没有回合号的 Outcome，
// 不会有“回合定了”（Settled）。
type rejectKernel struct {
	*fakeKernel
	reject atomic.Bool
	calls  atomic.Int32
}

func (k *rejectKernel) Respond(in turn.Input) <-chan turn.Outcome {
	k.calls.Add(1)
	if k.reject.Load() {
		out := make(chan turn.Outcome, 1)
		out <- turn.Outcome{Err: errors.New("这个对话暂时没法继续：made")}
		return out
	}
	return k.fakeKernel.Respond(in)
}

// seqsDelivered 是交给内核（没被拒）的所有输入里出现过的 seq。
func seqsDelivered(k *fakeKernel) []int64 {
	var out []int64
	for _, in := range k.inputs() {
		out = append(out, in.Seqs...)
	}
	return out
}

// 没有任何一个闸门的回合挂在这个会话上。
func noRounds(r *Responder) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, st := range r.keys {
		if len(st.rounds) > 0 {
			return false
		}
	}
	return true
}

// 内核受理就拒的批次要让闸门收尾：不回复、不重试、不放回挂起，故障解除以后新来的批次照常交给内核（QQ 群、QQ 私聊各一条）。
// 本地私聊、本地群聊不经过闸门（主窗口直接收 Outcome），不在这里。
func TestRejectedBatchReleasesBarrierSoRecoveryWorks(t *testing.T) {
	for _, c := range []struct {
		name string
		conv types.Conversation
	}{{"QQ 群", convGroup}, {"QQ 私聊", convFriend}} {
		t.Run(c.name, func(t *testing.T) {
			h := newRW(t)
			rk := &rejectKernel{fakeKernel: h.k}
			h.r.cfg.Kernel = rk
			rk.reject.Store(true)
			h.r.Release(batchFor(c.conv, mk(11, whoFriend, "在吗", true)))
			h.waitFor("第一批被拒", func() bool { return rk.calls.Load() == 1 })
			h.waitFor("被拒的这一批让闸门收尾", func() bool { return noRounds(h.r) })
			if h.r.Holding("ca", c.conv.String()) {
				t.Fatal("被拒的批次不该被放回挂起")
			}

			// 故障解除：新来的批次交给内核；再让这一轮结束一次，给“被拒的旧批次事后补发”留出机会
			rk.reject.Store(false)
			h.r.Release(batchFor(c.conv, mk(12, whoFriend, "再来", true)))
			h.waitFor("恢复后新批次交给内核", func() bool { return rk.calls.Load() == 2 })
			h.k.finish("ca", c.conv.String(), turn.OutcomeDone)
			h.idle()
			if h.r.Holding("ca", c.conv.String()) {
				t.Fatal("这一轮结束以后不该有挂起的旧批次")
			}
			if got := seqsDelivered(h.k); !slices.Equal(got, []int64{12}) {
				t.Fatalf("恢复后只该处理新来的批次（seq 12），被拒的那一批（seq 11）从不补发：%v", got)
			}
			if n := rk.calls.Load(); n != 2 {
				t.Fatalf("没有多余的 Respond：%d", n)
			}
		})
	}
}

// 前一回合在跑时攒下的挂起批次：故障期间被并成一批交出去仍被拒，同样收尾，不留死锁、不事后补发。
func TestHeldBatchRejectedAlsoReleasesBarrier(t *testing.T) {
	h := newRW(t)
	rk := &rejectKernel{fakeKernel: h.k}
	h.r.cfg.Kernel = rk
	conv := convGroup.String()
	h.r.Release(batchFor(convGroup, mk(11, whoFriend, "在吗", true)))
	h.waitFor("第一批交给内核", func() bool { return rk.calls.Load() == 1 })
	// 第一回合还没定：第二批进挂起。内核不收插话（steerOK 关），它走“这一轮其实不在跑，等处理完再回”的挂起路径；
	// 先等到能证明它进了挂起（Holding 为真），再让前一轮结束
	h.k.mu.Lock()
	h.k.steerOK = false
	h.k.mu.Unlock()
	h.r.Release(batchFor(convGroup, mk(12, whoFriend, "再来", false)))
	h.waitFor("第二批进了挂起", func() bool { return h.r.Holding("ca", conv) })
	rk.reject.Store(true)
	h.k.finish("ca", conv, turn.OutcomeDone) // 第一回合定了：挂起的并成一批交出去，被拒
	h.waitFor("挂起的那批被拒", func() bool { return rk.calls.Load() == 2 })
	h.waitFor("被拒后闸门收尾", func() bool { return noRounds(h.r) && !h.r.Holding("ca", conv) })
	rk.reject.Store(false)
	h.r.Release(batchFor(convGroup, mk(13, whoFriend, "恢复了", true)))
	h.waitFor("恢复后新批次交给内核", func() bool { return rk.calls.Load() == 3 })
	h.k.finish("ca", conv, turn.OutcomeDone)
	h.idle()
	if got := seqsDelivered(h.k); !slices.Equal(got, []int64{11, 13}) {
		t.Fatalf("被拒的挂起批次（seq 12）事后不该补发：%v", got)
	}
}

// 被拒的这一批：判断结局回填成 error，并且留一条没有正文的出错计划（照 TestGateSettledOutcomes 的写法，真闸门、真库）。
func TestRejectedBatchRecordsErrorOutcomeAndPlan(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	rk := &rejectKernel{fakeKernel: x.k}
	x.r.cfg.Kernel = rk
	rk.reject.Store(true)
	id := releaseOutcomeBatch(t, x, g, true)
	eventually(t, "被拒的判断回填了结局", func() bool {
		rows, err := x.st.GateDecisions(convGroup.String(), 10)
		return err == nil && len(rows) == 1 && rows[0].Outcome != ""
	})
	checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeError}, "内核没有受理这一批")
	eventually(t, "留了一条出错计划", func() bool {
		return countRows(t, x.st, `SELECT count(*) FROM outbound_plans WHERE kind = ?`, store.PlanKindError) == 1
	})
	var planID string
	if err := x.st.Events.QueryRow(`SELECT id FROM outbound_plans WHERE kind = ?`, store.PlanKindError).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	p, err := x.st.GetPlan(planID)
	if err != nil || len(p.Parts) != 0 || p.EventSeq != 0 {
		t.Fatalf("出错计划没有正文、不写 events：%+v %v", p, err)
	}
	if n := len(x.k.inputs()); n != 0 {
		t.Fatalf("被拒的批次不交给内核回：%d", n)
	}
}

// 判据不能放宽成“只要有错就收尾”：有回合号、有错的结果是内核受理了的一轮，照旧等“回合定了”，判断结局由 Settled 那条路写。
func TestOutcomeWithTurnIDAndErrorStillWaitsForSettled(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	errKernel := &errWithIDKernel{fakeKernel: x.k, out: make(chan turn.Outcome, 1)}
	x.r.cfg.Kernel = errKernel
	id := releaseOutcomeBatch(t, x, g, true)
	conv := convGroup.String()
	eventually(t, "回合号记下了", func() bool {
		x.r.mu.Lock()
		defer x.r.mu.Unlock()
		st := x.r.keys[roundKey("ca", conv)]
		return st != nil && len(st.rounds) == 1 && st.rounds[0].turnID == "t-made"
	})
	// 有回合号的错不触发被拒收尾：闸门仍挂着这一轮，判断还没有结局，也没有出错计划
	x.r.mu.Lock()
	st := x.r.keys[roundKey("ca", conv)]
	x.r.mu.Unlock()
	if noRounds(x.r) || st == nil {
		t.Fatal("有回合号的错不是被拒：闸门该继续等 Settled")
	}
	if n := countRows(t, x.st, `SELECT count(*) FROM outbound_plans WHERE kind = ?`, store.PlanKindError); n != 0 {
		t.Fatalf("还没定，不该有出错计划：%d", n)
	}
	rows, err := x.st.GateDecisions(conv, 10)
	if err != nil || len(rows) != 1 || rows[0].Outcome != "" {
		t.Fatalf("还没定，判断不该有结局：%+v %v", rows, err)
	}
	// 内核说定了：照旧由 Settled 写结局
	x.r.Settled(turn.Settled{TurnID: "t-made", CharacterID: "ca", Conversation: conv, Outcome: turn.OutcomeError, Err: errors.New("made"), Audience: errKernel.in.Audience})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := x.r.Idle(ctx); err != nil {
		t.Fatal(err)
	}
	checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeError}, "回合出错")
}

// errWithIDKernel：Respond 回一个有回合号、有错的结果，但这时还没有“回合定了”。
type errWithIDKernel struct {
	*fakeKernel
	out chan turn.Outcome
	in  turn.Input
}

func (k *errWithIDKernel) Respond(in turn.Input) <-chan turn.Outcome {
	k.in = in
	k.out <- turn.Outcome{TurnID: "t-made", Err: errors.New("made")}
	return k.out
}

// rejected 先判断、再领结局：这一轮已被标成定了（比如同会话上别的运行发来的 Settled 误标）时，判断结局不能被它领走。
func TestRejectedDoesNotClaimOutcomeOfSettledRound(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	releaseOutcomeBatch(t, x, g, true)
	key := roundKey("ca", convGroup.String())
	x.r.mu.Lock()
	rd := x.r.keys[key].rounds[0]
	rd.settled = true
	x.r.mu.Unlock()
	x.r.rejected(key, rd, errors.New("made"))
	x.r.mu.Lock()
	claimed := rd.outcomeClaimed
	x.r.mu.Unlock()
	if claimed {
		t.Fatal("已经定了的回合，被拒收尾不该领走它的判断结局（领走又不写，这一轮的判断就永远没有结局）")
	}
	rows, err := x.st.GateDecisions(convGroup.String(), 10)
	if err != nil || len(rows) != 1 || rows[0].Outcome != "" {
		t.Fatalf("判断结局不该被写：%+v %v", rows, err)
	}
}

// 关机窗口：ctx 已取消时被拒的批次照现有“被打断”的标法，不记成出错，也不留出错计划。
func TestRejectedDuringShutdownIsMarkedInterrupted(t *testing.T) {
	t.Parallel()
	x := newRO(t)
	g := newOutcomeGate(t, x)
	id := releaseOutcomeBatch(t, x, g, true)
	key := roundKey("ca", convGroup.String())
	x.r.mu.Lock()
	rd := x.r.keys[key].rounds[0]
	x.r.mu.Unlock()
	x.r.Stop()
	x.r.rejected(key, rd, errors.New("made"))
	checkGateOutcomes(t, x, map[int64]string{id: store.GateOutcomeInterrupted}, "后台关闭")
	if n := countRows(t, x.st, `SELECT count(*) FROM outbound_plans WHERE kind = ?`, store.PlanKindError); n != 0 {
		t.Fatalf("关机窗口里不留出错计划：%d", n)
	}
}
