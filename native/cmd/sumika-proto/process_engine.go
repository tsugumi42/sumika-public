package main

import (
	"strings"
	"time"

	"sumika-native/internal/chatui"
)

// 假后端的过程步骤（docs/规格-过程组件.md 六-5；交互原型 A01～A05、A08、P01、P04）。
// 每一轮第一次出现思考、读搜、命令、改文件时，在会话里加一条过程消息，后面的步骤都挂到它上面；
// 步骤的状态跟着资源走（running → succeeded / failed / canceled，审批 pending → denied / expired）。

// pendingApproval 是一张还没结算的审批单。
type pendingApproval struct {
	Turn     *turn
	Resource string
	Step     *chatui.Step
	Args     args
	Deadline time.Duration // 虚拟时间：暂停时倒计时也停
}

// live 是没到终态的资源状态；await 在这些状态上等，turn 有这些状态时不算完。
func live(state string) bool { return state == "streaming" || state == "running" || state == "pending" }

func (e *engine) processMsg(t *turn) *message {
	if t.Process == nil {
		cv := e.conv(t.Conversation)
		t.Process = &message{ID: t.ID + "/process", Actor: t.Actor, State: "complete", Turn: t.ID}
		cv.Messages = append(cv.Messages, t.Process)
	}
	return t.Process
}

func (e *engine) addStep(t *turn, id string, s *chatui.Step) *chatui.Step {
	s.ID = t.ID + "/" + id
	p := e.processMsg(t)
	p.Steps = append(p.Steps, s)
	e.StepOf[s.ID] = s
	return s
}

func secondsOf(d time.Duration) int { return int(d / time.Second) }

// triggerStep 处理 M06（出字）以外的触发器。
func (e *engine) triggerStep(t *turn, st step) {
	cv := e.conv(t.Conversation)
	dur := time.Duration(st.Args.DurationMS) * time.Millisecond
	switch st.Trigger {
	case "A01": // 思考
		s := e.addStep(t, st.ID, &chatui.Step{Kind: chatui.StepThink, State: chatui.StepRunning, Thought: st.Args.Text})
		e.resource(t, st.ID, "running")
		e.schedule(dur, t.ID, func() {
			s.State, s.Seconds = chatui.StepDone, secondsOf(dur)
			e.resource(t, st.ID, "complete")
		})
	case "A02": // 假读、假搜
		kind, target := chatui.StepRead, st.Args.Target
		if st.Args.Tool == "fake-search" {
			kind = chatui.StepSearch
			if target == "" {
				target = "纸灯"
			}
		} else if target == "" {
			target = "notes/今天.md"
		}
		s := e.addStep(t, st.ID, &chatui.Step{Kind: kind, State: chatui.StepRunning, Target: target})
		e.resource(t, st.ID, "running")
		e.schedule(dur, t.ID, func() {
			if st.Args.Tool == "fake-read" {
				e.ReadCount[cv.ID]++
			} else {
				e.SearchCount[cv.ID]++
			}
			s.State = chatui.StepDone
			e.resource(t, st.ID, "succeeded")
		})
	case "A03", "A04": // 命令（A04 是很慢的命令，没有进度）
		s := e.addStep(t, st.ID, &chatui.Step{Kind: chatui.StepCommand, State: chatui.StepRunning, Target: st.Args.Command})
		e.resource(t, st.ID, "running")
		e.runTool(t, st.ID, s, st.Args)
	case "A05": // 取消一个还在跑的工具：过 DurationMS（取消确认延迟）才真停
		e.resource(t, st.ID, "running")
		e.schedule(dur, t.ID, func() {
			if s := e.StepOf[t.ID+"/"+st.Ref]; s != nil && t.Resources[st.Ref] == "running" {
				s.State = chatui.StepStopped
				e.resource(t, st.Ref, "canceled")
			}
			e.resource(t, st.ID, "complete")
		})
	case "P01", "P04": // 要审批的命令或改文件
		s := &chatui.Step{State: chatui.StepRunning, Approval: &chatui.Approval{State: chatui.ApprovalPending, Reason: st.Args.Text, Remaining: time.Duration(st.Args.TTLMS) * time.Millisecond, Shown: e.Wall, Scopes: []chatui.ApprovalScope{chatui.AllowConversation, chatui.AllowAlways}}}
		if st.Args.Tool == "fake-edit" {
			s.Kind, s.Target, s.Diff = chatui.StepEdit, st.Args.Path, st.Args.Diff
			s.Added, s.Removed = diffCount(st.Args.Diff)
		} else {
			s.Kind, s.Target = chatui.StepCommand, st.Args.Command
		}
		e.addStep(t, st.ID, s)
		e.resource(t, st.ID, "pending")
		pa := &pendingApproval{Turn: t, Resource: st.ID, Step: s, Args: st.Args, Deadline: e.Now + time.Duration(st.Args.TTLMS)*time.Millisecond}
		e.Approvals[s.ID] = pa
		e.schedule(time.Duration(st.Args.TTLMS)*time.Millisecond, t.ID, func() {
			if s.Approval.State != chatui.ApprovalPending {
				return
			}
			delete(e.Approvals, s.ID)
			s.Approval.State, s.Approval.Remaining, s.State = chatui.ApprovalExpired, 0, chatui.StepStopped
			e.resource(t, st.ID, "expired")
		})
	}
}

