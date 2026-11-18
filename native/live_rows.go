package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/segview"
)

// 本地私聊一轮的临时行（规格-本地回合显示）：发出的话和正在生成的回复都排在消息列末尾，
// 真消息到了原地接替。消息列是唯一的显示位置，输入框高度不受影响。
const (
	liveNone    int8 = iota // 真消息
	livePending             // 发出去、还没进库的话
	liveReply               // 正在生成的回复
	liveTyping              // 她还没出字
	liveProcess             // 这一段的思考和工具
)

const (
	liveSendWindow  = 2 * time.Second // 发出的话：真消息的时间不早于发送前这么久才算同一句
	liveHandoffWait = 3 * time.Second // 这一步存库以后等真消息的时间，等不到就撤掉
)

var liveSeq uint64

func liveID(prefix string) string {
	liveSeq++
	return prefix + strconv.FormatUint(liveSeq, 10)
}

// addPending 点发送时调：列表末尾当场多一行，滚到底。返回这一行，拒收时用它撤掉。
func (a *app) addPending(conv, text string) *msg {
	st := a.localRun(conv)
	m := &msg{id: liveID("live-send-"), mine: true, md: false, text: text, at: time.Now().UnixMilli(), liveKind: livePending, enterNow: true}
	m.blocks = segview.TextBlocks(text)
	st.pending = append(st.pending, m)
	st.sentAt = m.at
	if a.sel == conv {
		a.reconcileLive()
		a.list.ScrollTo(len(a.msgs)-1, ui.End)
	}
	return m
}

// dropPending 后台拒收：撤掉这一行。
func (a *app) dropPending(conv string, m *msg) {
	st := a.localRun(conv)
	for i, p := range st.pending {
		if p == m {
			st.pending = append(st.pending[:i:i], st.pending[i+1:]...)
			break
		}
	}
	if a.sel == conv {
		a.reconcileLive()
	}
}

// liveReplyUpdate 收到后台的进度：text 是这一步到现在的全文，sealed 是已经存库的步数（步号从 0 起）。
func (a *app) liveReplyUpdate(conv string, st *localRun, step int, text string, sealed int) {
	for _, r := range st.replies {
		if r.liveStep < sealed && !r.liveSealed {
			r.liveSealed, r.streaming = true, false
			r.liveDeadline = time.Now().Add(liveHandoffWait)
			a.scheduleLiveExpiry(conv)
		}
	}
	if step >= sealed && text == "" {
		if n := len(st.replies); n > 0 && st.replies[n-1].liveStep == step && !st.replies[n-1].liveSealed {
			st.replies = st.replies[:n-1]
			kept := st.replied[:0]
			for _, s := range st.replied {
				if s != step {
					kept = append(kept, s)
				}
			}
			st.replied = kept
		}
	} else if step >= sealed && text != "" {
		var cur *msg
		if n := len(st.replies); n > 0 && st.replies[n-1].liveStep == step && !st.replies[n-1].liveSealed {
			cur = st.replies[n-1]
		}
		if cur == nil {
			cur = a.newLiveReply(conv, step)
			cur.liveTurn = st.turn
			st.replies = append(st.replies, cur)
			st.replied = append(st.replied, step)
		}
		cur.text = text
		cur.version++
	}
	if a.sel == conv {
		a.reconcileLive()
	}
}

func (a *app) newLiveReply(conv string, step int) *msg {
	k := a.keyOf(conv)
	id := a.liveActor(conv)
	m := &msg{id: liveID("live-reply-"), md: true, isChar: true, actor: id, streaming: true, at: time.Now().UnixMilli(), liveKind: liveReply, liveStep: step, enterNow: true}
	if ch, ok := a.charOf(id); ok {
		m.who, m.color = ch.Name, ch.Color
	}
	prepareChatView(m, k.Platform, nil)
	return m
}

