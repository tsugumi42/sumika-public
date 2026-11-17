package main

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// watchLocalStatus 订阅各本地私聊会话的目标、定期任务和自动轮状态，整份换掉。
func (a *app) watchLocalStatus(ctx context.Context, r api.LocalStatusReader, gen uint64) {
	sub, err := r.WatchLocalStatus(ctx)
	if err != nil {
		return
	}
	defer sub.Close()
	for snap := range sub.Updates() {
		by := make(map[string]api.LocalStatus, len(snap.Value))
		for _, s := range snap.Value {
			by[s.Key.Conversation] = s
		}
		a.post(func() {
			if a.srcGen == gen && ctx.Err() == nil {
				a.localStatus = by
			}
		})
	}
}

// localStatusLines 在输入框上方写一行目标状态和一行定期任务（没有就不画）。命令回复等系统行是会话里的消息。
func (a *app) localStatusLines(c *ui.Context, k api.ConversationKey, s api.LocalStatus) {
	if s.GoalLine != "" {
		hint := "  /goal clear 结束"
		if s.GoalState == "paused" {
			hint = "  /goal resume 继续，/goal clear 结束"
		}
		localCaption(c.Key("goal-line"), s.GoalLine+hint)
	}
	if n := len(s.Loops); n > 0 {
		localCaption(c.Key("loop-line"), fmt.Sprintf("定期任务 %d 个：%s", n, s.Loops[0]))
	}
}

// stopAutoLocal 停下宿主自动跑的那一轮（同时暂停目标）；没有本地发送在跑，所以不动 localRun 的状态。
func (a *app) stopAutoLocal(writer api.Reader, k api.ConversationKey) {
	chat, ok := writer.(api.LocalChat)
	if !ok {
		return
	}
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() { _ = chat.StopLocal(ctx, k) }()
}

// workbenchStatus 是新界面输入区上方的目标和定期任务状态（各一行，没有就不画）。
func (a *app) workbenchStatus(c *ui.Context, k api.ConversationKey, s api.LocalStatus) {
	th := c.Theme()
	line := func(key, text string) {
		ui.Text(c.Key(key), text).Font(th.Font).TextColor(th.TextMuted).Wrap().FillWidth()
	}
	if s.GoalLine != "" {
		if kit := a.workbench.shell.kit; kit != nil && s.Goal != nil {
			a.wbGoalRow(c, kit, k, s) // 常驻一行，点开看全文、轮数、预算和裁判理由
		} else {
			hint := "  /goal clear 结束"
			if s.GoalState == "paused" {
				hint = "  /goal resume 继续，/goal clear 结束"
			}
			line("wb-goal-line", s.GoalLine+hint)
		}
	}
	if n := len(s.Loops); n > 0 {
		line("wb-loop-line", fmt.Sprintf("定期任务 %d 个：%s", n, s.Loops[0]))
	}
}
