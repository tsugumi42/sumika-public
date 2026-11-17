package turn

import (
	"fmt"

	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
)

// failoverSelector 是这个 Hub 共用的冷却表：宿主没给时才建，并发回合拿到同一份。
func (h *Hub) failoverSelector(cfg Config) *llm.Failover {
	h.failoverOnce.Do(func() { h.failover = &llm.Failover{Now: cfg.Now} })
	return h.failover
}

// checkUnlistedImages：模型列表里没有的模型能力未知，历史里有图时不能悄悄不带图发出去（规格验收第 7 条）。
func checkUnlistedImages(cfg Config, l *Live, model string) error {
	for _, candidate := range cfg.modelCatalog {
		if candidate.ID == model {
			return nil
		}
	}
	for _, m := range l.msgs {
		if len(m.Images) != 0 {
			return llm.LocalError(fmt.Errorf("模型列表里没有 %s，不知道它能不能看图，不能丢掉这一轮的图片后继续", model))
		}
	}
	return nil
}

func (c Config) fallbacks(character string) []string {
	if chain := c.FallbackModels[character]; chain != nil {
		return chain
	}
	return c.DefaultFallbackModels
}

// clearModelReasoning also runs when a later turn returns to the configured model.
// Ordinary messages and already dispatched tool calls/results remain in place.
func (l *Live) clearModelReasoning(model string) {
	if l.reasoningModel != "" && l.reasoningModel != model {
		for i := range l.msgs {
			m := &l.msgs[i]
			m.Thinking, m.Replay, m.HasThinking = "", chat.Replay{}, false
		}
	}
	l.reasoningModel = model
}

func (l *Live) useFailoverModel(cfg Config, name string) error {
	model := llm.Model{ID: name}
	for _, candidate := range cfg.modelCatalog {
		if candidate.ID == name {
			model = candidate
			break
		}
	}
	if !model.SupportsImages() {
		for _, m := range l.msgs {
			if len(m.Images) != 0 {
				return llm.LocalError(fmt.Errorf("备选模型 %s 未声明支持图片，不能丢弃本步图片后继续", name))
			}
		}
	}
	l.clearModelReasoning(name)
	l.b.refresh(cfg, model)
	l.mu.Lock()
	if l.run != nil {
		l.run.model = modelUse(l)
	}
	l.mu.Unlock()
	return nil
}

func failoverNotice(in Input, turnID string, step int, plan *llm.FailoverPlan) {
	if !in.local() || in.onProcess == nil || plan.Info == nil || plan.Info.PreviousModel == "" {
		return
	}
	in.onProcess(ProcessStep{ID: fmt.Sprintf("model:%s:%d:%s", turnID, step, plan.Model), Step: step,
		Kind: "model", Tool: "模型切换", Target: "模型暂时用不了，换成 " + plan.Model, State: "done"})
}

func discardModelDraft(l *Live, in Input, turnID string, step int) {
	l.mu.Lock()
	l.stepText = ""
	l.mu.Unlock()
	if in.onDraft != nil {
		in.onDraft(LocalDraft{TurnID: turnID, Step: step, Reset: true})
	}
}
