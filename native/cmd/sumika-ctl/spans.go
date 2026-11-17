package main

import (
	"fmt"
	"io"
	"time"

	"sumika-native/internal/ctlproto"
)

// spanText 是环节的中文名。
var spanText = map[string]string{
	"turn.total":      "整轮",
	"turn.save":       "每步写库",
	"context.images":  "备图",
	"context.plugin":  "插件拼参考信息",
	"context.request": "拼请求",
	"model.queue":     "排模型名额",
	"model.wait":      "等首字节",
	"model.first":     "等首段内容",
	"model.stream":    "流式输出",
	"model.retry":     "等重试",
	"tool.prepare":    "工具准备（含审批）",
	"tool.queue":      "工具排队",
	"tool.exec":       "工具执行",
	"tool.save":       "存工具结果",
}

func spanLabel(kind, name string) string {
	label, ok := spanText[kind]
	if !ok {
		label = kind
	}
	if name != "" {
		label += " · " + name
	}
	return label
}

func spanMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d 毫秒", ms)
	}
	return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
}

// renderSpans 把 stats spans 写成给人看的中文。--json 不走这里。
func renderSpans(w, stderr io.Writer, r ctlproto.Reply) {
	var rep ctlproto.SpansReport
	if !decode(stderr, r, &rep) {
		return
	}
	if rep.Turn != "" {
		fmt.Fprintf(w, "这一轮的时间线（%s）\n", rep.Turn)
		var first int64
		for i, row := range rep.Rows {
			if i == 0 {
				first = row.StartedAt
			}
			fail := ""
			if row.Outcome != "ok" {
				fail = "  失败"
			}
			fmt.Fprintf(w, "  +%-10s %-10s %s%s\n", spanMs(row.StartedAt-first), spanMs(row.Ms), spanLabel(row.Kind, row.Name), fail)
		}
		if rep.Truncated {
			fmt.Fprintf(w, "  只列前 %d 行\n", ctlproto.SpansMaxRows)
		}
		return
	}
	if len(rep.Stats) == 0 && rep.Range.FromMS == 0 {
		fmt.Fprintln(w, "还没有记录。")
		return
	}
	fmt.Fprintf(w, "耗时（%s，%d 轮，按合计从大到小）\n", rangeText(rep.Range), rep.Turns)
	if len(rep.Stats) == 0 {
		fmt.Fprintln(w, "这段时间没有记录。想看更长的：加 --days 30 或 --all。")
		return
	}
	for _, s := range rep.Stats {
		fail := ""
		if s.Failed > 0 {
			fail = fmt.Sprintf("，失败 %d", s.Failed)
		}
		fmt.Fprintf(w, "  %s：%d 次，合计 %s，p50 %s，p95 %s，最长 %s%s\n", spanLabel(s.Kind, s.Name), s.Count,
			(time.Duration(s.TotalMs) * time.Millisecond).Round(time.Second), spanMs(s.P50Ms), spanMs(s.P95Ms), spanMs(s.MaxMs), fail)
	}
}
