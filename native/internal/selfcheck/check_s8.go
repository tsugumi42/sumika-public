package selfcheck

import (
	"context"
	"sort"
	"strconv"
	"time"
)

// eventsSeqGap 是 S8 events.seq_gap：聊天库 events.seq 出现新的断号（消息丢了、被删了）。
//
// 和规格字面的差别：events.seq 是每个平台聊天库里全局的 INTEGER PRIMARY KEY AUTOINCREMENT（migrations/chat/0001_baseline.sql），
// 不是“每个会话各自从 1 数”，同一个库里有多个会话时，单个会话的 seq 本来就不连续。所以断号按“平台库”判：
// 水位之后的行，应该正好是 水位+1 … 最大 seq 每个号一行。事务回滚不会留洞（AUTOINCREMENT 的计数在同一个事务里，测试 TestRollbackLeavesNoGap 钉住）；
// 库里没有任何地方删 events，所以一个洞就是异常。
//
// 增量：每个平台库在状态文件（state.json）里记一个水位（已查过的最大 seq）；评估只看水位以后的行（按主键，不随表增大）。
// 第一次（没有水位：新装、状态文件没有或读坏了、新加的平台）做历史基线：数出已有的历史缺口（max − count），写进 baseline.seqGaps.<平台>，不报；
// 水位定在当时的最大 seq。之后只报水位以后新出现的缺口，当天出现过就一直是 bad，到零点自然恢复（当天的新缺口数记在状态文件里，重启不丢）。
type eventsSeqGap struct {
	example map[string]string // 当天最近一次发现新断号时的位置；之后几轮没有新的，状态还是 bad，例子要留着
	exDay   string
}

func (*eventsSeqGap) ID() string   { return "events.seq_gap" }
func (*eventsSeqGap) Name() string { return "聊天库序号出现新断号" }

// Timeout：第一次做历史基线要数一遍表，允许 5 秒；之后的增量很快
func (*eventsSeqGap) Timeout() time.Duration { return 5 * time.Second }

func (c *eventsSeqGap) Eval(ctx context.Context, e *Env) Result {
	r := Result{Threshold: 1}
	if e.Events == nil {
		r.State, r.Note = Unknown, "没有聊天库的只读连接"
		return r
	}
	dbs := e.Events()
	if len(dbs) == 0 {
		r.State, r.Note = Unknown, "没有聊天库的只读连接"
		return r
	}
	names := make([]string, 0, len(dbs))
	for n := range dbs {
		names = append(names, n)
	}
	sort.Strings(names)
	day := e.Now.Format("2006-01-02")
	todayKey := "newgaps." + day
	var firstBad map[string]string
	var totalBase int64
	for _, name := range names {
		wmKey := "events." + name + ".watermark"
		wm, haveWM := e.KV.Get(wmKey)
		var count, maxSeq int64
		var err error
		if !haveWM {
			count, _, maxSeq, err = seqRange(ctx, dbs[name], 0)
			if err != nil {
				r.State, r.Note = Unknown, "查询失败："+errClassOf(err)
				return r
			}
			// 历史基线：max − count 个号在历史上就缺着（早期手工修过、导入没带），记下来，不报
			e.KV.Set(baselinePre+"seqGaps."+name, maxSeq-count)
			e.KV.Set(wmKey, maxSeq)
			continue
		}
		count, _, maxSeq, err = seqRange(ctx, dbs[name], wm)
		if err != nil {
			r.State, r.Note = Unknown, "查询失败："+errClassOf(err)
			return r
		}
		if count == 0 {
			continue
		}
		if gap := (maxSeq - wm) - count; gap > 0 {
			cur, _ := e.KV.Get(todayKey)
			e.KV.Set(todayKey, cur+gap)
			if firstBad == nil {
				firstBad = map[string]string{"platform": name, "afterSeq": strconv.FormatInt(wm, 10), "missing": strconv.FormatInt(gap, 10)}
			}
		}
		e.KV.Set(wmKey, maxSeq)
	}
	for _, name := range names {
		v, _ := e.KV.Get(baselinePre + "seqGaps." + name)
		totalBase += v
	}
	e.KV.Set(baselinePre+"seqGapsBefore", totalBase)
	n, _ := e.KV.Get(todayKey)
	r.Value = n
	if firstBad != nil {
		c.example, c.exDay = firstBad, day
	}
	if n >= 1 {
		r.State = Bad
		if c.exDay == day {
			r.Example = c.example
		}
		return r
	}
	r.State = Ok
	return r
}

// seqRange 返回 seq > after 的行数、最小和最大 seq（没有行是 0、0、0）。after 为 0 就是整张表（历史基线）
func seqRange(ctx context.Context, q Querier, after int64) (count, minSeq, maxSeq int64, err error) {
	rows, err := q.QueryContext(ctx, `SELECT COUNT(*), COALESCE(MIN(seq), 0), COALESCE(MAX(seq), 0) FROM events WHERE seq > ?`, after)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	if rows.Next() {
		err = rows.Scan(&count, &minSeq, &maxSeq)
	}
	if err == nil {
		err = rows.Err()
	}
	return
}
