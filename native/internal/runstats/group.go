package runstats

import (
	"fmt"
	"sort"
	"time"

	"sumika-native/internal/ctlproto"
)

// acc 是一组原始合计（SQL 一行或几行加起来）
type acc struct {
	runs, failed, noUsage           int64
	uncached, cacheRead, cacheWrite int64
	output, reasoning, okRuns, okMs int64
}

func (a *acc) plus(b acc) {
	a.runs += b.runs
	a.failed += b.failed
	a.noUsage += b.noUsage
	a.uncached += b.uncached
	a.cacheRead += b.cacheRead
	a.cacheWrite += b.cacheWrite
	a.output += b.output
	a.reasoning += b.reasoning
	a.okRuns += b.okRuns
	a.okMs += b.okMs
}

// stats 把合计变成对外的结构：总输入 = 未缓存 + 缓存读 + 缓存写；命中率 = 缓存读 / 总输入
func (a acc) stats() ctlproto.UsageStats {
	in := a.uncached + a.cacheRead + a.cacheWrite
	s := ctlproto.UsageStats{
		Runs: a.runs, Failed: a.failed, NoUsage: a.noUsage,
		UsageTokens: ctlproto.UsageTokens{Input: in, Uncached: a.uncached, CacheRead: a.cacheRead, CacheWrite: a.cacheWrite, Output: a.output, Reasoning: a.reasoning},
		OKRuns:      a.okRuns, OKMs: a.okMs,
	}
	if a.okRuns > 0 {
		s.AvgMs = a.okMs / a.okRuns
	}
	if in > 0 {
		s.Hit = float64(a.cacheRead) / float64(in)
	}
	return s
}

// maxUnknownRows 是认不出的值最多单列几行；再多的合成一行（原文写"其余 N 种"），免得一堆乱值撑爆页面
const maxUnknownRows = 50

type taskAcc struct {
	acc
	raw    string
	models map[string]*acc
}

type grouper struct {
	tasks   map[string]*taskAcc // 键：类别键；unknown 的键是 "unknown\x00" + 原文
	models  map[string]*acc
	days    map[int64]*acc
	total   acc
	unknown []string
}

func newGrouper() *grouper {
	return &grouper{tasks: map[string]*taskAcc{}, models: map[string]*acc{}, days: map[int64]*acc{}}
}

func (g *grouper) add(purpose, stage, model string, day int64, a acc) {
	key := TaskOf(purpose, stage)
	raw := ""
	tk := key
	if key == ctlproto.TaskUnknown {
		raw = rawOf(purpose, stage)
		tk = key + "\x00" + raw
	}
	t := g.tasks[tk]
	if t == nil {
		t = &taskAcc{raw: raw, models: map[string]*acc{}}
		g.tasks[tk] = t
		if key == ctlproto.TaskUnknown {
			g.unknown = append(g.unknown, tk)
		}
	}
	t.plus(a)
	bump(t.models, model, a)
	bump(g.models, model, a)
	d := g.days[day]
	if d == nil {
		d = &acc{}
		g.days[day] = d
	}
	d.plus(a)
	g.total.plus(a)
}

func bump(m map[string]*acc, k string, a acc) {
	x := m[k]
	if x == nil {
		x = &acc{}
		m[k] = x
	}
	x.plus(a)
}

func modelRows(m map[string]*acc) []ctlproto.UsageModel {
	out := make([]ctlproto.UsageModel, 0, len(m))
	for name, a := range m {
		out = append(out, ctlproto.UsageModel{Model: ctlproto.Clean(name), UsageStats: a.stats()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Input != out[j].Input {
			return out[i].Input > out[j].Input
		}
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func (g *grouper) report() ctlproto.UsageReport {
	rep := ctlproto.UsageReport{Totals: g.total.stats(), Models: modelRows(g.models)}
	for _, meta := range Tasks {
		row := ctlproto.UsageTask{Key: meta.Key, Label: meta.Label, Wired: meta.Wired}
		if t := g.tasks[meta.Key]; t != nil {
			row.UsageStats = t.stats()
			row.Models = modelRows(t.models)
		}
		rep.Tasks = append(rep.Tasks, row)
	}
	// 认不出的值各占一行，按次数从多到少；超过上限的合并成一行
	sort.Slice(g.unknown, func(i, j int) bool {
		a, b := g.tasks[g.unknown[i]], g.tasks[g.unknown[j]]
		if a.runs != b.runs {
			return a.runs > b.runs
		}
		return a.raw < b.raw
	})
	var rest *taskAcc
	for i, k := range g.unknown {
		t := g.tasks[k]
		if i >= maxUnknownRows {
			if rest == nil {
				rest = &taskAcc{models: map[string]*acc{}}
			}
			rest.plus(t.acc)
			for name, m := range t.models {
				bump(rest.models, name, *m)
			}
			continue
		}
		rep.Tasks = append(rep.Tasks, ctlproto.UsageTask{Key: ctlproto.TaskUnknown, Label: "未知", Wired: true, Raw: t.raw, UsageStats: t.stats(), Models: modelRows(t.models)})
	}
	if rest != nil {
		rep.Tasks = append(rep.Tasks, ctlproto.UsageTask{Key: ctlproto.TaskUnknown, Label: "未知", Wired: true, Raw: fmt.Sprintf("其余 %d 种", len(g.unknown)-maxUnknownRows), UsageStats: rest.stats(), Models: modelRows(rest.models)})
	}

	// 按天：SQL 里的天序号是 (started_at + 偏移) / 一天，所以序号乘一天就是"挪到 UTC 以后的那天零点"，
	// 取它的 UTC 日期就是本地日期，和时区无关地对得上 SQL 的分组
	idx := make([]int64, 0, len(g.days))
	for d := range g.days {
		idx = append(idx, d)
	}
	sort.Slice(idx, func(i, j int) bool { return idx[i] < idx[j] })
	weeks := map[string]*acc{}
	var weekOrder []string
	for _, d := range idx {
		day := time.UnixMilli(d * dayMS).UTC()
		rep.Days = append(rep.Days, ctlproto.UsageBucket{Date: day.Format("2006-01-02"), UsageStats: g.days[d].stats()})
		monday := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)).Format("2006-01-02")
		if weeks[monday] == nil {
			weekOrder = append(weekOrder, monday)
		}
		bump(weeks, monday, *g.days[d])
	}
	for _, monday := range weekOrder { // idx 升序，周一的日期也是升序
		rep.Weeks = append(rep.Weeks, ctlproto.UsageBucket{Date: monday, UsageStats: weeks[monday].stats()})
	}
	return rep
}
