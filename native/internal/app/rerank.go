package app

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// rerankSource 是嵌入后端里能重排的那一头（*embed.Local）；测试里的假嵌入器可以不实现
type rerankSource interface {
	Rerank(ctx context.Context, query string, docs []string) ([]int, []float64, error)
}

// memoryReranker 是交给 memory.Options.Reranker 的重排（规格-记忆写入上线 W1a）。memory.rerank.model 空就是 nil，行为和以前一字不差。
// 记忆比嵌入先打开（Reranker 在 Open 时固定，嵌入晚于记忆就绪），所以这里给一个懒绑定的包装：调用时才去看嵌入现在能不能用。
func (a *App) memoryReranker() memory.Reranker {
	rc := a.cfg.Memory.Rerank
	if rc.Model == "" {
		return nil
	}
	return lazyReranker{app: a, timeout: time.Duration(rc.TimeoutSeconds * float64(time.Second))}
}

// lazyReranker 只在明确回忆（memory_search）里被调到（recall.go 的 reorder 前有 ModeExplicit 的条件）。
// 它的时限是重排自己的（memory.rerank.timeoutSeconds，默认 5 秒），不是 RerankClient 的 30 秒：重排慢时 memory_search 不该把整轮卡住。
// 超时回的错里有 context.DeadlineExceeded，reorder 认成"超时"。
type lazyReranker struct {
	app     *App
	timeout time.Duration
}

func (r lazyReranker) Rerank(ctx context.Context, query string, docs []string) ([]int, []float64, error) {
	a := r.app
	rs, ok := a.embedding.(rerankSource)
	if !ok {
		return nil, nil, embed.ErrUnavailable
	}
	if status, _ := a.embedding.Status(); status != embed.StatusReady {
		return nil, nil, embed.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	order, scores, err := rs.Rerank(ctx, query, docs)
	if errors.Is(err, embed.ErrRerankModelUnknown) {
		a.noteRerankModelUnknown(err)
	}
	return order, scores, err
}

// noteRerankModelUnknown：memory.rerank.model 写的 id 嵌入服务里没有，提醒一次（同一个键只留一条）。
// 嵌入和守卫不受影响；这只让 memory_search 照原来的顺序。放在第一次真的用到重排时，不放在启动时：服务就绪要等一阵
func (a *App) noteRerankModelUnknown(cause error) {
	a.alerts.Raise(alert.Alert{
		Key: "memory:rerank-model-unknown", Class: "memory", Title: "记忆重排没有生效",
		Body: cause.Error() + "；memory_search 照原来的顺序，嵌入和发送前检查不受影响。改好 memory.rerank.model（或留空）后重启后台。",
	})
}
