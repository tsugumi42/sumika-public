package main

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

type growth struct {
	name                    string
	baseline, last, percent float64
	pass                    bool
	rule                    string
}
type summary struct {
	metrics        []growth
	window, warmup time.Duration
	complete, pass bool
}

func median(values []float64) float64 {
	slices.Sort(values)
	n := len(values)
	if n%2 == 1 {
		return values[n/2]
	}
	return (values[n/2-1] + values[n/2]) / 2
}
func percent(base, last float64) float64 {
	if base == 0 {
		if last == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return (last - base) / base * 100
}
func summarize(rows []sample, elapsed time.Duration) (summary, error) {
	if len(rows) < 2 || elapsed <= 0 {
		return summary{}, errors.New("至少需要两个采样点和正的时长")
	}
	window := min(time.Hour, elapsed/2)
	// 刚启动的第一小时内存还在往上爬（2026-10-31 那次 72 小时：第 1 小时均值 36 MB，之后一直 40～41 MB），
	// 够长的运行跳过它，拿第二小时当基线（ADR-219）；短跑没地方跳，照旧用前后半程。
	var warmup time.Duration
	if elapsed >= 3*time.Hour {
		warmup = time.Hour
	}
	result := summary{window: window, warmup: warmup, complete: elapsed >= 72*time.Hour, pass: true}
	metrics := []struct {
		name string
		get  func(sample) uint64
		rule string
	}{
		{"WorkingSet", func(s sample) uint64 { return s.workingSet }, "<10%"},
		{"PrivateUsage", func(s sample) uint64 { return s.private }, "<10%"},
		{"HeapInuse（GC 前，诊断）", func(s sample) uint64 { return s.heap }, "诊断"},
		{"HeapInuse（GC 后，诊断）", func(s sample) uint64 { return s.heapGC }, "诊断"},
		{"协程", func(s sample) uint64 { return s.goroutines }, "不涨"},
		{"句柄（规格没设线）", func(s sample) uint64 { return s.handles }, "诊断：不涨"},
	}
	for _, metric := range metrics {
		var first, last []float64
		for _, row := range rows {
			if row.elapsed >= warmup && row.elapsed < warmup+window {
				first = append(first, float64(metric.get(row)))
			}
			if row.elapsed >= elapsed-window {
				last = append(last, float64(metric.get(row)))
			}
		}
		if len(first) == 0 || len(last) == 0 {
			return summary{}, errors.New("首尾窗口没有采样点，请缩短 interval")
		}
		base, end := median(first), median(last)
		g := growth{name: metric.name, baseline: base, last: end, percent: percent(base, end), pass: true, rule: metric.rule}
		switch metric.rule {
		case "<10%":
			g.pass = g.percent < 10
		case "不涨", "诊断：不涨":
			g.pass = end <= base
		}
		if metric.rule == "<10%" || metric.rule == "不涨" {
			result.pass = result.pass && g.pass
		}
		result.metrics = append(result.metrics, g)
	}
	return result, nil
}
func (s summary) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "比较窗口：跳过开头 %s 预热，之后和结尾各取 %s 的中位数（以实际运行时长计）\n", s.warmup, s.window.Round(time.Millisecond))
	if s.window < time.Hour {
		fmt.Fprintln(&b, "短跑使用互不重叠的前后半程；三小时以上的运行跳过第一小时，用第二小时和最后一小时比。")
	}
	for _, m := range s.metrics {
		fmt.Fprintf(&b, "%s：%.1f → %.1f；涨幅 %+.3f%%；%s；过线=%t\n", m.name, m.baseline, m.last, m.percent, m.rule, m.pass)
	}
	fmt.Fprintf(&b, "本次窗口内存及协程过线=%t\n", s.pass)
	if s.complete {
		fmt.Fprintf(&b, "B6 72 小时挂机过线=%t\n", s.pass)
	} else {
		fmt.Fprintln(&b, "B6 72 小时挂机：未验收（运行不足 72 小时）")
	}
	return b.String()
}
