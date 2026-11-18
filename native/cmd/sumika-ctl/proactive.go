package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"sumika-native/internal/ctlproto"
)

// proactiveReason 是日志原因码的中文说明。不认识的码原样写出（加引号，不当成我们的话）
var proactiveReason = map[string]string{
	"due":                "到点，本该醒（第 1 步还没接模型，只记日志）",
	"due-asleep-active":  "到点，在睡觉时段内但他在活跃，照常放行",
	"snoozed":            "他说过别烦，还没到时间",
	"daily-cap":          "今天已经找满了",
	"already-open":       "已经有一条线开着，没到续醒时间",
	"owner-chatting":     "他正在聊，先不打扰",
	"asleep-no-activity": "睡觉时段内，他没有活跃迹象",
	"cold-streak":        "连续几次他都没理，今天不再找",
	"missed-offline":     "到点时程序没开着，没醒到（不补跑）",
	"error":              "导演自己出错",
}

var proactiveKind = map[string]string{"wake": "醒", "followup": "续醒", "skip": "没醒", "close": "收线"}

// renderProactive 写 proactive log：先是各角色下次什么时候醒，再是最近的日志行（新的在前）。
// 备注是库里来的文字，一律加引号。--json 不走这里
func renderProactive(w, stderr io.Writer, r ctlproto.Reply) {
	var l ctlproto.ProactiveLog
	if !decode(stderr, r, &l) {
		return
	}
	if len(l.States) == 0 && len(l.Rows) == 0 {
		fmt.Fprintln(w, "（主动联系还没有任何记录：配置里 proactive.enabled 是关着的，或者程序开着以后还没排过第一次）")
		return
	}
	for _, s := range l.States {
		line := fmt.Sprintf("%s  下次醒 %s  上次醒 %s  系数 %.2f", ctlproto.Clean(s.Character), clock(s.NextWake), clock(s.LastWake), s.Factor)
		if s.ColdStreak > 0 {
			line += fmt.Sprintf("  连冷 %d 次", s.ColdStreak)
		}
		if s.QuietUntil > time.Now().UnixMilli() {
			line += "  别烦到 " + clock(s.QuietUntil)
		}
		fmt.Fprintln(w, line)
	}
	if len(l.Rows) == 0 {
		return
	}
	fmt.Fprintln(w, "最近的日志（新的在前）：")
	for _, row := range l.Rows {
		kind := proactiveKind[row.Kind]
		if kind == "" {
			kind = strconvQuote(ctlproto.Clean(row.Kind))
		}
		reason := proactiveReason[row.Reason]
		if reason == "" {
			reason = strconvQuote(ctlproto.Clean(row.Reason))
		}
		line := fmt.Sprintf("%s  %s  %s  %s", clock(row.At), ctlproto.Clean(row.Character), kind, reason)
		if row.Count > 1 {
			line += fmt.Sprintf("（共 %d 次）", row.Count)
		}
		if row.NextAt > 0 {
			line += "  下次 " + clock(row.NextAt)
		}
		if row.Thread != "" {
			line += "  线 " + strconvQuote(ctlproto.Clean(row.Thread))
		}
		if row.Note != "" {
			line += "  备注 " + strconvQuote(ctlproto.Clean(row.Note))
		}
		fmt.Fprintln(w, line)
		if in := inputsLine(row.Inputs); in != "" {
			fmt.Fprintln(w, "    "+in)
		}
	}
}

// inputsLine 把当时的快照写成一行；读不懂就不写（快照只有数字和布尔，不是文字）
func inputsLine(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in struct {
		SinceContactMin       int     `json:"sinceContactMin"`
		Heat                  string  `json:"heat"`
		Factor                float64 `json:"factor"`
		ThreadsToday          int     `json:"threadsToday"`
		OpenThread            bool    `json:"openThread"`
		InSleep               bool    `json:"inSleep"`
		OwnerLastActiveAgoMin int     `json:"ownerLastActiveAgoMin"`
		LocalLastInputAgoMin  int     `json:"localLastInputAgoMin"`
		WindowForeground      bool    `json:"windowForeground"`
		MissedMin             int     `json:"missedMin"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	ago := func(m int) string {
		if m < 0 {
			return "不知道"
		}
		return fmt.Sprintf("%d 分钟前", m)
	}
	yes := func(b bool) string {
		if b {
			return "是"
		}
		return "否"
	}
	s := fmt.Sprintf("当时：距上次找他 %s，系数 %.2f，今天已找 %d 次，睡觉时段内 %s，他最后发言 %s，窗口前台 %s",
		ago(in.SinceContactMin), in.Factor, in.ThreadsToday, yes(in.InSleep), ago(in.OwnerLastActiveAgoMin), yes(in.WindowForeground))
	if in.MissedMin > 0 {
		s += fmt.Sprintf("，晚了 %d 分钟", in.MissedMin)
	}
	return s
}
