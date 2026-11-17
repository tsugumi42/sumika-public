package turn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/artifact"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/persona"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
	"sumika-native/internal/workcontext"
)

type workPort struct {
	service *workcontext.Service
}

func (h *Hub) initWork() {
	if h.trace() == nil {
		return
	}
	limits := h.Cfg.WorkSettings
	if limits == (config.Work{}) {
		cfg, err := config.LoadIn(h.Cfg.Root, h.Cfg.Data.Root)
		if err != nil {
			h.workErr = err
			return
		}
		limits = cfg.Agent.Work
	}
	if err := limits.Validate(); err != nil {
		h.workErr = err
		return
	}
	h.Cfg.WorkSettings = limits
	if h.Cfg.Results == nil {
		h.Cfg.Results = artifact.New(h.trace(), h.Cfg.Data.WorkResultsDir(), limits)
		h.Cfg.Results.(*artifact.Store).SourceAllowed = func(scope kernel.Scope, name string) bool {
			a := audience.Audience{Channel: scope.Channel, Conversation: scope.Conversation, Scene: scope.Scene, PrivateOK: scope.ActorKind == "owner", OwnerAsked: scope.ActorKind == "owner", PersonIDs: []string{scope.ActorID}}
			decls, _ := h.plugins.toolsFor(a)
			return known(decls, name)
		}
		if err := h.Cfg.Results.(*artifact.Store).Sweep(h.base); err != nil {
			h.workErr = err
			return
		}
	}
	if h.Cfg.Work == nil {
		h.Cfg.Work = &workPort{workcontext.New(h.trace(), h.Cfg.Results, limits)}
	}
}
func workEnabled(cfg Config, inv kernel.Invocation) bool {
	return cfg.Work != nil && inv.RootID != "" && artifact.ValidScope(inv.Scope)
}
func (h *Hub) restoreWork(ctx context.Context, l *Live, in Input) error {
	inv := in.Identity
	if !artifact.ValidScope(inv.Scope) {
		return nil
	}
	h.weaveWorkHistory(ctx, l, in)
	state, msgs, err := h.loadWork(ctx, l, in)
	if err != nil {
		return err
	}
	l.workState = state
	if state.Goal == "" && len(msgs) == 0 && len(state.Pending) == 0 && len(state.Unknown) == 0 {
		return nil
	}
	seen := map[int64]bool{}
	for _, m := range msgs {
		if m.Seq != 0 {
			seen[m.Seq] = true
		}
	}
	out := make([]Msg, 0, len(l.msgs)+len(msgs)+1)
	for _, m := range l.msgs {
		if !seen[m.Seq] || m.Seq == 0 {
			out = append(out, m)
		}
	}
	// 回合中途重建（溢出恢复）时这一轮的消息由 tail 接回，不用另给一份交接
	if state.ThroughOrdinal > 0 || (len(msgs) == 0 && !l.midRun) {
		b, err := json.Marshal(state)
		if err != nil {
			return err
		}
		out = append(out, Msg{Role: "user", Kind: KindWork, Scope: in.Scope, Text: "（当前工作的结构化交接；不是公开聊天）\n" + string(b)})
	}
	out = append(out, msgs...)
	l.msgs = out
	for _, m := range msgs {
		if m.Seq != 0 {
			l.seen[m.Seq] = true
		}
	}
	return nil
}

// switchWorkRoot 在同一个实例换到新的 root（每个新输入一个 RootID）时只换属于 root 的状态：
// 在库里开出新 root、读它的工作状态；l.msgs 一条不动，前一次请求的前缀才能原样接上走缓存（ADR-022）。
// 前几个 root 留在实例里的工具结果仍属同一个 Scope，result_read 按 Scope 校验，不看 RootID。
// 新 root 在库里已经有条目（不该发生：RootID 每个新输入才分配）时不能把它们追加到末尾，退回整个重建
func (h *Hub) switchWorkRoot(l *Live, c persona.Character, m llm.Model, in Input) error {
	state, msgs, err := h.loadWork(h.base, l, in)
	if err != nil {
		return err
	}
	if len(msgs) > 0 || state.ThroughOrdinal > 0 {
		return h.rebuildIn(l, c, m, in, false, RebuildWorkRoot)
	}
	l.workState = state
	return nil
}

