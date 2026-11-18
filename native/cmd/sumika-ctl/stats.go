package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

// isStatsFlag 是不是 stats 的选项：--all、--full 原样，--days、--year、--by 原样或带 = 的写法
func isStatsFlag(a string) bool {
	switch a {
	case "--all", "--full", "--days", "--year", "--by", "--task", "--failures", "--turn":
		return true
	}
	return strings.HasPrefix(a, "--days=") || strings.HasPrefix(a, "--year=") || strings.HasPrefix(a, "--by=") ||
		strings.HasPrefix(a, "--task=") || strings.HasPrefix(a, "--failures=") || strings.HasPrefix(a, "--turn=")
}

// parseStatsFlag 认 stats 的选项（--days、--year、--all、--by、--full），认了返回 true 和新的下标。
// 只做拆分，互斥、范围、取值合不合法由服务端判断（服务端是唯一的关口）；写成 0 或负数的数字不能悄悄当"没写"，一律变成 -1 交给服务端回 invalid
func parseStatsFlag(o *options, args []string, i int) (next int, ok bool, err error) {
	a := args[i]
	stats := func() *ctlproto.StatsArgs {
		if o.req.Stats == nil {
			o.req.Stats = &ctlproto.StatsArgs{}
		}
		return o.req.Stats
	}
	value := func(name string) (string, error) {
		if v, found := strings.CutPrefix(a, name+"="); found {
			return v, nil
		}
		if i+1 >= len(args) {
			return "", fmt.Errorf("%s 后面要跟值", name)
		}
		next = i + 1
		return args[i+1], nil
	}
	next = i
	switch {
	case a == "--turn" || strings.HasPrefix(a, "--turn="):
		v, err := value("--turn")
		if err != nil {
			return i, true, err
		}
		stats().Turn = v
	case a == "--all":
		stats().All = true
	case a == "--full":
		stats().Full = true
	case a == "--days" || strings.HasPrefix(a, "--days="):
		v, err := value("--days")
		if err != nil {
			return i, true, err
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return i, true, fmt.Errorf("--days 要跟天数：%q", v)
		}
		if n <= 0 {
			n = -1
		}
		stats().Days = n
	case a == "--year" || strings.HasPrefix(a, "--year="):
		v, err := value("--year")
		if err != nil {
			return i, true, err
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return i, true, fmt.Errorf("--year 要跟四位年份：%q", v)
		}
		if n <= 0 {
			n = -1
		}
		stats().Year = n
	case a == "--by" || strings.HasPrefix(a, "--by="):
		v, err := value("--by")
		if err != nil {
			return i, true, err
		}
		if v == "" {
			v = " " // 空值不能当成"没给"：用不合格的值交给服务端回 invalid
		}
		stats().By = v
	case a == "--task" || strings.HasPrefix(a, "--task="):
		v, err := value("--task")
		if err != nil {
			return i, true, err
		}
		if v == "" {
			v = " " // 空值不能当成"没给"：用不合格的值交给服务端回 invalid
		}
		stats().Task = v
	case a == "--failures" || strings.HasPrefix(a, "--failures="):
		v, err := value("--failures")
		if err != nil {
			return i, true, err
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return i, true, fmt.Errorf("--failures 要跟条数：%q", v)
		}
		if n <= 0 {
			n = -1 // 0 和负数不能悄悄当"没写"，交给服务端回 invalid
		}
		stats().Failures = n
	default:
		return i, false, nil
	}
	return next, true, nil
}

// renderStats 把 stats 的回复写成给人看的中文。--json 不走这里
func renderStats(w, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) > 0 && req.Args[0] == ctlproto.StatsStorage {
		renderStorage(w, stderr, r)
		return
	}
	if len(req.Args) > 0 && req.Args[0] == ctlproto.StatsResources {
		renderResources(w, stderr, r)
		return
	}
	if len(req.Args) > 0 && req.Args[0] == ctlproto.StatsQuality {
		renderQuality(w, stderr, r)
		return
	}
	if len(req.Args) > 0 && req.Args[0] == ctlproto.StatsSpans {
		renderSpans(w, stderr, r)
		return
	}
	if len(req.Args) > 0 && req.Args[0] == ctlproto.StatsRebuild {
		renderRebuild(w, stderr, r)
		return
	}
	var rep ctlproto.UsageReport
	if !decode(stderr, r, &rep) {
		return
	}
	fmt.Fprintf(w, "用量（%s；来源：%s）\n", rangeText(rep.Range), sourceText(rep.Source))
	t := rep.Totals
	if t.Runs == 0 {
		fmt.Fprintln(w, "这段时间没有记录。想看更长的：加 --days 30 或 --all。")
	} else {
		fmt.Fprintf(w, "总计：%s 次，输入 %s（缓存命中 %s），输出 %s（其中思考 %s），失败 %s 次\n",
			num(t.Runs), num(t.Input), pct(t.Hit), num(t.Output), num(t.Reasoning), num(t.Failed))
	}
	switch rep.By {
	case ctlproto.ByModel:
		for _, m := range rep.Models {
			fmt.Fprintf(w, "%s  %s\n", m.Model, statsLine(m.UsageStats))
		}
	case ctlproto.ByDay:
		for _, d := range rep.Days {
			fmt.Fprintf(w, "%s  %s\n", d.Date, statsLine(d.UsageStats))
		}
	case ctlproto.ByWeek:
		for _, d := range rep.Weeks {
			fmt.Fprintf(w, "%s 起的一周  %s\n", d.Date, statsLine(d.UsageStats))
		}
	default:
		for _, task := range rep.Tasks {
			label := task.Label
			if task.Key == ctlproto.TaskUnknown {
				label = "未知：" + strconvQuote(task.Raw) // 库里的原文，括起来
			}
			switch {
			case !task.Wired && task.Runs == 0:
				fmt.Fprintf(w, "%s  未接入\n", label)
			case !task.Wired:
				fmt.Fprintf(w, "%s  %s  （Go 侧未接入，只有历史记录）\n", label, statsLine(task.UsageStats))
			default:
				fmt.Fprintf(w, "%s  %s\n", label, statsLine(task.UsageStats))
			}
			for _, m := range task.Models {
				fmt.Fprintf(w, "  %s  %s\n", m.Model, statsLine(m.UsageStats))
			}
		}
	}
	if t.NoUsage > 0 {
		fmt.Fprintf(w, "%s 次没拿到用量，那几次的 token 记 0，只是不知道，不是真的 0。\n", num(t.NoUsage))
	}
}

