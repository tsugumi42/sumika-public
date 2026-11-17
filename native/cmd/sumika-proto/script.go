package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type target struct {
	Venue        string `json:"venue"`
	Account      string `json:"account"`
	Conversation string `json:"conversation"`
	Actor        string `json:"actor"`
	Turn         string `json:"turn"`
}
type script struct {
	Version int     `json:"version"`
	ID      string  `json:"id"`
	Seed    int64   `json:"seed"`
	Policy  string  `json:"policy"`
	Initial string  `json:"initial"`
	Tracks  []track `json:"tracks"`
}
type track struct {
	Name   string `json:"name"`
	Target target `json:"target"`
	On     string `json:"on,omitempty"`
	Steps  []step `json:"steps"`
}
type step struct {
	Op      string            `json:"op"`
	MS      int               `json:"ms,omitempty"`
	Trigger string            `json:"trigger,omitempty"`
	ID      string            `json:"id,omitempty"`
	Args    args              `json:"args,omitempty"`
	Ref     string            `json:"ref,omitempty"`
	Then    map[string][]step `json:"then,omitempty"`
}
type args struct {
	Text       string `json:"text,omitempty"`
	Chunk      string `json:"chunk,omitempty"`
	IntervalMS int    `json:"intervalMs,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Result     string `json:"result,omitempty"`
	Target     string `json:"target,omitempty"`  // 读的文件、搜的词（A02）
	Command    string `json:"command,omitempty"` // 命令（A03、A04、P01、P04）
	Output     string `json:"output,omitempty"`  // 命令输出，按行在时长里均匀到达
	Exit       int    `json:"exit,omitempty"`    // 退出码；不是 0 就算失败（A03）
	Path       string `json:"path,omitempty"`    // 改的文件（P01、P04 的 fake-edit）
	Diff       string `json:"diff,omitempty"`    // 统一 diff 正文
	TTLMS      int    `json:"ttlMs,omitempty"`   // 审批有效期
}

func parseScript(data string, e *engine) (script, error) {
	var s script
	d := json.NewDecoder(strings.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return s, fmt.Errorf("脚本只能包含一个 JSON 对象")
	}
	if s.Version != 1 || s.ID == "" || s.Policy != "current" || s.Initial != "four-venues" {
		return s, fmt.Errorf("需要 version=1、id、policy=current、initial=four-venues")
	}
	names := map[string]bool{}
	for _, tr := range s.Tracks {
		cv := e.conv(tr.Target.Conversation)
		if tr.Name == "" || names[tr.Name] {
			return s, fmt.Errorf("轨道名字为空或重复")
		}
		names[tr.Name] = true
		if cv == nil || cv.Venue != tr.Target.Venue || cv.Account != tr.Target.Account || !slices.Contains(cv.Actors, tr.Target.Actor) {
			return s, fmt.Errorf("轨道 %s 的会话、账号或角色不匹配", tr.Name)
		}
		if tr.Target.Turn != "$input.turnId" {
			return s, fmt.Errorf("轨道 turn 必须为 $input.turnId")
		}
		if tr.On != "" && tr.On != "run.start" && tr.On != "message.accepted" {
			return s, fmt.Errorf("未知 on：%s", tr.On)
		}
		if err := validateSteps(tr.Steps, map[string]string{}, cv.Account == "local"); err != nil {
			return s, fmt.Errorf("轨道 %s：%w", tr.Name, err)
		}
	}
	return s, nil
}
func validateSteps(steps []step, ids map[string]string, local bool) error {
	for _, st := range steps {
		switch st.Op {
		case "wait":
			if st.MS < 0 {
				return fmt.Errorf("wait.ms 不能为负")
			}
		case "endTrack":
		case "trigger":
			if st.ID == "" || ids[st.ID] != "" {
				return fmt.Errorf("资源 ID 为空或重复")
			}
			switch st.Trigger {
			case "M06":
				if st.Args.Text == "" || !utf8.ValidString(st.Args.Text) || st.Args.IntervalMS <= 0 || st.Args.Chunk != "grapheme" {
					return fmt.Errorf("M06 需要非空 text、chunk=grapheme 和正 intervalMs")
				}
			case "A01":
				if st.Args.DurationMS < 0 {
					return fmt.Errorf("思考时长不能为负")
				}
			case "A02":
				if !local || (st.Args.Tool != "fake-read" && st.Args.Tool != "fake-search") || st.Args.DurationMS < 0 {
					return fmt.Errorf("第一步仅支持本地假读搜")
				}
			case "A03", "A04":
				if !local || st.Args.Command == "" || st.Args.DurationMS < 0 {
					return fmt.Errorf("%s 需要本地场合、非空 command、非负 durationMs", st.Trigger)
				}
				if st.Trigger == "A04" && st.Args.DurationMS < 5000 {
					return fmt.Errorf("A04（很慢的工具）的 durationMs 至少 5000")
				}
			case "A05":
				if t := ids[st.Ref]; t != "A03" && t != "A04" && t != "P01" && t != "P04" {
					return fmt.Errorf("A05 的 ref 要指向前面的命令或审批工具")
				}
			case "P01", "P04":
				if !local || st.Args.TTLMS <= 0 || st.Args.DurationMS < 0 {
					return fmt.Errorf("%s 需要本地场合、正 ttlMs、非负 durationMs", st.Trigger)
				}
				if (st.Args.Tool != "fake-command" || st.Args.Command == "") && (st.Args.Tool != "fake-edit" || st.Args.Path == "" || st.Args.Diff == "") {
					return fmt.Errorf("%s 需要 fake-command + command，或 fake-edit + path + diff", st.Trigger)
				}
			default:
				return fmt.Errorf("不支持触发器 %s", st.Trigger)
			}
			ids[st.ID] = st.Trigger
		case "await":
			trigger := ids[st.Ref]
			if trigger == "" {
				return fmt.Errorf("await 引用了不存在的资源 %s", st.Ref)
			}
			terminal := "complete"
			switch trigger {
			case "A02", "A03", "A04", "P01", "P04":
				terminal = "succeeded"
			}
			if _, ok := st.Then[terminal]; !ok {
				return fmt.Errorf("await 缺少终态分支 %s", terminal)
			}
			for state, branch := range st.Then {
				if state != terminal && state != "error" && state != "interrupted" && state != "failed" && state != "canceled" && state != "denied" && state != "expired" {
					return fmt.Errorf("未知终态 %s", state)
				}
				if err := validateSteps(branch, copyIDs(ids), local); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("第一步不支持操作 %s", st.Op)
		}
	}
	return nil
}
func copyIDs(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func scriptJSON(s script) string {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		panic(err)
	}
	return out.String()
}
func replyScript(cv *conversation, on string, interleave, tools bool) script {
	s := script{Version: 1, ID: "quiet-reply", Seed: 42, Policy: "current", Initial: "four-venues"}
	actors := cv.Actors[:1]
	if interleave {
		actors = cv.Actors[:min(2, len(cv.Actors))]
	}
	for i, actor := range actors {
		steps := []step{{Op: "wait", MS: 300 + i*240}}
		if tools {
			steps = append(steps, step{Op: "trigger", Trigger: "A02", ID: "read", Args: args{Tool: "fake-read", DurationMS: 450}}, step{Op: "await", Ref: "read", Then: map[string][]step{"succeeded": {}}}, step{Op: "trigger", Trigger: "A02", ID: "search", Args: args{Tool: "fake-search", DurationMS: 650}}, step{Op: "await", Ref: "search", Then: map[string][]step{"succeeded": {}}})
		}
		text := "我收到你的小纸条了。我们可以从最想说的那件事开始。"
		if i == 1 {
			text = "我也在这里，先把想到的片段放在一起吧。"
		}
		steps = append(steps, step{Op: "trigger", Trigger: "M06", ID: "reply", Args: args{Text: text, Chunk: "grapheme", IntervalMS: 50}}, step{Op: "await", Ref: "reply", Then: map[string][]step{"complete": {}, "interrupted": {{Op: "endTrack"}}, "error": {{Op: "endTrack"}}}})
		if interleave {
			steps = append(steps, step{Op: "wait", MS: 300}, step{Op: "trigger", Trigger: "M06", ID: "reply-2", Args: args{Text: []string{"然后再慢慢接着聊。", "嗯，不着急。"}[i], Chunk: "grapheme", IntervalMS: 70}}, step{Op: "await", Ref: "reply-2", Then: map[string][]step{"complete": {}}})
		}
		s.Tracks = append(s.Tracks, track{Name: "reply-" + actor, Target: target{cv.Venue, cv.Account, cv.ID, actor, "$input.turnId"}, On: on, Steps: steps})
	}
	return s
}
func (e *engine) start(s script, on, conversationID string) {
	for _, tr := range s.Tracks {
		if tr.On != on && !(tr.On == "" && on == "run.start") {
			continue
		}
		if on == "message.accepted" && tr.Target.Conversation != conversationID {
			continue
		}
		t := &turn{ID: e.id("turn"), Conversation: tr.Target.Conversation, Actor: tr.Target.Actor, State: "running", Resources: make(map[string]string), Waiters: make(map[string][]func()), Tracks: 1}
		e.Turns = append(e.Turns, t)
		e.record(t.Conversation, t.ID, "idle", "running")
		e.steps(t, tr.Steps)
	}
}
func (e *engine) finish(t *turn) {
	t.Tracks = 0
	e.settle(t)
}

func (e *engine) settle(t *turn) {
	if t.Tracks != 0 || t.State != "running" {
		return
	}
	for _, state := range t.Resources {
		if live(state) {
			return
		}
	}
	t.State = "complete"
	e.record(t.Conversation, t.ID, "running", "complete")
	e.drain(e.conv(t.Conversation))
}
func (e *engine) steps(t *turn, steps []step) {
	if t.State != "running" {
		return
	}
	for i, st := range steps {
		rest := steps[i+1:]
		switch st.Op {
		case "wait":
			e.schedule(time.Duration(st.MS)*time.Millisecond, t.ID, func() { e.steps(t, rest) })
			return
		case "endTrack":
			e.finish(t)
			return
		case "trigger":
			e.trigger(t, st)
		case "await":
			resume := func() {
				state := t.Resources[st.Ref]
				branch, ok := st.Then[state]
				if !ok {
					e.Error = "资源进入脚本未声明的终态：" + state
					e.stop(t.Conversation)
					return
				}
				next := make([]step, 0, len(branch)+len(rest))
				next = append(next, branch...)
				next = append(next, rest...)
				e.steps(t, next)
			}
			state := t.Resources[st.Ref]
			if live(state) {
				t.Waiters[st.Ref] = append(t.Waiters[st.Ref], resume)
				return
			}
			resume()
			return
		}
	}
	e.finish(t)
}
func (e *engine) resource(t *turn, id, state string) {
	old := t.Resources[id]
	t.Resources[id] = state
	e.record(t.Conversation, t.ID+"/"+id, old, state)
	if !live(state) { // 所有终态都唤醒等它的（失败、取消、拒绝、过期也要走对应的分支）
		waiters := t.Waiters[id]
		delete(t.Waiters, id)
		for _, fn := range waiters {
			fn()
		}
	}
	e.settle(t)
}
func (e *engine) trigger(t *turn, st step) {
	cv := e.conv(t.Conversation)
	if st.Trigger == "M06" {
		m := &message{ID: t.ID + "/" + st.ID, Actor: t.Actor, State: "streaming", Turn: t.ID}
		cv.Messages = append(cv.Messages, m)
		e.resource(t, st.ID, "streaming")
		ends := graphemeEnds(st.Args.Text)
		pos := 0
		var emit func()
		emit = func() {
			if t.State != "running" || m.State != "streaming" {
				return
			}
			m.Text = st.Args.Text[:ends[pos]]
			pos++
			if pos == len(ends) {
				m.State = "complete"
				cv.Unread++
				e.resource(t, st.ID, "complete")
				return
			}
			e.schedule(time.Duration(st.Args.IntervalMS)*time.Millisecond, t.ID, emit)
		}
		e.schedule(0, t.ID, emit)
	} else {
		e.triggerStep(t, st)
	}
}

// 内置文本逐字显示；组合音标、变体、肤色、区域旗帜与 ZWJ 链不能切成半个可见字。
func graphemeEnds(s string) []int {
	ends := make([]int, 0, utf8.RuneCountInString(s))
	join := false
	regional := 0
	for i, r := range s {
		attached := unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0xfe0f || (r >= 0x1f3fb && r <= 0x1f3ff) || r == 0x200d || join
		flag := r >= 0x1f1e6 && r <= 0x1f1ff
		if flag && regional%2 == 1 {
			attached = true
		}
		if i > 0 && !attached {
			ends = append(ends, i)
		}
		join = r == 0x200d
		if flag {
			regional++
		} else {
			regional = 0
		}
	}
	if s != "" {
		ends = append(ends, len(s))
	}
	return ends
}
