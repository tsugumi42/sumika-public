package main

import (
	"fmt"
	"io"
	"strings"

	"sumika-native/internal/ctlproto"
)

// rebuildLine 是一行汇总的中文说法：几次重建、各原因、平均和最大丢几条、有摘要覆盖几次
func rebuildLine(r ctlproto.RebuildRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "重建 %s 次", num(r.Rebuilds))
	if r.Counted > 0 {
		fmt.Fprintf(&b, "；平均带上 %.1f 条、丢 %.1f 条，最多丢 %s 条", r.KeptAvg, r.DroppedAvg, num(r.DroppedMax))
	}
	if r.Covered > 0 {
		fmt.Fprintf(&b, "；%s 次丢掉的有摘要覆盖", num(r.Covered))
	} else if r.Counted > 0 {
		b.WriteString("；没有摘要覆盖")
	}
	if r.NotCounted > 0 {
		fmt.Fprintf(&b, "；%s 次没数出丢了几条（不算进平均）", num(r.NotCounted))
	}
	if r.Untraced > 0 {
		fmt.Fprintf(&b, "；%s 次是没记原因的旧记录", num(r.Untraced))
	}
	return b.String()
}

func rebuildReasons(r ctlproto.RebuildRow) string {
	parts := make([]string, 0, len(r.Reasons))
	for _, c := range r.Reasons {
		parts = append(parts, fmt.Sprintf("%s %s 次", ctlproto.RebuildReasonLabel(c.Reason), num(c.Count)))
	}
	return strings.Join(parts, "，")
}

// renderRebuild 把 stats rebuild 写成给人看的中文。--json 不走这里。
func renderRebuild(w, stderr io.Writer, r ctlproto.Reply) {
	var rep ctlproto.RebuildReport
	if !decode(stderr, r, &rep) {
		return
	}
	fmt.Fprintf(w, "重建留痕（%s）\n", rangeText(rep.Range))
	if len(rep.Places) == 0 {
		fmt.Fprintln(w, "这段时间没有重建记录。想看更长的：加 --days 30 或 --all。")
		return
	}
	fmt.Fprintln(w, "按场合合计")
	for _, p := range rep.Places {
		fmt.Fprintf(w, "  %s  %s\n", p.Place, rebuildLine(p))
		if s := rebuildReasons(p); s != "" {
			fmt.Fprintf(w, "    原因：%s\n", s)
		}
	}
	fmt.Fprintln(w, "按天")
	date := ""
	for _, d := range rep.Rows {
		if d.Date != date {
			date = d.Date
			fmt.Fprintf(w, "  %s\n", date)
		}
		fmt.Fprintf(w, "    %s  %s\n", d.Place, rebuildLine(d))
		if s := rebuildReasons(d); s != "" {
			fmt.Fprintf(w, "      原因：%s\n", s)
		}
	}
}