// scheduleLiveExpiry 这一步存库以后真消息一直不来（没存成、被清掉）：到点撤掉，不留假消息。
func (a *app) scheduleLiveExpiry(conv string) {
	time.AfterFunc(liveHandoffWait+50*time.Millisecond, func() {
		a.post(func() {
			st := a.localRuns[conv]
			if st == nil {
				return
			}
			now := time.Now()
			keep := st.replies[:0:0]
			for _, r := range st.replies {
				if !r.liveSealed || r.liveDeadline.After(now) {
					keep = append(keep, r)
				}
			}
			st.replies = keep
			if a.sel == conv {
				a.reconcileLive()
			}
		})
	})
}

// reconcileLive 让 a.msgs 变成“真消息 + 这一轮的临时行”：每次消息列表或临时行变了都调一次。
func (a *app) reconcileLive() {
	real := a.msgs[:0:0]
	for _, m := range a.msgs {
		if m.liveKind == liveNone {
			real = append(real, m)
		}
	}
	st := a.localRuns[a.sel]
	if st == nil {
		a.msgs = real
		return
	}
	st.pending = claimPending(real, st.pending)
	st.replies = claimReplies(real, st, st.sentAt)
	real = withProcessRows(real)
	for _, group := range [][]*msg{st.pending, st.procs, st.replies} {
		real, _ = insertLive(real, group)
	}
	if (st.busy || st.submitting) && !st.stopped {
		if st.typing == nil {
			st.typing = &msg{id: liveID("live-typing-"), liveKind: liveTyping, isChar: true, actor: a.liveActor(a.sel), at: time.Now().UnixMilli(), enterNow: true}
		}
		// 有字在出、在想、工具在跑时不画，但行留着：回来时还是同一个键，不重放入场动画
		if !hasStreamingReply(st.replies) && !st.stepRunning() {
			real = append(real, st.typing)
		}
	} else {
		st.typing = nil
	}
	a.msgs = real
}

// stepRunning 这一轮有一步正在进行（思考或工具）：过程气泡已经在动，不再叠一个三个点（主人 2026-11-09：
// “本来我是想这个浮动动画就是在思考的内容，如果有开始返回思考内容的话就会在它后边增加正在想的文本”）。
func (st *localRun) stepRunning() bool {
	for _, p := range st.procs {
		if p.liveTurn != st.turn || p.liveGap < 0 {
			continue
		}
		for _, s := range p.steps {
			if s.State == chatui.StepRunning {
				return true
			}
		}
	}
	return false
}

func hasStreamingReply(replies []*msg) bool {
	for _, r := range replies {
		if !r.liveSealed {
			return true
		}
	}
	return false
}

// claimPending 发出的话：找到真消息就让它接替，列表键不变。
func claimPending(real []*msg, pending []*msg) []*msg {
	if len(pending) == 0 {
		return pending
	}
	keep := pending[:0:0]
	for _, p := range pending {
		if !claimFirst(real, p, func(m *msg) bool {
			return m.mine && m.at >= p.at-liveSendWindow.Milliseconds() && samePending(m, p)
		}) {
			keep = append(keep, p)
		}
	}
	return keep
}

// samePending 带图、带引用的话文字可能和输入框不一样；这时只看方向和时间。
func samePending(m, p *msg) bool {
	if p.liveLoose {
		return true
	}
	return strings.TrimSpace(m.text) == strings.TrimSpace(p.text)
}

// claimReplies 回复：后台说这一步存好了才接替，不比文字（存库前会清理正文）。这一步之前的过程行跟着挂到真回复前面。
func claimReplies(real []*msg, st *localRun, since int64) []*msg {
	replies := st.replies
	if len(replies) == 0 {
		return replies
	}
	keep := replies[:0:0]
	for _, r := range replies {
		claimed := r.liveSealed && claimFirst(real, r, func(m *msg) bool {
			return !m.mine && m.isChar && m.at >= since && (st.actor == "" || m.actor == r.actor) // 群里要认准是谁说的
		})
		if !claimed {
			keep = append(keep, r)
			continue
		}
		if r.liveTurn == st.turn {
			st.handed = max(st.handed, r.liveStep+1)
		}
		for i, p := range st.procs {
			if p.liveTurn == r.liveTurn && p.liveGap >= 0 && p.liveGap <= r.liveStep {
				r.liveClaimedBy().procRow = p
				st.procs = append(st.procs[:i:i], st.procs[i+1:]...)
				break
			}
		}
	}
	return keep
}

