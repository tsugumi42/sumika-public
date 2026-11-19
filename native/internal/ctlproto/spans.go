package ctlproto

import "errors"

// StatsSpans 是 stats 的耗时子命令（规格-回合耗时时间线）。
const StatsSpans = "spans"

// SpansMaxRows 是一轮时间线最多列几行；上限内的按开始时间列，多的不列并在 Truncated 里说。
const SpansMaxRows = 500

// SpanStat 是一种环节的汇总：每种 kind 加 name 一行。
type SpanStat struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Count   int64  `json:"count"`
	Failed  int64  `json:"failed,omitempty"`
	TotalMs int64  `json:"totalMs"`
	P50Ms   int64  `json:"p50Ms"`
	P95Ms   int64  `json:"p95Ms"`
	MaxMs   int64  `json:"maxMs"`
}

// SpanRow 是时间线的一行。
type SpanRow struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	StartedAt int64  `json:"startedAt"`
	Ms        int64  `json:"ms"`
	Outcome   string `json:"outcome"`
}

// SpansReport 是 stats spans 的结果：写了 --turn 时给 Turn、Rows，否则给 Range、Stats（按合计耗时从大到小）。
type SpansReport struct {
	Range     UsageRange `json:"range"`
	Turns     int64      `json:"turns,omitempty"`
	Stats     []SpanStat `json:"stats,omitempty"`
	Turn      string     `json:"turn,omitempty"`
	Rows      []SpanRow  `json:"rows,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// ValidateSpans 检查 stats spans 的参数：只认 --days / --all 和 --turn，--turn 不和范围一起写。
func (a StatsArgs) ValidateSpans() error {
	if a.Year != 0 || a.By != "" || a.Full || a.Task != "" || a.Failures != 0 {
		return errors.New("stats spans 只接受 --days、--all、--turn")
	}
	if a.Turn != "" && (a.Days != 0 || a.All) {
		return errors.New("--turn 看一轮，不和 --days、--all 一起写")
	}
	return a.Validate()
}
