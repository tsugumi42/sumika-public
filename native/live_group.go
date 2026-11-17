package main

import (
	"time"

	"sumika-native/internal/api"
)

// 本地群聊一轮的临时行（规格-本地回合显示）：和私聊同一套，只是一轮里好几位成员轮流说。
// 换人时当成新的一轮（步号从 0 重来），上一位没等到真消息的回复留到接替期限。

// liveActor 是现在说话的角色：群聊是当前这位，私聊就是会话的角色。
func (a *app) liveActor(conv string) string {
	if st := a.localRuns[conv]; st != nil && st.actor != "" {
		return st.actor
	}
	return charOfConv(a.keyOf(conv))
}

// liveGroupDraft 收到群聊的进度：Step 为 -1 说这位开始了；Text 为空说这一步存好了。
func (a *app) liveGroupDraft(conv string, st *localRun, v api.LocalDraft) {
	if v.TurnID != st.turn {
		for _, r := range st.replies {
			if !r.liveSealed {
				r.liveSealed, r.streaming = true, false
				r.liveDeadline = time.Now().Add(liveHandoffWait)
			}
		}
		if len(st.replies) > 0 {
			a.scheduleLiveExpiry(conv)
		}
		st.turn, st.actor, st.sealed = v.TurnID, v.CharacterID, 0
		st.typing = nil                // 换了人，“正在输入”的名字跟着换
		st.handed, st.replied = 0, nil // 步号从 0 重来；上一位的过程行靠 liveTurn 认，等它的真消息
	}
	if v.Step < 0 {
		if a.sel == conv {
			a.reconcileLive()
		}
		return
	}
	if v.Text == "" && !v.Reset { // 撤回不算存好：备选模型还在这一步重来
		st.sealed = max(st.sealed, v.Step+1)
	}
	a.liveReplyUpdate(conv, st, v.Step, v.Text, st.sealed)
}
