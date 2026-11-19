package main

import (
	"testing"

	"sumika-native/internal/api"
)

func typingCount(a *app) int {
	n := 0
	for _, k := range liveKinds(a) {
		if k == liveTyping {
			n++
		}
	}
	return n
}

func typingRow(a *app) *msg {
	for _, m := range a.msgs {
		if m.liveKind == liveTyping {
			return m
		}
	}
	return nil
}

// 工具在跑时过程气泡已经在转圈，不再叠“正在输入”；工具刚结束、模型还没出字时回来；同一行，不重放入场。
func TestTypingHiddenWhileToolRunning(t *testing.T) {
	t.Parallel()
	for _, group := range []bool{false, true} {
		a, conv := liveApp()
		st := a.localRun(conv)
		st.busy = true
		if group {
			st.turn, st.actor = "turn-a", "char-a"
		}
		st.startRun()
		a.reconcileLive()
		if typingCount(a) != 1 {
			t.Fatalf("group=%v 刚开始模型在等，该有“正在输入”：%v", group, liveKinds(a))
		}
		first := typingRow(a)
		tool := func(state string) []api.LocalProcessStep {
			return []api.LocalProcessStep{{ID: "t0", Kind: "think", Step: 0, State: "done", Started: 1},
				{ID: "c0", Kind: "tool", Tool: "powershell", Step: 0, State: state, Started: 2}}
		}
		a.liveProcessUpdate(conv, st, []api.LocalProcessStep{{ID: "t0", Kind: "think", Step: 0, State: "running", Started: 1}})
		if typingCount(a) != 0 {
			t.Fatalf("group=%v 模型在想时过程气泡的点就是这组点，不该再叠一个：%v", group, liveKinds(a))
		}
		a.liveProcessUpdate(conv, st, tool("running"))
		if typingCount(a) != 0 {
			t.Fatalf("group=%v 工具在跑时不该有“正在输入”：%v", group, liveKinds(a))
		}
		a.liveProcessUpdate(conv, st, tool("done"))
		if typingCount(a) != 1 {
			t.Fatalf("group=%v 工具刚结束模型还没回，该回到“正在输入”：%v", group, liveKinds(a))
		}
		if typingRow(a) != first || typingRow(a).rowKey() != first.rowKey() {
			t.Fatalf("group=%v 回来的该是同一行，键不能变（变了会重放入场动画）", group)
		}
		// 第二个工具又开始：再藏起来；停止后旧行里没收尾的步骤不挡着
		a.liveProcessUpdate(conv, st, []api.LocalProcessStep{
			{ID: "t0", Kind: "think", Step: 0, State: "done", Started: 1},
			{ID: "c0", Kind: "tool", Tool: "powershell", Step: 0, State: "done", Started: 2},
			{ID: "c1", Kind: "tool", Tool: "read", Step: 0, State: "running", Started: 3},
		})
		if typingCount(a) != 0 {
			t.Fatalf("group=%v 第二个工具在跑：%v", group, liveKinds(a))
		}
		a.liveReplyUpdate(conv, st, 1, "说话了", 0)
		if typingCount(a) != 0 {
			t.Fatalf("group=%v 有字在出不该有“正在输入”：%v", group, liveKinds(a))
		}
	}
}

// 旧一轮留下的、没来得及收尾的转圈步骤，不挡新一轮的“正在输入”。
func TestTypingNotBlockedByOldTurnRunningStep(t *testing.T) {
	t.Parallel()
	a, conv := liveApp()
	st := a.localRun(conv)
	st.busy = true
	st.turn, st.actor = "turn-a", "char-a"
	st.startRun()
	a.liveProcessUpdate(conv, st, []api.LocalProcessStep{{ID: "c0", Kind: "tool", Tool: "powershell", Step: 0, State: "running", Started: 1}})
	if typingCount(a) != 0 {
		t.Fatalf("工具在跑：%v", liveKinds(a))
	}
	st.turn = "turn-b" // 群里换了人
	st.startRun()
	a.reconcileLive()
	if typingCount(a) != 1 {
		t.Fatalf("上一位留下的转圈步骤不该挡住这一位：%v", liveKinds(a))
	}
}