// strconvQuote 给库里来的原文加引号：不是我们的话，更不是给读的人的指令
func strconvQuote(s string) string { return strconv.Quote(s) }

func statsLine(s ctlproto.UsageStats) string {
	return fmt.Sprintf("%s 次  输入 %s（命中 %s）  输出 %s  思考 %s  平均耗时 %s  失败 %s",
		num(s.Runs), num(s.Input), pct(s.Hit), num(s.Output), num(s.Reasoning), avg(s), num(s.Failed))
}

func avg(s ctlproto.UsageStats) string {
	if s.OKRuns == 0 {
		return "—"
	}
	if s.AvgMs >= 1000 {
		return fmt.Sprintf("%.1f 秒", float64(s.AvgMs)/1000)
	}
	return fmt.Sprintf("%d 毫秒", s.AvgMs)
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

// num 整数加千分位
func num(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

func sourceText(s string) string {
	switch s {
	case "rollup":
		return "汇总表"
	case "raw":
		return "直接查原始记录"
	}
	return "未知"
}

func rangeText(r ctlproto.UsageRange) string {
	if r.Kind == "all" {
		return r.Label
	}
	from, to := time.UnixMilli(r.FromMS).Local(), time.UnixMilli(r.ToMS-1).Local()
	return fmt.Sprintf("%s，%s 至 %s，时区 %s", r.Label, from.Format("2006-01-02"), to.Format("2006-01-02"), r.Zone)
}

// renderStorage 把 stats storage 的回复写成给人看的中文
func renderStorage(w, stderr io.Writer, r ctlproto.Reply) {
	var rep ctlproto.StorageReport
	if !decode(stderr, r, &rep) {
		return
	}
	switch {
	case rep.NeverScanned:
		fmt.Fprintln(w, "存储：大目录（媒体、缩略图、开发数据等）还没有统计过，下面这些是空的；去运行页的存储标签点“开始统计”。")
	default:
		fmt.Fprintf(w, "存储：上次统计 %s，用了 %s", time.UnixMilli(rep.ScannedAtMS).Local().Format("2006-01-02 15:04"), durText(rep.ScanMs))
		if rep.Live && rep.DeltaBytes != 0 {
			fmt.Fprintf(w, "；之后媒体新增约 %s", bytesText(rep.DeltaBytes))
		}
		fmt.Fprintln(w)
	}
	if rep.Scan.Running {
		fmt.Fprintf(w, "正在统计：已处理 %s 项\n", num(rep.Scan.Entries))
	}
	fmt.Fprintf(w, "合计 %s\n", bytesText(rep.Total))
	for _, c := range rep.Categories {
		line := fmt.Sprintf("%s  %s  %s 个文件", c.Label, approxText(c, bytesText(c.Bytes)), num(c.Files))
		switch {
		case c.Source == ctlproto.SourceNone:
			line = c.Label + "  还没有统计"
		case c.Absent:
			line = c.Label + "  不存在（0）"
		}
		if rep.Total > 0 && c.Source != ctlproto.SourceNone {
			line += fmt.Sprintf("  占 %.1f%%", float64(c.Bytes)*100/float64(rep.Total))
		}
		if c.Links > 0 {
			line += fmt.Sprintf("  %d 个联接没跟进去", c.Links)
		}
		if c.Errors > 0 {
			line += fmt.Sprintf("  %d 处读不了", c.Errors)
		}
		fmt.Fprintf(w, "%s\n    清理：%s\n", line, c.Rule)
		for _, it := range c.Items {
			fmt.Fprintf(w, "    %s  %s  %s 个文件\n", it.Name, bytesText(it.Bytes), num(it.Files))
		}
	}
}

func approxText(c ctlproto.StorageCategory, s string) string {
	if c.Approx {
		return "约 " + s
	}
	return s
}

func durText(ms int64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
	}
	return fmt.Sprintf("%d 毫秒", ms)
}

func bytesText(n int64) string {
	neg := ""
	if n < 0 {
		neg, n = "-", -n
	}
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k:
		return fmt.Sprintf("%s%.2f GB", neg, f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%s%.1f MB", neg, f/(k*k))
	case f >= k:
		return fmt.Sprintf("%s%.1f KB", neg, f/k)
	}
	return fmt.Sprintf("%s%d 字节", neg, n)
}