// runTool 跑一个假工具：输出按行在时长里均匀到达；到点时还在跑才结算（被取消以后迟到的结果只进轨迹，不改成功，A05）。
func (e *engine) runTool(t *turn, id string, s *chatui.Step, a args) {
	dur := time.Duration(a.DurationMS) * time.Millisecond
	start := e.Now
	e.Running[s] = start
	if a.Output != "" {
		lines := strings.Split(strings.TrimRight(a.Output, "\n"), "\n")
		for i, l := range lines {
			e.schedule(dur*time.Duration(i+1)/time.Duration(len(lines)+1), t.ID, func() {
				if t.Resources[id] == "running" {
					s.Output += l + "\n"
				}
			})
		}
	}
	e.schedule(dur, t.ID, func() {
		delete(e.Running, s)
		if t.Resources[id] != "running" {
			e.record(t.Conversation, t.ID+"/"+id, t.Resources[id], "late-result") // 取消以后到的结果
			return
		}
		s.Seconds = secondsOf(dur)
		if a.Exit != 0 {
			s.State, s.ExitCode, s.HasExit = chatui.StepFailed, a.Exit, true
			e.resource(t, id, "failed")
			return
		}
		s.State = chatui.StepDone
		e.resource(t, id, "succeeded")
	})
}

// approve、deny 是主人在审批块上的决定。先进"提交中"（按钮禁用），150ms 后结算；已经过期、撤回、结算过的单不再认（P04 迟到批准）。
func (e *engine) approve(stepID string, scope chatui.ApprovalScope) bool {
	pa := e.Approvals[stepID]
	if pa == nil || pa.Step.Approval.State != chatui.ApprovalPending {
		return false
	}
	pa.Step.Approval.State = chatui.ApprovalSubmitting
	e.record(pa.Turn.Conversation, stepID, "pending", "submitting")
	e.schedule(150*time.Millisecond, pa.Turn.ID, func() {
		delete(e.Approvals, stepID)
		pa.Step.Approval.State, pa.Step.Approval.Remaining = chatui.ApprovalAllowed, 0
		e.resource(pa.Turn, pa.Resource, "running")
		if pa.Step.Kind == chatui.StepEdit {
			e.Running[pa.Step] = e.Now
			e.schedule(time.Duration(pa.Args.DurationMS)*time.Millisecond, pa.Turn.ID, func() {
				delete(e.Running, pa.Step)
				pa.Step.State = chatui.StepDone
				e.resource(pa.Turn, pa.Resource, "succeeded")
			})
			return
		}
		e.runTool(pa.Turn, pa.Resource, pa.Step, pa.Args)
	})
	return true
}

func (e *engine) deny(stepID string) bool {
	pa := e.Approvals[stepID]
	if pa == nil || pa.Step.Approval.State != chatui.ApprovalPending {
		return false
	}
	pa.Step.Approval.State = chatui.ApprovalSubmitting
	e.schedule(150*time.Millisecond, pa.Turn.ID, func() {
		delete(e.Approvals, stepID)
		pa.Step.Approval.State, pa.Step.Approval.Remaining, pa.Step.State = chatui.ApprovalDenied, 0, chatui.StepDenied
		e.resource(pa.Turn, pa.Resource, "denied")
	})
	return true
}

// tickSteps 在虚拟时间推进后更新审批的剩余时间、运行中命令的秒数。
func (e *engine) tickSteps() {
	for _, pa := range e.Approvals {
		if pa.Step.Approval.State == chatui.ApprovalPending {
			pa.Step.Approval.Remaining = max(0, pa.Deadline-e.Now)
		}
	}
	for s, start := range e.Running {
		s.Seconds = secondsOf(e.Now - start)
	}
}

// nextRedraw 是审批倒计时、运行中命令的秒数下一次会变的时刻离现在多久（界面按它排重画，否则没有事件时数字停住）；没有就 0。
func (e *engine) nextRedraw() time.Duration {
	var next time.Duration
	pick := func(d time.Duration) {
		if d > 0 && (next == 0 || d < next) {
			next = d
		}
	}
	for _, pa := range e.Approvals {
		if r := pa.Deadline - e.Now; pa.Step.Approval.State == chatui.ApprovalPending && r > 0 {
			d := r % time.Second // 倒计时向上取整：剩余时间跨过整秒时数字变
			if d == 0 {
				d = time.Second
			}
			pick(d)
		}
	}
	for _, start := range e.Running {
		pick(time.Second - (e.Now-start)%time.Second)
	}
	return next
}

// stopSteps：一轮被停下时，还在跑的步骤标成停了，没结算的审批撤回。
func (e *engine) stopSteps(t *turn) {
	if t.Process == nil {
		return
	}
	for _, s := range t.Process.Steps {
		if s.State == chatui.StepRunning {
			s.State = chatui.StepStopped
		}
		if s.Approval != nil && (s.Approval.State == chatui.ApprovalPending || s.Approval.State == chatui.ApprovalSubmitting) {
			s.Approval.State, s.Approval.Remaining = chatui.ApprovalRevoked, 0
			delete(e.Approvals, s.ID)
		}
		delete(e.Running, s)
	}
}

func diffCount(diff string) (added, removed int) {
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}
