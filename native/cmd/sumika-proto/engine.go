package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/chatui"
)

type message struct {
	ID, Actor, Text, State, Turn, Error string
	QuoteName, QuoteText                string // 引用的那条（名字、原文）；空就是没引用
	Attempt                             int
	Steps                               []*chatui.Step // 不空时这一条是她这一轮的过程（process_engine.go）
}
type conversation struct {
	ID, Venue, Account, Name string
	Actors                   []string
	Messages                 []*message
	Draft, PendingDraft      string
	Unread                   int
	FailNext                 bool
	Queued                   []string
	QueuePaused              bool
}
type event struct {
	At   time.Duration
	Seq  uint64
	Run  uint64
	Turn string
	Do   func()
}
type trace struct {
	At                         time.Duration
	Target, Resource, From, To string
}
type turn struct {
	ID           string
	Conversation string
	Actor        string
	State        string
	Resources    map[string]string
	Waiters      map[string][]func()
	Tracks       int
	Process      *message // 这一轮的过程消息；第一次出现思考、工具时才建
}
type engine struct {
	Run, Serial, Seq       uint64
	Now                    time.Duration
	Wall                   time.Time
	Paused                 bool
	Events                 []event
	Conversations          []*conversation
	Turns                  []*turn
	Trace                  []trace
	Scripts                map[string]script
	ReadCount, SearchCount map[string]int
	OnAccepted             func(string, string)
	Error                  string
	StepOf                 map[string]*chatui.Step        // 步骤 ID（turn/资源）→ 步骤
	Approvals              map[string]*pendingApproval    // 还没结算的审批，按步骤 ID
	Running                map[*chatui.Step]time.Duration // 正在跑的命令、改文件，记开始的虚拟时间（更新秒数用）
}

var actorNames = map[string]string{"r1": "晴枝", "r2": "雾栞", "r3": "青穗", "r4": "浅舟", "r5": "白榆", "r6": "萤川", "r7": "千澄", "r8": "小岚", "r9": "雨芽", "r10": "星棉", "self": "我"}

