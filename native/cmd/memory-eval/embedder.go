package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// evalEmbedder 套在嵌入插口外面：数算了几次、多少条、多少维、一共多久（拿几次的结果并排比要看这些），
// 连的是本地模型服务时还要说清楚"这句是查询还是库存"（input_type，规格-本地模型服务 3.3、六.3）
type evalEmbedder struct {
	inner embed.Embedder
	typed embed.Typed // 内层能带 input_type 时用它；只有 Embedder 的（假嵌入器）就没它
	// false：Open 期间，算的是条目（document）；true：Open 以后，算的都是查询（query）。
	// memory-eval 只读打开（memory.Options{}），Open 里那一次 SyncVectors 是唯一一批条目，之后没有写入那一侧的补齐协程
	query atomic.Bool

	mu    sync.Mutex
	calls int
	texts int
	dim   int
	total time.Duration
}

func newEvalEmbedder(inner embed.Embedder) *evalEmbedder {
	e := &evalEmbedder{inner: inner}
	if t, ok := inner.(embed.Typed); ok {
		e.typed = t
	}
	return e
}

// setQuery 在 Open 返回以后调：之后的嵌入都按查询算
func (e *evalEmbedder) setQuery() { e.query.Store(true) }

func (e *evalEmbedder) Model() string { return e.inner.Model() }

func (e *evalEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	t0 := time.Now()
	var (
		vs  [][]float32
		err error
	)
	if e.typed != nil {
		kind := embed.TypeDocument
		if e.query.Load() {
			kind = embed.TypeQuery
		}
		vs, err = e.typed.EmbedTyped(ctx, kind, texts)
	} else {
		vs, err = e.inner.Embed(ctx, texts)
	}
	// 失败的也算进总耗时：要的是"这次花在嵌入上多久"
	e.mu.Lock()
	e.calls++
	e.texts += len(texts)
	e.total += time.Since(t0)
	if e.dim == 0 && len(vs) > 0 {
		e.dim = len(vs[0])
	}
	e.mu.Unlock()
	return vs, err
}

// head 是输出开头"嵌入："后面那一句：用的哪个模型、多少维
func (e *evalEmbedder) head() string {
	e.mu.Lock()
	dim := "维度未知"
	if e.dim > 0 {
		dim = fmt.Sprintf("%d 维", e.dim)
	}
	e.mu.Unlock()
	return fmt.Sprintf("%s（%s）", e.inner.Model(), dim)
}

// line 是结尾那句：算了几次、多少条、一共多久（几次的结果并排比要看这个；整个跑完才算得准）
func (e *evalEmbedder) line() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("调用 %d 次 %d 条，一共 %d ms", e.calls, e.texts, e.total.Milliseconds())
}

// stats 写进结果 JSON，好把几次的模型、维度、耗时并排看
func (e *evalEmbedder) stats() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return map[string]any{"model": e.inner.Model(), "dim": e.dim, "calls": e.calls, "texts": e.texts, "ms": e.total.Milliseconds()}
}

// evalReranker 套在重排插口外面：数调了几次、送了多少条、一共多久（几次的结果并排比要看这些）。
// 没排成退回原顺序的次数记在 Recalled.RerankSkipped 里（「顺序不对」是召回那边判的，这里看不见）
type evalReranker struct {
	inner memory.Reranker
	model string

	mu    sync.Mutex
	calls int
	docs  int
	total time.Duration
}

func newEvalReranker(inner memory.Reranker, model string) *evalReranker {
	return &evalReranker{inner: inner, model: model}
}

func (e *evalReranker) Rerank(ctx context.Context, query string, docs []string) ([]int, []float64, error) {
	t0 := time.Now()
	order, scores, err := e.inner.Rerank(ctx, query, docs)
	// 失败的也算进总耗时：要的是"这次花在重排上多久"
	e.mu.Lock()
	e.calls++
	e.docs += len(docs)
	e.total += time.Since(t0)
	e.mu.Unlock()
	return order, scores, err
}

// head 是输出开头"重排："后面那一句：用的哪个模型（给的 k 在调用处另写）
func (e *evalReranker) head() string { return e.model }

// line 是结尾那句：调了几次、送了多少条、一共多久
func (e *evalReranker) line() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("调用 %d 次 %d 条，一共 %d ms", e.calls, e.docs, e.total.Milliseconds())
}

// stats 写进结果 JSON，好把几次的模型、条数、耗时并排看
func (e *evalReranker) stats() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return map[string]any{"model": e.model, "calls": e.calls, "docs": e.docs, "ms": e.total.Milliseconds()}
}
