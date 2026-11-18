package goal

import (
	"fmt"
	"strings"

	"sumika-native/internal/slash"
)

// ParseCommand 返回的命令名。
const (
	CmdSet    = "set"    // /goal <目标>
	CmdStatus = "status" // /goal
	CmdClear  = "clear"  // /goal clear（别名 stop、off、cancel）
	CmdPause  = "pause"
	CmdResume = "resume"
	CmdEdit   = "edit" // /goal edit <新目标>：改目标原文
)

// Command 是 /goal 在斜杠命令表里的登记项。
var Command = slash.Command{
	Name:    "goal",
	Summary: "设一个目标，做到达成为止；/goal 看状态，edit 改目标，clear 结束，pause、resume 暂停和继续",
	Subs: []slash.Sub{
		{Name: "clear", Aliases: []string{"stop", "off", "cancel"}, NoArg: true, Summary: "结束目标"},
		{Name: "pause", NoArg: true, Summary: "暂停"},
		{Name: "resume", NoArg: true, Summary: "继续"},
		{Name: "edit", Summary: "改目标原文（保留轮数和预算）：/goal edit <新目标>"},
	},
}

// Register 把 /goal 登记进命令表。
func Register(r *slash.Registry) { r.Register(Command) }

// ParseCommand 认整条消息以 /goal 开头的写法。ok 为假表示不是 /goal（别的命令或普通文字）。
// 子命令后面还跟着别的字（如“/goal clear the cache”）就当目标原文。
func ParseCommand(text string) (cmd, arg string, ok bool) {
	name, rest, isCmd := slash.Split(text)
	if !isCmd || name != "goal" {
		return "", "", false
	}
	first, tail := rest, ""
	if i := strings.IndexAny(rest, " \t\r\n"); i >= 0 {
		first, tail = rest[:i], strings.TrimSpace(rest[i:])
	}
	if first == "" {
		return CmdStatus, "", true
	}
	if strings.EqualFold(first, "edit") {
		return CmdEdit, tail, true
	}
	for _, s := range Command.Subs {
		hit := strings.EqualFold(first, s.Name)
		for _, a := range s.Aliases {
			hit = hit || strings.EqualFold(first, a)
		}
		if hit && tail == "" {
			return s.Name, "", true
		}
	}
	return CmdSet, rest, true
}

// StatusLine 是给界面用的一行状态。
func StatusLine(g Goal) string {
	text := oneLine(g.Text)
	if r := []rune(text); len(r) > 40 {
		text = string(r[:40]) + "…"
	}
	switch g.Status {
	case StatusActive:
		return fmt.Sprintf("目标：%s · 第 %d 轮 · 进行中", text, g.Rounds+1)
	case StatusPaused:
		reason := ""
		if g.PauseReason != "" {
			reason = "：" + g.PauseReason
		}
		return fmt.Sprintf("目标：%s · 已跑 %d 轮 · 已暂停%s", text, g.Rounds, reason)
	case StatusDone:
		return fmt.Sprintf("目标：%s · 已跑 %d 轮 · 已达成", text, g.Rounds)
	default:
		return fmt.Sprintf("目标：%s · 已清除", text)
	}
}
