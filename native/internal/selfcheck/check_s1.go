package selfcheck

import (
	"context"
	"strconv"
	"time"

	"sumika-native/internal/filelog"
)

// QuietFor 是静默点要求的最短安静时间：距上一条交来的消息（实时或补录）超过这么久，才拿守恒差额判定
const QuietFor = 5 * time.Second

// intakeUnaccounted 是 S1 intake.unaccounted：收到的消息没有去向。
//
// 两条守恒式（每个出口恰好记一个桶，计数器在 counters.go 里）：
//
//	A（适配器）：收到的帧 = 解码失败 + 不是消息 + 交给入库一站
//	B（入库一站）：交来的消息 = 没收下 + 关了丢弃 + 补录被取消 + 准入丢弃 + 处理时 panic + 入库失败 + 重复 + 入库
//
// 规格第三节只写了一条，把补录、没收下、panic、入库失败这些出口漏了：补录不经过收帧点会让入库多于收到，入库失败会让 S1、S2 同时红。
// 所以拆成两条。再补第三条把 A、B 接起来，不然适配器交出去以后、到入库一站收下之前丢的会两边都看不见：
//
//	C（交接）：适配器交给入库的帧 = 接收端丢掉（号对不上，有意的） + 实时交来的消息（Intake.Inbound；补录不走这里）
//	C 只在适配器数帧时才有意义（收到过帧）：不数帧的适配器（测试用的假适配器）不判这一条。
//
// 差额 = |A 差| + |B 差| + |C 差|，三条都要在静默点判（在途的消息会让差额暂时不为零）。
// 静默点 = 距上一条交来的消息超过 QuietFor，并且入库队列里没有排着、没做完的活（Env.Pending）：只看时间不够，
// 拦截链、闸门在会话队列里同步跑，队列被它卡住时，后面排着的消息已经算“交来”、还没有结局，不是丢了。
// 代价：队列一直卡着不动时，S1 一直不判（那是另一类故障，卡住的回合归 S4 管，不该让 S1 报成“消息没有去向”）。
// 判定：差额连续两次评估（静默点上）都不为零才报；因为计数是进程累计，丢了的那条不会自己补回来，所以以“基线”为参照，
// 只报基线以后新出现的：进程第一次评估时基线是 0（计数器随进程从 0 开始，启动后这段时间里真丢的也要报），
// 过了零点以后重建基线为那一刻的差额，过了零点自然恢复（和 S2 同一个口径）；盲区：零点那次评估把 23:5x 到零点之间已有的差额一并吸收了，
// 要报这段时间里的差额，得在零点前连续两次静默点上已经报过（报过的记在状态文件里，到零点才恢复）。
// 报出去的差额记进状态文件（键 s1.bad.<日期>）：换 exe、崩溃重启后计数器归零，差额也归零，不记下来的话当天就翻回 ok，还会写一条假的“自检恢复”。
type intakeUnaccounted struct {
	day       string
	baseA     int64
	baseB     int64
	baseC     int64
	baselined bool
	pending   bool // 上一次静默点上的差额也不为零
}

func (*intakeUnaccounted) ID() string   { return "intake.unaccounted" }
func (*intakeUnaccounted) Name() string { return "收到的消息没有去向" }

func (c *intakeUnaccounted) Eval(_ context.Context, e *Env) Result {
	r := Result{Threshold: 1}
	if e.Live == nil {
		r.State, r.Note = Unknown, "没有计数器"
		return r
	}
	last := e.Live.LastSubmit() // 最近一次交来消息的时间；心跳等非消息帧不算
	if !last.IsZero() && e.Now.Sub(last) <= QuietFor {
		r.State = Skip // 不在静默点：在途的消息会让差额暂时不为零，不判
		return r
	}
	if e.Pending != nil && e.Pending() > 0 {
		r.State = Skip // 入库队列里还有排着、没做完的活：它们已经算交来、还没有结局
		return r
	}
	l := e.Live
	a := l.Get(FrameReceived) - (l.Get(FrameDecodeFailed) + l.Get(FrameNonMessage) + l.Get(FrameToIntake))
	b := l.Get(IntakeSubmitted) - (l.Get(IntakeRejected) + l.Get(IntakeClosed) + l.Get(IntakeCancelled) + l.Get(IntakeAdmitDropped) +
		l.Get(IntakePanicked) + l.Get(IntakeStoreFailed) + l.Get(IntakeDuplicate) + l.Get(IntakeStored))
	var cc int64 // 只有适配器数帧时才判 C
	if l.Get(FrameReceived) > 0 {
		cc = l.Get(FrameToIntake) - (l.Get(FrameSinkDropped) + l.Get(IntakeLive))
	}
	day := e.Now.Format(filelog.DayLayout)
	switch {
	case !c.baselined:
		// 进程第一次评估：计数器随进程从 0 开始，基线就是 0，启动后这段时间里真丢的也要报
		c.day, c.baselined, c.pending = day, true, false
	case day != c.day:
		// 过了零点：这一刻的差额当基线（已有的差额不再报，只报以后新出现的）
		c.day, c.baseA, c.baseB, c.baseC, c.pending = day, a, b, cc, false
	}
	da, db, dc := a-c.baseA, b-c.baseB, cc-c.baseC
	v := abs64(da) + abs64(db) + abs64(dc)
	key := "s1.bad." + day
	reported, _ := e.KV.Get(key)
	if v == 0 {
		c.pending = false
		if reported > 0 {
			// 当天已经报过（重启前），计数器归零不代表丢的那条回来了：保持 bad 到零点
			r.State, r.Value, r.Note = Bad, reported, "当天已报过，重启后从状态文件续上"
			return r
		}
		r.State = Ok
		return r
	}
	if !c.pending {
		c.pending = true // 第一次不为零：等下一次静默点再看（在途的消息过一会儿就平了）
		r.State = Skip
		return r
	}
	r.State, r.Value = Bad, max(v, reported)
	e.KV.Set(key, r.Value)
	r.Example = map[string]string{"frames": strconv.FormatInt(da, 10), "intake": strconv.FormatInt(db, 10), "handoff": strconv.FormatInt(dc, 10)}
	return r
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
