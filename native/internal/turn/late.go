package turn

import (
	"sync"

	"sumika-native/internal/llm"
)

// lateBatch 是一批晚到的图：note 是放在图前面的那句说明。
type lateBatch struct {
	note   string
	images []llm.Image
}

// lateQueue 是一轮里还没送到的晚到图。只活在这一轮里（openLate 到 closeLate），不进库：
// 重建历史时原消息上的图会按事件重新取回，补图本身不需要留。
type lateQueue struct {
	mu      sync.Mutex
	batches []lateBatch
}

func (q *lateQueue) take() []lateBatch {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.batches
	q.batches = nil
	return out
}

func (h *Hub) openLate(runID string) *lateQueue {
	q := &lateQueue{}
	h.late.Store(runID, q)
	return q
}

func (h *Hub) closeLate(runID string) {
	if v, ok := h.late.LoadAndDelete(runID); ok && h.Cfg.Logf != nil {
		if left := v.(*lateQueue).take(); len(left) > 0 {
			h.Cfg.Logf("补图没来得及送到 run=%s：这一轮在下一次请求之前结束了", runID)
		}
	}
}

// LateImages 把晚到的图补给正在跑的那一轮：下一次请求之前作为一条 user 消息追加（和插话同一个时机，只追加不改前面）。
// 这一轮已经结束就返回 false，调用方写日志；不新开一轮，也不存事件。
func (h *Hub) LateImages(runID string, images []llm.Image, note string) bool {
	v, ok := h.late.Load(runID)
	if !ok || len(images) == 0 {
		return false
	}
	q := v.(*lateQueue)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.batches = append(q.batches, lateBatch{note: note, images: images})
	return true
}
