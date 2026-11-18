package main

import (
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

// 程序退出时被中断的回合：后台读到的步骤挂在系统行上，聊天区把过程气泡画在系统行前面，
// 用说话的角色的名字和颜色；被切断的命令是“停了”，展开处第一句写清程序退出时中断；系统行本身还是通知条。
func TestInterruptedTurnShowsProcessBeforeNotice(t *testing.T) {
	t.Parallel()
	a := shellApp()
	k := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:r1"}
	charOf := func(id string) (character, bool) {
		return character{ID: id, Name: "白羽", Color: "#3377bb"}, id == "r1"
	}
	v := systemRowView("n1", "turn", "程序退出时，这一轮被中断了：1 个工具没有跑完，结果不知道", 61000)
	v.ActorID = "r1"
	v.Process = []api.LocalProcessStep{
		{ID: "c0", Kind: "tool", Tool: "read", Target: "a.txt", State: "done", Started: 60001},
		{ID: "c1", Kind: "tool", Tool: "powershell", Target: "Start-Sleep 30", State: "stopped", Output: "程序退出时中断：这一步没有跑完", Started: 60002},
	}
	a.msgs = []*msg{
		{id: "m1", mine: true, owner: true, text: "跑个命令", at: 60000, who: "主人"},
		msgFromView(k, v, charOf),
	}
	a.msgs = withProcessRows(a.msgs)
	if len(a.msgs) != 3 || a.msgs[1].liveKind != liveProcess || a.msgs[2].service == "" {
		t.Fatalf("该是：主人的话、过程气泡、系统行：%d 行", len(a.msgs))
	}
	proc := a.msgs[1]
	if proc.who != "白羽" || proc.actor != "r1" || len(proc.steps) != 2 {
		t.Fatalf("过程气泡用角色的名字：%+v", proc)
	}
	cmd := proc.steps[1]
	if cmd.State != chatui.StepStopped || cmd.Kind != chatui.StepCommand || cmd.Target != "Start-Sleep 30" || cmd.Output == "" {
		t.Fatalf("被切断的命令是停了、命令和说明都在：%+v", cmd)
	}
	if proc.steps[0].State != chatui.StepDone {
		t.Fatalf("跑完的不受影响：%+v", proc.steps[0])
	}
	if got := chatMessage(a.msgs[2]); got.Service == "" || got.Steps != nil {
		t.Fatalf("系统行仍是通知条，不带步骤：%+v", got)
	}
}
