package main

import (
	"fmt"
	"io"
	"time"

	"sumika-native/internal/ctlproto"
)

func renderBackfill(w, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 {
		return
	}
	switch req.Args[0] {
	case "status":
		var v ctlproto.BackfillStatus
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(w, "统计范围：%s（%s）\n", v.Scope, backfillTime(v.AsOfMS))
		if v.LastAttempt == nil {
			fmt.Fprintln(w, "最近一次：尚未对齐")
		} else {
			renderBackfillJob(w, "最近一次", v.LastAttempt)
		}
		if v.CurrentJob != nil {
			renderBackfillJob(w, "当前任务", v.CurrentJob)
		}
		fmt.Fprintf(w, "剩余缺口：尚未补齐 %d 处，旧版已停自动 %d 处，排除当前范围 %d 处\n", v.RemainingOpen, v.RemainingAbandoned, v.Excluded)
		m := v.Media
		fmt.Fprintf(w, "媒体（%s）：排队 %d，运行 %d，已下载 %d，已过期 %d，失败 %d，等手动 %d，跳过 %d，未检查 %d\n", m.State, m.Queued, m.Running, m.Downloaded, m.Expired, m.Failed, m.AwaitingManual, m.Skipped, m.Unchecked)
		if !v.CanRun {
			fmt.Fprintf(w, "当前不能发起对齐：%s\n", v.ReasonCode)
		}
		if v.Discovery == "unsupported" {
			fmt.Fprintln(w, "新会话发现未支持。")
		}
	case "gaps":
		var v ctlproto.BackfillGaps
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(w, "统计范围：%s（%s）\n", v.Scope, backfillTime(v.AsOfMS))
		if len(v.Items) == 0 {
			fmt.Fprintln(w, "（当前页没有缺口）")
		}
		for _, g := range v.Items {
			retry := "当前不能重试"
			if g.CanRetry {
				retry = "可重试"
			}
			if g.RetryAfterMS != 0 {
				retry = "最早重试 " + backfillTime(g.RetryAfterMS)
			}
			fmt.Fprintf(w, "句柄 %s  %s  %s 至 %s  %s / %s  %s\n", g.GapHandle, g.MaskedConversation, backfillTime(g.FromMS), backfillTime(g.ToMS), g.State, g.Reason, retry)
			if g.JobID != "" {
				fmt.Fprintf(w, "  任务 %s\n", g.JobID)
			}
		}
		if v.HasMore {
			fmt.Fprintf(w, "还有更多：backfill gaps --cursor %s\n", v.NextCursor)
		}
	default:
		var v ctlproto.BackfillAccepted
		if decode(stderr, r, &v) {
			fmt.Fprintf(w, "任务已接受：%s（%s，%s）；用 backfill status 查看进度。\n", v.JobID, v.Scope, v.State)
		}
	}
}
func renderBackfillJob(w io.Writer, label string, j *ctlproto.BackfillJob) {
	scope := "账号对齐"
	if j.Scope == "gap" {
		scope = "单段补录"
	}
	at := j.StartedMS
	if j.EndedMS != 0 {
		at = j.EndedMS
	}
	fmt.Fprintf(w, "%s：%s · %s · %s · 已查 %d 页，补回 %d 条，去重 %d 条（%s）\n", label, scope, j.State, backfillTime(at), j.Pages, j.Inserted, j.Duplicates, j.ID)
	if !j.CompleteCoverage {
		fmt.Fprintln(w, "  覆盖尚未确认完整。")
	}
	if j.ReasonCode != "" {
		fmt.Fprintf(w, "  原因：%s\n", j.ReasonCode)
	}
}
func backfillTime(ms int64) string {
	if ms == 0 {
		return "未记录"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}
