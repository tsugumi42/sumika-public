package turn

import (
	"context"

	"sumika-native/internal/kernel"
)

// Running 报这个会话现在有没有一轮在跑或排着队。宿主自动发下一轮（/goal 续轮、定期任务）之前问它。
func (h *Hub) Running(characterID, conversation string) bool {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	domain := convKey(characterID, conversation)
	return lc.active[domain] != nil || len(lc.pending[domain]) > 0
}

// StoredSend 描述宿主自动发的一轮：这句话已由宿主存成了事件 Seqs（它不是主人说的话），内核不再存一遍；
// ReplyOrigin 是她这一轮的话存库用的来源名（空是 character）。
type StoredSend struct {
	Seqs        []int64
	ReplyOrigin string
	Auto        bool // 宿主自动发的轮（目标续轮、定期任务）：额度走顶层那组
}

// SendLocalStored 和 SendLocal 一样由可信的桌面宿主调用，区别是这句话已经存好：
// 内核按"已入库的一批"处理，不把它存成主人亲口说的。身份仍是主人在本地私聊的那一档，工具和审批照旧。
func (h *Hub) SendLocalStored(ctx context.Context, characterID, conversation, text string, s StoredSend, draft func(LocalDraft)) <-chan Outcome {
	in := consoleInput(characterID, 0, text)
	in.Conversation, in.Audience.Conversation = conversation, conversation
	in.Seqs, in.ReplyOrigin, in.Auto = append([]int64(nil), s.Seqs...), s.ReplyOrigin, s.Auto
	in.requestCtx, in.onDraft = ctx, draft
	return h.send(in)
}

// ActiveRunID 交出这个会话正在跑的那一轮的编号；没有在跑的（空闲，或只是排着）返回空。
// 宿主要把主人的话插进正在跑的自动轮时，用它当 ExpectedRunID。
func (h *Hub) ActiveRunID(characterID, conversation string) string {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if r := lc.active[convKey(characterID, conversation)]; r != nil && r.snapshot.State == kernel.RunRunning {
		return r.input.Identity.RunID
	}
	return ""
}

// SteerPending 是这一轮有没有收下了、还没送到她眼前的插话（下一次请求之前才送）。
// 工具要等主人时先看它：不然工具等主人，插话等工具（规格-向主人提问 第四节）
func (h *Hub) SteerPending(runID string) bool {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return len(lc.steers[runID]) > 0
}
