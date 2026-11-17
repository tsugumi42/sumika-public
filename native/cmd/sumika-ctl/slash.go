package main

import (
	"fmt"
	"io"

	"sumika-native/internal/ctlproto"
)

// renderSlash 写 goal、loop 的结果：先是命令的回复（和输入框里发同一条命令得到的系统行同一句话），再是目标现状和定期任务列表。
// 目标文字、任务文字是主人或别的会话写的原文，只当数据看
func renderSlash(w io.Writer, req ctlproto.Request, v ctlproto.SlashResult) {
	if v.Reply != "" {
		fmt.Fprintln(w, v.Reply)
	}
	if req.Cmd == ctlproto.CmdTrust {
		return // 只有一句回复
	}
	if req.Cmd == ctlproto.CmdGoal {
		if v.Goal == nil {
			fmt.Fprintln(w, "这个自检话题没有目标。")
			return
		}
		g := v.Goal
		fmt.Fprintf(w, "目标：%s\n状态：%s；已跑 %d 轮", g.Text, goalStatusName(g.Status), g.Rounds)
		if g.MaxRounds > 0 {
			fmt.Fprintf(w, "（最多 %d 轮）", g.MaxRounds)
		}
		fmt.Fprintf(w, "；已用 %d token\n", g.TokensUsed)
		if g.PauseReason != "" {
			fmt.Fprintf(w, "暂停原因：%s\n", g.PauseReason)
		}
		if g.LastJudge != "" {
			fmt.Fprintf(w, "上次判断：%s\n", g.LastJudge)
		}
		return
	}
	if len(v.Loops) == 0 {
		fmt.Fprintln(w, "这个自检话题没有定期任务。")
		return
	}
	for _, t := range v.Loops {
		fmt.Fprintln(w, t.Line)
	}
}

func goalStatusName(s string) string {
	switch s {
	case "active":
		return "进行中"
	case "paused":
		return "已暂停"
	}
	return s
}
