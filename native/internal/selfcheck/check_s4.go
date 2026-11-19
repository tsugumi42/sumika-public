package selfcheck

import (
	"context"
	"strconv"
	"time"
)

// turnStuck 是 S4 turn.stuck：回合开始了，既没有结局，也不在运行，也不是在等主人批准，已经超过 stuckAfter。
//
// 读：trace 库 turns（ended_at 为空、started_at 早于阈值）；排除 steps 里 status='waiting' 的（停在审批卡上，SweepInterrupted 也是这么认的）；
// 再排除进程内正在跑的回合（Env.Running，含内部运行）。Running 没接就判不了，记 unknown，不猜。
// 误报压法：只要回合还在 Running 里，多长都不算卡住（长工具调用）；重启后没结局的回合在建 Hub 时已被 SweepInterrupted 记成 interrupted，不会留着。
// 同一个回合不会重复计：状态是“现在卡着几个”，回合有了结局就从数里掉出去。
// 查询窗口收窄到最近 7 天，turns 上没有 ended_at 索引也不加（规格第八节）。
type turnStuck struct{ stuckAfter time.Duration }

func (turnStuck) ID() string   { return "turn.stuck" }
func (turnStuck) Name() string { return "回合卡住（没结局、不在跑、不在等审批）" }

func (c turnStuck) Eval(ctx context.Context, e *Env) Result {
	r := Result{Threshold: 1}
	if e.Trace == nil {
		r.State, r.Note = Unknown, "没有运行记录库的只读连接"
		return r
	}
	if e.Running == nil {
		r.State, r.Note = Unknown, "拿不到进程内正在跑的回合"
		return r
	}
	// 先取进程内在跑的再查库：查库时刚结束的回合 ended_at 已写，查不出来；刚开始的回合 started_at 在阈值以内，也查不出来
	running := map[string]bool{}
	for _, id := range e.Running() {
		running[id] = true
	}
	rows, err := e.Trace.QueryContext(ctx, `SELECT t.id, t.started_at, t.conversation FROM turns t
		WHERE t.ended_at IS NULL AND t.started_at < ? AND t.started_at >= ?
		AND NOT EXISTS (SELECT 1 FROM steps s WHERE s.turn_id = t.id AND s.status = 'waiting')
		ORDER BY t.started_at LIMIT 50`, e.Now.Add(-c.stuckAfter).UnixMilli(), e.Now.Add(-7*24*time.Hour).UnixMilli())
	if err != nil {
		r.State, r.Note = Unknown, "查询失败："+errClassOf(err)
		return r
	}
	defer rows.Close()
	var n int64
	var first map[string]string
	for rows.Next() {
		var id, conv string
		var started int64
		if err := rows.Scan(&id, &started, &conv); err != nil {
			r.State, r.Note = Unknown, "读取失败："+errClassOf(err)
			return r
		}
		if running[id] {
			continue
		}
		n++
		if first == nil {
			first = map[string]string{"turn": id, "started": time.UnixMilli(started).In(e.Now.Location()).Format(time.RFC3339), "conversation": conv}
		}
	}
	if err := rows.Err(); err != nil {
		r.State, r.Note = Unknown, "读取失败："+errClassOf(err)
		return r
	}
	r.Value = n
	if n >= 1 {
		r.State, r.Example = Bad, first
		r.Note = "超过 " + strconv.Itoa(int(c.stuckAfter/time.Minute)) + " 分钟没结局"
		return r
	}
	r.State = Ok
	return r
}