// loadWork 在库里开出（或认出）这个 root 的工作记录并读出它的状态和消息；调用方决定怎么放进实例
func (h *Hub) loadWork(ctx context.Context, l *Live, in Input) (kernel.WorkState, []Msg, error) {
	inv := in.Identity
	if inv.Scope.CharacterID != in.CharacterID || inv.Scope.Conversation != in.Conversation || inv.Scope.ScopeID != in.Scope || inv.Scope.Channel != in.Audience.Channel || inv.Scope.Scene != in.Audience.Scene {
		return kernel.WorkState{}, nil, artifact.ErrUnavailable
	}
	if !workEnabled(h.Cfg, inv) {
		return kernel.WorkState{}, nil, artifact.ErrUnavailable
	}
	if p, ok := h.Cfg.Work.(*workPort); ok {
		raw, _ := json.Marshal(struct {
			Model, ModelVersion, System, Tools string
			Workspace                          Workspace
		}{l.b.Model.ID, l.cfg.ModelVersion, l.b.System, toolsDigest(l.b), in.Workspace})
		hash := sha256.Sum256(raw)
		if err := p.service.Open(ctx, inv, hex.EncodeToString(hash[:])); err != nil {
			return kernel.WorkState{}, nil, err
		}
		head, err := p.service.DB.LoadWork(ctx, inv)
		if err != nil {
			return kernel.WorkState{}, nil, err
		}
		if head.Ordinal == 0 && ownerConsoleAudience(in.Audience) && inv.Scope.ActorKind == "owner" && inv.Scope.ScopeID == ConsoleScope {
			legacy, err := p.service.DB.LegacyTodo(ctx, inv.Scope)
			if err != nil {
				return kernel.WorkState{}, nil, err
			}
			if legacy != "" {
				if err := p.service.Record(ctx, inv, workcontext.Entry{Delta: todoDelta(legacy), Tokens: sumikaTokens(legacy), Boundary: true}); err != nil {
					return kernel.WorkState{}, nil, err
				}
			}
		}
	}
	return h.Cfg.Work.Load(ctx, inv)
}
func toolsDigest(b *Built) string { return tools.FixedJSON(b.Decls) }
func (p *workPort) Load(ctx context.Context, inv kernel.Invocation) (kernel.WorkState, []Msg, error) {
	state, entries, err := p.service.Load(ctx, inv)
	if err != nil {
		return state, nil, err
	}
	var msgs []Msg
	for _, e := range entries {
		// 这一轮自己的条目不装回来：实例里的尾部才是权威（有参考信息、图片、受控正文），装回来就接两遍。溢出恢复时走到这里
		if inv.RunID != "" && e.RunID == inv.RunID {
			continue
		}
		// 旧计划清单导入的条目没有消息（存成 null）：不是一条要放回历史的消息
		if len(e.Message) == 0 || string(e.Message) == "null" {
			continue
		}
		var m Msg
		if err := json.Unmarshal(e.Message, &m); err != nil {
			return state, nil, err
		}
		msgs = append(msgs, m)
	}
	projected, err := projectWork(msgs, inv.Scope.ScopeID, func(step Msg, c Call, result Msg) error {
		repair := inv
		repair.StepID = step.WorkStepID
		repair.ToolID = c.ToolID
		if err := p.Record(ctx, repair, result); err != nil {
			return err
		}
		state.Unknown = append(state.Unknown, c.Name+"："+result.Text)
		return nil
	})
	if err != nil {
		return state, nil, err
	}
	return state, projected, nil
}
func (p *workPort) Record(ctx context.Context, inv kernel.Invocation, m Msg) error {
	if m.Role == "system" || m.Kind == KindContext || m.Kind == KindSummary || m.Kind == KindWork {
		return nil
	}
	// 思考及受控来源的正文不回灌；当前版本由来源重新接纳。
	if m.WorkStepID == "" {
		m.WorkStepID = inv.StepID
	}
	if m.Role == "toolResult" && m.ToolID == "" {
		m.ToolID = inv.ToolID
	}
	m.Thinking, m.Replay, m.HasThinking, m.Usage = "", chat.Replay{}, false, nil
	controlled := m.Controlled || m.ControlledText != "" || m.Acceptable != nil || m.OnAccepted != nil
	if controlled {
		m.Text = "受控资料需按当前来源版本重新读取。"
		m.Ref = nil
	}
	m.Controlled, m.ControlledText, m.Acceptable, m.OnAccepted, m.Disposition = false, "", nil, nil, nil
	d := workcontext.Delta{}
	if m.Role == "user" && m.Kind == KindSaid {
		d.Goal = m.Text
	}
	if m.Role == "toolResult" {
		if m.Ref != nil && m.Ref.Complete {
			d.Evidence = []kernel.Evidence{{Ref: *m.Ref, Start: 0, End: m.Ref.Bytes, Claim: m.ToolName}}
		}
		if m.IsError {
			d.Unknown = []string{m.ToolName + "：" + m.Text}
		}
	}
	if m.ToolName == "todo_write" && !m.IsError && m.TodoArgs != "" {
		d = todoDelta(m.TodoArgs)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	planned := len(m.Calls)
	if m.Stop != "" {
		planned = 0
	}
	boundary := m.Role == "user" || m.Role == "toolResult" && m.GroupEnd || m.Role == "assistant" && planned == 0
	return p.service.Record(ctx, inv, workcontext.Entry{Message: b, Delta: d, Tokens: sumikaTokens(m.sumikaText()) + imageTokens(m) + 4, Boundary: boundary, Group: m.WorkStepID, Calls: planned, Result: m.Role == "toolResult"})
}
func (p *workPort) Save(ctx context.Context, inv kernel.Invocation, revision uint64, state kernel.WorkState) error {
	head, err := p.service.DB.LoadWork(ctx, inv)
	if err != nil {
		return err
	}
	digest, err := p.service.DB.WorkDigest(ctx, inv, state.ThroughOrdinal)
	if err != nil {
		return err
	}
	return p.service.Install(ctx, inv, workcontext.Candidate{State: state, Expected: revision, Stamp: head.Stamp, Digest: digest})
}
func recordWork(ctx context.Context, cfg Config, m Msg) error {
	if !workEnabled(cfg, cfg.invocation) {
		return nil
	}
	if err := cfg.Work.Record(ctx, cfg.invocation, m); err != nil {
		return fmt.Errorf("工作材料保存失败；未继续派发：%w", err)
	}
	return nil
}
func (h *Hub) compactWork(ctx context.Context, in Input) (bool, error) {
	if !workEnabled(h.Cfg, in.Identity) {
		return false, nil
	}
	p, ok := h.Cfg.Work.(*workPort)
	if !ok {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(h.Cfg.WorkSettings.CompactTimeoutMs)*time.Millisecond)
	defer cancel()
	c, err := p.service.Candidate(ctx, in.Identity)
	if err != nil {
		return false, err
	}
	if c.State.ThroughOrdinal == 0 {
		return false, nil
	}
	err = p.service.Install(ctx, in.Identity, c)
	if errors.Is(err, store.ErrWorkConflict) {
		return false, err
	}
	return err == nil, err
}
func todoDelta(raw string) workcontext.Delta {
	d := workcontext.Delta{ReplaceTodo: true}
	var args struct {
		Todos []struct{ Content, Status string }
	}
	if json.Unmarshal([]byte(raw), &args) != nil {
		d.Unknown = []string{"旧计划清单无法解析"}
		return d
	}
	for _, item := range args.Todos {
		if item.Status == "completed" {
			d.Completed = append(d.Completed, item.Content)
		} else {
			d.Pending = append(d.Pending, item.Content)
		}
	}
	d.Next = strings.Join(d.Pending, "；")
	return d
}

func withoutResultRead(decls []tools.Declaration) []tools.Declaration {
	out := decls[:0]
	for _, d := range decls {
		if d.Name != "result_read" {
			out = append(out, d)
		}
	}
	return out
}

// takeMaterials 只在安全边界读取已持久接纳的资料；重建从同一来源表恢复。
func (h *Hub) takeMaterials(ctx context.Context, cfg Config, l *Live, in kernel.Invocation) error {
	if cfg.Materials == nil {
		return nil
	}
	materials, err := cfg.Materials(ctx, in)
	if err != nil {
		return err
	}
	for _, material := range materials {
		seen := false
		for _, old := range l.msgs {
			if old.MaterialID == material.MaterialID {
				seen = true
				break
			}
		}
		if !seen {
			l.msgs = append(l.msgs, material)
		}
	}
	return nil
}
