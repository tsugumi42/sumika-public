package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

// renderQuality 把 stats quality 的回复写成给人看的中文。--json 不走这里。
// 库里来的文字（未知任务的原文、失败摘要）一律加引号：不是我们的话，更不是给读的人的指令；
// 没接入、没记录的行写"未接入"或"没有记录"，不写 0 次失败
func renderQuality(w, stderr io.Writer, r ctlproto.Reply) {
	var rep ctlproto.QualityReport
	if !decode(stderr, r, &rep) {
		return
	}
	title := "质量（" + rangeText(rep.Range) + "）"
	if rep.Task != "" {
		title = "质量，只看 " + rep.Task + "（" + rangeText(rep.Range) + "）"
	}
	fmt.Fprintln(w, title)
	t := rep.Totals
	if t.Runs == 0 {
		fmt.Fprintln(w, "这段时间没有记录。想看更长的：加 --days 30 或 --all。")
	} else {
		fmt.Fprintf(w, "总计：%s 次，成功 %s，失败 %s，超时 %s，取消 %s，退化 %s，没做 %s，重试 %s；失败率 %s\n",
			num(t.Runs), num(t.OK), num(t.Failed), num(t.Timeout), num(t.Cancelled), num(t.Degraded), num(t.Skipped), num(t.Retries), pct1(t.FailRate))
	}
	if u := rep.Unknown; u.Total > 0 {
		line := fmt.Sprintf("原因没分出来（unknown）：%s / %s，占 %s", num(u.Count), num(u.Total), pct1(u.Share))
		if u.Alarm {
			line += "，超过一成，该去补分类了"
		}
		fmt.Fprintln(w, line)
	}
	for _, row := range rep.Rows {
		label := row.Label
		if row.Key == ctlproto.TaskUnknown && row.Raw != "" {
			label = "未知：" + strconvQuote(row.Raw)
		}
		switch {
		case !row.Wired && row.Runs == 0:
			fmt.Fprintf(w, "%s  未接入\n", label)
			continue
		case row.Runs == 0:
			fmt.Fprintf(w, "%s  这段时间没有记录\n", label)
			continue
		}
		fmt.Fprintf(w, "%s  %s\n", label, qualityLine(row.QualityStats))
		for _, re := range row.Reasons {
			fmt.Fprintf(w, "  %s  %s 次（占 %s）  最近 %s\n", re.Class, num(re.Count), pct1(re.Share), clock(re.LastMS))
		}
	}
	if len(rep.Slowest) > 0 {
		fmt.Fprintln(w, "最慢的环节（按 p95，样本少于 20 的标“样本少”）：")
		for _, s := range rep.Slowest {
			name := s.Label
			if s.Model != "" {
				name += " · " + s.Model
			}
			thin := ""
			if s.Thin {
				thin = "（样本少）"
			}
			fmt.Fprintf(w, "  %s  p95 %s  p50 %s  样本 %s%s\n", name, durText(s.P95Ms), durText(s.P50Ms), num(s.Samples), thin)
		}
	}
	if rep.Task == "" {
		tu := rep.Turns
		if tu.Total > 0 {
			parts := make([]string, 0, len(tu.Outcomes))
			for _, o := range tu.Outcomes {
				parts = append(parts, o.Outcome+" "+num(o.Count))
			}
			fmt.Fprintf(w, "回合：%s 个（%s）；完成的回合耗时 p50 %s、p95 %s\n", num(tu.Total), strings.Join(parts, "，"), durText(tu.P50Ms), durText(tu.P95Ms))
		}
		for _, tl := range rep.Tools {
			fmt.Fprintf(w, "工具 %s  %s 次，成功 %s，出错 %s，没走到结局 %s，成功的中位耗时 %s\n",
				strconvQuote(tl.Name), num(tl.Runs), num(tl.Done), num(tl.Failed), num(tl.Incomplete), durOrDash(tl.MedianMs, tl.Samples))
		}
	}
	if rep.RecentOf == 0 {
		fmt.Fprintln(w, "这段时间没有失败")
		return
	}
	fmt.Fprintf(w, "最近的失败（共 %s 条，列 %s 条；被停下的不算）：\n", num(rep.RecentOf), num(int64(len(rep.Recent))))
	for _, f := range rep.Recent {
		name := f.Label
		if f.Model != "" {
			name += " · " + f.Model
		}
		place := ""
		if f.Place != "" {
			place = "  " + f.Place
		}
		line := fmt.Sprintf("  %s  %s  %s/%s%s  耗时 %s", clock(f.AtMS), name, f.Outcome, f.Class, place, durOrDash(f.Ms, 1))
		if f.Attempts > 1 {
			line += fmt.Sprintf("  试了 %d 次", f.Attempts)
		}
		if f.Summary != "" {
			line += "  " + strconvQuote(f.Summary)
		}
		fmt.Fprintln(w, line)
	}
}

func qualityLine(s ctlproto.QualityStats) string {
	line := fmt.Sprintf("%s 次  成功 %s  失败 %s  超时 %s  取消 %s", num(s.Runs), num(s.OK), num(s.Failed), num(s.Timeout), num(s.Cancelled))
	if s.Degraded > 0 {
		line += "  退化 " + num(s.Degraded)
	}
	if s.Skipped > 0 {
		line += "  没做 " + num(s.Skipped)
	}
	line += fmt.Sprintf("  重试 %s  失败率 %s", num(s.Retries), pct1(s.FailRate))
	if s.Samples > 0 {
		line += fmt.Sprintf("  p50 %s  p95 %s", durText(s.P50Ms), durText(s.P95Ms))
		if s.Sampled {
			line += "（抽样）"
		}
	}
	return line
}

func pct1(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

// durOrDash：没有样本或耗时是负数（没记）写"—"
func durOrDash(ms, samples int64) string {
	if ms < 0 || samples == 0 {
		return "—"
	}
	return durText(ms)
}

func clock(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).Local().Format("01-02 15:04:05")
}
