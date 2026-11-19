package main

// 输入框上方的目标行：常驻只有一行（目标前 40 字、第几轮、状态），右边一个展开钮，
// 点开看目标全文、轮数、预算和裁判最近一次的理由，再点收起（规格-目标和循环 第二节“界面”）。
// 展开的是同一份只读数据（api.LocalStatus.Goal）；改目标、继续、结束仍走 /goal 命令。
// 标准控件取自 winshell.Kit（IconButton、KeyValues、Note），不另拼。

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

func (a *app) wbGoalRow(c *ui.Context, kit *winshell.Kit, k api.ConversationKey, s api.LocalStatus) {
	g, p := kit.G, kit.P
	open := a.goalOpen[k.Conversation]
	line := s.GoalLine
	if s.GoalState == "paused" {
		line += "  /goal resume 继续" // 暂停了要主人动手，这句留在常驻行上
	}
	ui.Row(c.Key("wb-goal-row")).FillWidth().Shrink(0).Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
		ui.Text(c.Key("wb-goal-line"), line).FontSize(g.LabelSize).FixedLineHeight(g.LabelLine).TextColor(p.Muted).Grow(1).MinWidth(0).SingleLine()
		label, icon := "展开目标详情", chatui.Icon("down", false)
		if open {
			label, icon = "收起目标详情", chatui.Icon("up", false)
		}
		kit.IconButton(c, "wb-goal-toggle", label, icon, func() {
			if a.goalOpen == nil {
				a.goalOpen = map[string]bool{}
			}
			a.goalOpen[k.Conversation] = !open
		})
	})
	if !open {
		return
	}
	ui.Column(c.Key("wb-goal-detail")).FillWidth().Shrink(0).Gap(g.SpaceSmall).Padding(g.ControlInset).Radius(g.RadiusControl).Background(p.Bubble).Children(func() {
		kit.KeyValues(c, "wb-goal-facts", goalFacts(*s.Goal))
		kit.Note(c, "/goal edit 改目标，/goal clear 结束", false)
	})
}

// goalFacts 是展开后的几行“名字 + 值”：目标全文、状态、轮数、预算、裁判理由。预算为 0 写“不限”。
func goalFacts(gi api.GoalInfo) []winshell.KeyValue {
	state := "进行中"
	if gi.State == "paused" {
		state = "已暂停"
		if gi.PauseReason != "" {
			state += "：" + gi.PauseReason
		}
	}
	rounds := fmt.Sprintf("已跑 %d 轮，上限 %s", gi.Rounds, limitText(int64(gi.MaxRounds), " 轮"))
	tokens := fmt.Sprintf("已用 %s，上限 %s", thousands(gi.TokensUsed), limitText(gi.MaxTokens, ""))
	judge := "还没判过"
	if gi.LastAt != 0 {
		at := time.UnixMilli(gi.LastAt).Format("15:04")
		switch {
		case gi.LastFailed:
			judge = "裁判出错（" + at + "）：" + gi.LastReason
		case gi.LastAchieved:
			judge = "达成（" + at + "）：" + gi.LastReason
		default:
			judge = "没达成（" + at + "）：" + gi.LastReason
		}
	}
	return []winshell.KeyValue{{Key: "目标", Value: gi.Text}, {Key: "状态", Value: state}, {Key: "轮数", Value: rounds}, {Key: "token", Value: tokens}, {Key: "裁判", Value: judge}}
}

func limitText(n int64, unit string) string {
	if n <= 0 {
		return "不限"
	}
	return thousands(n) + unit
}

// thousands 写成带千分位的整数：12345 → 12,345。
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