func newEngine(run uint64) *engine {
	e := &engine{Run: run, Scripts: make(map[string]script), ReadCount: make(map[string]int), SearchCount: make(map[string]int),
		StepOf: make(map[string]*chatui.Step), Approvals: make(map[string]*pendingApproval), Running: make(map[*chatui.Step]time.Duration)}
	e.Conversations = []*conversation{
		{ID: "A", Venue: "localPrivate", Account: "local", Name: "晴枝", Actors: []string{"r1"}},
		{ID: "B", Venue: "localGroup", Account: "local", Name: "纸灯小屋", Actors: []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8", "r9", "r10"}},
		{ID: "C", Venue: "qqPrivate", Account: "q1", Name: "风铃来信", Actors: []string{"r1"}},
		{ID: "D", Venue: "qqGroup", Account: "q1", Name: "云朵茶会", Actors: []string{"r1", "r2"}},
	}
	for _, cv := range e.Conversations {
		cv.Messages = []*message{{ID: "fixture:" + cv.ID + ":1", Actor: "r1", Text: "窗边的纸灯亮起来了。今天想聊些什么？", State: "complete"}, {ID: "fixture:" + cv.ID + ":2", Actor: "self", Text: "先把想到的事情慢慢说吧。", State: "sent"}, {ID: "fixture:" + cv.ID + ":3", Actor: "r1", Text: "好，我在听。", State: "complete"}}
		e.Scripts[cv.ID] = replyScript(cv, "message.accepted", strings.HasSuffix(cv.Venue, "Group"), false)
	}
	// 群聊里加一条带引用的，让引用块进几何检查（审查 C5）
	b := e.conv("B")
	b.Messages = append(b.Messages, &message{ID: "fixture:B:quote", Actor: "r2", Text: "我也想听。", State: "complete", QuoteName: "晴枝", QuoteText: "窗边的纸灯亮起来了。今天想聊些什么？"})
	return e
}
func (e *engine) conv(id string) *conversation {
	for _, c := range e.Conversations {
		if c.ID == id {
			return c
		}
	}
	return nil
}
func (e *engine) id(prefix string) string {
	e.Serial++
	return fmt.Sprintf("%s:%d:%d", prefix, e.Run, e.Serial)
}
func (e *engine) record(cv, resource, from, to string) {
	e.Trace = append(e.Trace, trace{e.Now, cv, resource, from, to})
}
func (e *engine) schedule(delay time.Duration, tid string, fn func()) {
	e.Seq++
	ev := event{e.Now + delay, e.Seq, e.Run, tid, fn}
	i := sort.Search(len(e.Events), func(i int) bool { return e.Events[i].At > ev.At })
	e.Events = append(e.Events, event{})
	copy(e.Events[i+1:], e.Events[i:])
	e.Events[i] = ev
}
func (e *engine) tick(wall time.Time) {
	if e.Wall.IsZero() {
		e.Wall = wall
	}
	delta := wall.Sub(e.Wall)
	e.Wall = wall
	if !e.Paused {
		e.advance(e.Now + delta)
	}
	e.tickSteps()
}
func (e *engine) advance(to time.Duration) {
	for len(e.Events) > 0 && e.Events[0].At <= to {
		ev := e.Events[0]
		e.Events[0] = event{}
		e.Events = e.Events[1:]
		e.Now = ev.At
		if ev.Run == e.Run {
			ev.Do()
		}
	}
	e.Now = to
}
func (e *engine) next() {
	if len(e.Events) > 0 {
		e.advance(e.Events[0].At)
	}
}
func (e *engine) active(cv string) bool {
	for _, t := range e.Turns {
		if t.Conversation == cv && t.State == "running" {
			return true
		}
	}
	return false
}
func (e *engine) submit(cv *conversation, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || cv.PendingDraft != "" || cv.Account != "local" {
		return false
	}
	if e.active(cv.ID) {
		cv.Queued = append(cv.Queued, text)
		cv.Draft = ""
		return true
	}
	m := &message{ID: e.id("message"), Actor: "self", Text: text, State: "sending", Attempt: 1}
	cv.Messages = append(cv.Messages, m)
	cv.PendingDraft = text
	e.deliver(cv, m)
	return true
}
func (e *engine) deliver(cv *conversation, m *message) {
	fail := cv.FailNext
	cv.FailNext = false
	e.record(cv.ID, m.ID, "draft", "sending")
	e.schedule(180*time.Millisecond, "", func() {
		cv.PendingDraft = ""
		if fail {
			m.State = "failed"
			m.Error = "发送失败"
			e.record(cv.ID, m.ID, "sending", "failed")
			return
		}
		m.State = "sent"
		m.Error = ""
		if strings.TrimSpace(cv.Draft) == m.Text {
			cv.Draft = ""
		}
		e.record(cv.ID, m.ID, "sending", "sent")
		if e.OnAccepted != nil {
			e.OnAccepted(cv.ID, m.Text)
		}
		e.start(e.Scripts[cv.ID], "message.accepted", cv.ID)
	})
}
func (e *engine) retry(cv *conversation, m *message) {
	if m.State != "failed" || cv.PendingDraft != "" {
		return
	}
	m.State = "sending"
	m.Attempt++
	cv.PendingDraft = m.Text
	e.deliver(cv, m)
}
func (e *engine) stop(cv string) {
	for _, t := range e.Turns {
		if t.Conversation == cv && t.State == "running" {
			t.State = "stopped"
			e.record(cv, t.ID, "running", "stopped")
			e.stopSteps(t)
			for _, m := range e.conv(cv).Messages {
				if m.Turn == t.ID && m.State == "streaming" {
					m.State = "interrupted"
				}
			}
		}
	}
	dst := e.Events[:0]
	for _, ev := range e.Events {
		keep := true
		for _, t := range e.Turns {
			if ev.Turn == t.ID && t.State == "stopped" {
				keep = false
				break
			}
		}
		if keep {
			dst = append(dst, ev)
		}
	}
	clear(e.Events[len(dst):])
	e.Events = dst
	e.conv(cv).QueuePaused = true
}
func (e *engine) resume(cv *conversation) { cv.QueuePaused = false; e.drain(cv) }
func (e *engine) drain(cv *conversation) {
	if cv.QueuePaused || e.active(cv.ID) || len(cv.Queued) == 0 {
		return
	}
	text := cv.Queued[0]
	cv.Queued = cv.Queued[1:]
	e.submit(cv, text)
}
func (e *engine) status(cv *conversation) string {
	if cv.PendingDraft != "" {
		return "正在发送"
	}
	n := 0
	actor := ""
	state := ""
	for _, t := range e.Turns {
		if t.Conversation == cv.ID && t.State == "running" {
			n++
			actor = actorNames[t.Actor]
			state = "正在想"
			// 优先级：等批准 > 查资料（跑工具；思考不算）> 输入 > 想
			var streaming, tool, pending bool
			for id, s := range t.Resources {
				st := e.StepOf[t.ID+"/"+id]
				streaming = streaming || s == "streaming"
				tool = tool || (s == "running" && (st == nil || st.Kind != chatui.StepThink))
				pending = pending || s == "pending"
			}
			switch {
			case pending:
				state = "在等你批准"
			case tool:
				state = "正在查资料"
			case streaming:
				state = "正在输入"
			}
		}
	}
	if n > 1 {
		return fmt.Sprintf("%s%s，另 %d 项", actor, state, n-1)
	}
	if n == 1 {
		return actor + state
	}
	for _, m := range cv.Messages {
		if m.State == "failed" {
			return "有消息没有发出去"
		}
	}
	return "随时可以聊聊"
}
func (e *engine) activity(cv string) string {
	return fmt.Sprintf("读了 %d 份资料，搜了 %d 次", e.ReadCount[cv], e.SearchCount[cv])
}
func (e *engine) history(cv *conversation, n int) {
	cv.Messages = make([]*message, n)
	for i := range cv.Messages {
		actor := cv.Actors[i%len(cv.Actors)]
		cv.Messages[i] = &message{ID: fmt.Sprintf("history:%s:%d", cv.ID, i), Actor: actor, Text: fmt.Sprintf("第 %d 封小纸条：沿着树影走了一会儿，发现风也在慢慢改变方向。", i+1), State: "complete"}
	}
}
