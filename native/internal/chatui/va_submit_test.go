package chatui

import (
	"testing"
	"time"
)

// 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

// 提交中：主按钮里转圈，按钮宽度不变，旁边的按钮不跳。
func TestSubmittingKeepsWidth(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 720}
	widths := map[ApprovalState]float32{}
	xs := map[ApprovalState]float32{}
	for _, st := range []ApprovalState{ApprovalPending, ApprovalSubmitting} {
		root := tree(processTester(h, []*Step{{ID: "c", Kind: StepCommand, State: StepRunning, Target: "rm x", Approval: &Approval{State: st, Remaining: time.Minute, Scopes: []ApprovalScope{AllowConversation, AllowAlways}}}}, map[string]*bool{}))
		allow, more := root.find("allow"), root.find("more")
		if allow == nil || more == nil {
			t.Fatal("找不到按钮")
		}
		widths[st], xs[st] = allow.Box.W, more.Box.X
	}
	if !near(widths[ApprovalPending], widths[ApprovalSubmitting]) || !near(xs[ApprovalPending], xs[ApprovalSubmitting]) {
		t.Errorf("提交中主按钮宽 %v、更多按钮位置 %v，和等决定时不一样", widths, xs)
	}
}
