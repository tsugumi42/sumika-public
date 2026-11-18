package memory

import (
	"context"
	"errors"
	"log"

	"sumika-native/internal/embed"
)

// RerankK 是重排看多少条（起步值，Options.RerankK 可改）：合并排序以后先截出这么多，交给 Reranker 换顺序，
// 然后才按字数装进这一轮（规格-记忆-v2 第七节，2026-10-28 加）。和 Candidates 的 40 一样，
// 不然「加重排」和「多看几条」混在一起（盘点-本地嵌入和重排模型 第 4 节）
const RerankK = 40

// 重排没排成的原因码（Recalled.RerankSkipped 和日志里用，不写正文）
const (
	rerankTimeout = "超时"
	rerankFailed  = "出错"
	rerankOrder   = "顺序不对"
	// 嵌入还没就绪（embed.ErrUnavailable）：不是重排坏了，等就绪了就正常
	rerankUnready = "未就绪"
)

// Reranker 是「给一句查询和这 K 条候选的正文，返回新顺序和分数」的插口（可选，nil 就是不重排）。
// order 是 docs 的下标（0 起），必须是 0..len(docs)-1 的一个排列；scores 和 order 一一对应，越大越相关（不够长按 0）。
// 出错、超时、返回的不是排列：这一轮退回原来的顺序，记一行日志，不让人这一轮失败。
// 它只该改顺序（规格-记忆-v2 第七节：不加不减条目、不改可见集合）
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []string) (order []int, scores []float64, err error)
}

// logf 写一行日志：Options.Logf 为空就用 log.Printf（只写计数、原因码和 ref，不写正文）
func (m *Memory) logf(format string, args ...any) {
	if m.opt.Logf != nil {
		m.opt.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// rerankPool 是这次重排看多少条：Options.RerankK 为零就是 RerankK
func (m *Memory) rerankPool() int {
	if m.opt.RerankK > 0 {
		return m.opt.RerankK
	}
	return RerankK
}

// reorder 用重排把前 K 条重新排一遍：只改顺序，不加不减，后面超出 K 的照原样接上。
// 第二个返回值是这一轮真的换了顺序；第三个是没排成的原因码（没配重排、候选不够两条时两者都为零值）。
// 没排成时返回的仍是原来那份顺序，调用方直接接着用
func (m *Memory) reorder(ctx context.Context, query string, cands []Hit) ([]Hit, bool, string) {
	r := m.opt.Reranker
	if r == nil {
		return cands, false, ""
	}
	n := min(len(cands), m.rerankPool())
	if n < 2 {
		return cands, false, ""
	}
	docs := make([]string, n)
	for i := range docs {
		docs[i] = cands[i].Text
	}
	order, scores, err := r.Rerank(ctx, query, docs)
	if err != nil || !isPermutation(order, n) {
		why := rerankOrder
		switch {
		case errors.Is(err, embed.ErrRerankModelUnknown):
			// memory.rerank.model 写的 id 服务里没有：每次 memory_search 都一样，原因码照写进 RerankSkipped，
			// 日志只在第一次写一行（id 不对由 app 提醒）
			why = embed.ErrRerankModelUnknown.Error()
			if !m.rerankNoted.CompareAndSwap(false, true) {
				return cands, false, why
			}
		case errors.Is(err, embed.ErrUnavailable):
			why = rerankUnready
		case err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)):
			why = rerankTimeout
		case err != nil:
			why = rerankFailed
		}
		m.logf("记忆[%s]：重排没用上（原因 %s），这一轮按原来的顺序", m.opt.Character, why)
		return cands, false, why
	}
	out := make([]Hit, 0, len(cands))
	for i, idx := range order {
		h := cands[idx]
		if i < len(scores) {
			h.Rerank = scores[i]
		}
		out = append(out, h)
	}
	return append(out, cands[n:]...), true, ""
}

// isPermutation 判重排回来的 order 是不是 0..n-1 的一个排列：重复、越界、少一条都不行（不然「只改顺序」做不到）
func isPermutation(order []int, n int) bool {
	if len(order) != n {
		return false
	}
	seen := make([]bool, n)
	for _, i := range order {
		if i < 0 || i >= n || seen[i] {
			return false
		}
		seen[i] = true
	}
	return true
}