// withProcessRows 把每条真回复的过程行放到它前面：进行中攒下的优先，没有就用后台读的。
func withProcessRows(real []*msg) []*msg {
	need := false
	for _, m := range real {
		if m.procRow != nil || len(m.proc) > 0 {
			need = true
			break
		}
	}
	if !need {
		return real
	}
	out := make([]*msg, 0, len(real)+4)
	for _, m := range real {
		if m.procRow == nil && len(m.proc) > 0 {
			m.procRow = processRow(m.id+"#process", m.actor, m.who, m.color, m.at-1, m.proc)
			m.procRow.noEnter, m.procRow.liveKind = true, liveProcess
		}
		if m.procRow != nil {
			out = append(out, m.procRow)
		}
		out = append(out, m)
	}
	return out
}

// insertLive 把临时行按时间插进列表（已经有的新消息排在它们后面）。
func insertLive(list []*msg, group []*msg) ([]*msg, int) {
	for _, m := range group {
		i := len(list)
		for i > 0 && list[i-1].at > m.at {
			i--
		}
		list = append(list, nil)
		copy(list[i+1:], list[i:])
		list[i] = m
	}
	return list, len(group)
}

// liveProcessUpdate 收到这一轮的思考和工具：工具算在下一段回复之前，思考算在这一步回复之前。
func (a *app) liveProcessUpdate(conv string, st *localRun, steps []api.LocalProcessStep) {
	if len(steps) == 0 {
		return
	}
	k := a.keyOf(conv)
	groups := map[int][]api.LocalProcessStep{}
	var order []int
	for _, s := range steps {
		gap := s.Step
		if s.Kind == "tool" {
			gap = s.Step + 1
		}
		for gap > 0 && !st.repliedStep(gap-1) { // 上一步没说话：这段思考和工具接在同一个过程气泡里
			gap--
		}
		if _, ok := groups[gap]; !ok {
			order = append(order, gap)
		}
		groups[gap] = append(groups[gap], s)
	}
	for _, gap := range order {
		var row *msg
		for _, p := range st.procs {
			if p.liveTurn == st.turn && p.liveGap == gap {
				row = p
			}
		}
		if row == nil {
			if st.handedGap(gap) {
				continue // 这一段的真回复已经到了
			}
			id := a.liveActor(conv)
			ch, _ := a.charOf(id)
			row = processRow(liveID("live-process-"), id, ch.Name, ch.Color, groups[gap][0].Started, nil)
			row.liveKind, row.liveGap, row.liveTurn = liveProcess, gap, st.turn
			row.enterNow = st.typing == nil // 三个点正亮着就是它接着长出来，不再进场一次
			prepareChatView(row, k.Platform, nil)
			st.procs = append(st.procs, row)
		}
		row.steps = chatSteps(groups[gap])
		row.version++
	}
	if a.sel == conv {
		a.reconcileLive()
	}
}

// repliedStep 这一步她说过话（有过回复气泡）。
func (st *localRun) repliedStep(step int) bool {
	for _, s := range st.replied {
		if s == step {
			return true
		}
	}
	return false
}

// startRun 新的一轮：上一轮没被接替的过程行留在原位，不再和新一轮的步号混。
func (st *localRun) startRun() {
	st.handed, st.replied = 0, nil
	for _, p := range st.procs {
		p.liveGap = -1
	}
}

// handedGap 这一段的回复已经被真消息接替过了：后台进度再来也不开新行。
func (st *localRun) handedGap(gap int) bool { return gap < st.handed }

// processRow 造一行过程气泡。
func processRow(id, actor, who, color string, at int64, steps []api.LocalProcessStep) *msg {
	m := &msg{id: id, isChar: true, actor: actor, who: who, color: color, at: at, steps: chatSteps(steps)}
	m.chatColor = chatColor(color)
	m.chatName = chatNameID(actor)
	return m
}

