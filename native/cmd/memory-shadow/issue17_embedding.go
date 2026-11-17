package main

import (
	"context"
	"sync"

	"sumika-native/internal/embed"
)

// 生产召回允许嵌入失败后只查全文；验收必须计出失败，避免把降级成绩当成真嵌入成绩。
type issue17MeasuredEmbed struct {
	inner         embed.Embedder
	mu            sync.Mutex
	calls, errors int
}

func (e *issue17MeasuredEmbed) Model() string { return e.inner.Model() }
func (e *issue17MeasuredEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vectors, err := e.inner.Embed(ctx, texts)
	e.mu.Lock()
	e.calls++
	// 关库取消的后台补向量不属于服务失败；实际召回的错误仍计入验收。
	if err != nil && ctx.Err() != context.Canceled {
		e.errors++
	}
	e.mu.Unlock()
	return vectors, err
}
func (e *issue17MeasuredEmbed) stats() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, e.errors
}
