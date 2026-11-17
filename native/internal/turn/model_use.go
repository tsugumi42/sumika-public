package turn

// ModelUse 是一轮开始时的实际模型和门槛。发布后不再变，设置页读取不碰 Live 的可变历史。
type ModelUse struct {
	Character, Conversation, Version, Model, Thinking string
	Window, Reserve                                   int
	Soft, Hard                                        float64
}

func modelUse(l *Live) ModelUse {
	b := l.b
	return ModelUse{Character: b.CharacterID, Conversation: b.Conversation, Version: l.cfg.ModelVersion, Model: b.Model.ID, Thinking: b.Effort, Window: b.Window, Reserve: b.Reserve, Soft: b.Soft, Hard: b.Hard}
}
func (h *Hub) ActiveModels() []ModelUse {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ModelUse, 0, len(h.active))
	for _, l := range h.active {
		l.mu.Lock()
		if l.run != nil {
			out = append(out, l.run.model)
		}
		l.mu.Unlock()
	}
	return out
}