// chatSteps 把后台的步骤换成过程组件的。
func chatSteps(steps []api.LocalProcessStep) []*chatui.Step {
	out := make([]*chatui.Step, 0, len(steps))
	for _, s := range steps {
		v := &chatui.Step{ID: s.ID, Tool: s.Tool, Target: s.Target, Seconds: s.Seconds, Thought: s.Thought, Output: s.Output}
		if s.Rule != "" {
			v.Approval = &chatui.Approval{State: chatui.ApprovalAuto, AutoReason: s.Rule}
		}
		switch s.State {
		case "failed":
			v.State = chatui.StepFailed
		case "denied":
			v.State = chatui.StepDenied
		case "stopped":
			v.State = chatui.StepStopped
		case "running":
			v.State = chatui.StepRunning
		default:
			v.State = chatui.StepDone
		}
		switch {
		case s.Kind == "recall": // 自动召回不是工具：单独一种，明细原样带过来
			v.Kind, v.Tool = chatui.StepRecall, ""
			for _, r := range s.Recall {
				it := chatui.RecallItem{Text: r.Text, Kind: r.Kind, Source: r.Source, At: r.CreatedAt, Pinned: r.Pinned, Hearsay: r.Hearsay,
					Score: r.Score, Rerank: r.Rerank, Sim: r.Sim, Lexical: r.Lexical, Vector: r.Vector}
				for _, p := range r.Previous {
					it.Previous = append(it.Previous, chatui.RecallPrior{Text: p.Text, At: p.CreatedAt})
				}
				v.Recall = append(v.Recall, it)
			}
		case s.Kind == "think":
			v.Kind = chatui.StepThink
		case s.Tool == "powershell":
			v.Kind = chatui.StepCommand
		case s.Tool == "write", s.Tool == "edit":
			v.Kind = chatui.StepEdit
		case s.Tool == "grep", s.Tool == "find", s.Tool == "memory_search", s.Tool == "recall_raw":
			v.Kind = chatui.StepSearch
		case s.Tool == "read":
			v.Kind = chatui.StepRead
		default: // 待办、记忆、MCP、读链接……都写清是哪个工具，不算“读文件”
			v.Kind = chatui.StepTool
		}
		out = append(out, v)
	}
	return out
}

// claimFirst 找第一条还没被接替过的真消息，把临时行的列表键交给它。
func claimFirst(real []*msg, live *msg, match func(*msg) bool) bool {
	for _, m := range real {
		if m.listKey == "" && match(m) {
			m.listKey = live.rowKey()
			m.noEnter = true
			live.claimedBy = m
			return true
		}
	}
	return false
}

func (m *msg) liveClaimedBy() *msg { return m.claimedBy }

// rowKey 是虚拟列表认的键：真消息接替临时行以后沿用临时行的键，高度和滚动位置不跳。
func (m *msg) rowKey() string {
	if m.listKey != "" {
		return m.listKey
	}
	return m.id
}

// clearLive 这一轮结束：只留等真消息接替的，“正在输入”撤掉。
func (a *app) clearLive(conv string, st *localRun) {
	st.typing = nil
	for _, p := range st.procs {
		for _, s := range p.steps {
			if s.State == chatui.StepRunning {
				s.State = chatui.StepStopped
			}
		}
	}
	for _, r := range st.replies {
		if !r.liveSealed {
			r.liveSealed, r.streaming = true, false
			r.liveDeadline = time.Now().Add(liveHandoffWait)
		}
	}
	if len(st.replies) > 0 {
		a.scheduleLiveExpiry(conv)
	}
	if a.sel == conv {
		a.reconcileLive()
	}
}

// stepOpenState 是过程气泡每一段的展开状态，按段第一步的 ID 记。
func (a *app) stepOpenState(id string) *bool {
	if a.stepOpen == nil {
		a.stepOpen = map[string]*bool{}
	}
	o := a.stepOpen[id]
	if o == nil {
		o = new(bool)
		a.stepOpen[id] = o
	}
	return o
}
