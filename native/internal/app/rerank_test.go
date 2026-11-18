package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/embed"
	"sumika-native/internal/memory"
)

// rerankLocal 是能重排的假嵌入器：状态和重排函数都由测试给
type rerankLocal struct {
	guardLocal
	rerank func(ctx context.Context, query string, docs []string) ([]int, []float64, error)
	calls  int
}

func (r *rerankLocal) Rerank(ctx context.Context, query string, docs []string) ([]int, []float64, error) {
	r.calls++
	return r.rerank(ctx, query, docs)
}

func rerankApp(backend string, emb LocalEmbedding, model string, timeout float64) *App {
	a := &App{embedding: emb, alerts: alert.New(), logf: func(string, ...any) {}}
	a.cfg.Embed.Backend = backend
	a.cfg.Memory.Rerank.Model, a.cfg.Memory.Rerank.TimeoutSeconds = model, timeout
	return a
}

// memory.rerank.model 空 = 不重排（Options.Reranker 是 nil，行为和以前一字不差）；写了才有包装
func TestMemoryRerankerOnlyWhenConfigured(t *testing.T) {
	t.Parallel()
	if r := rerankApp("go-models", nil, "", 5).memoryReranker(); r != nil {
		t.Fatalf("没写重排模型不该有重排：%v", r)
	}
	if r := rerankApp("go-models", nil, "编的重排", 5).memoryReranker(); r == nil {
		t.Fatal("写了重排模型该有重排包装")
	}
}

// 会重排的后端（GM7 接 go-models；这里直接把后端字段写成别的值）：嵌入没就绪、嵌入器没有重排能力都回 ErrUnavailable，不去调；
// 就绪了才真去调，并且用重排自己的时限，慢的重排不会拖住 memory_search
func TestLazyRerankerLazyBindingAndOwnTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &rerankLocal{guardLocal: guardLocal{status: embed.StatusStarting}}
	fake.rerank = func(ctx context.Context, q string, docs []string) ([]int, []float64, error) {
		if _, has := ctx.Deadline(); !has {
			t.Error("重排调用该带自己的时限")
		}
		return []int{1, 0}, []float64{2, 1}, nil
	}
	r := rerankApp("go-models", fake, "编的重排", 5).memoryReranker()
	if _, _, err := r.Rerank(ctx, "q", []string{"a", "b"}); !errors.Is(err, embed.ErrUnavailable) || fake.calls != 0 {
		t.Fatalf("嵌入没就绪：该回 ErrUnavailable 且不去调：%v，%d 次", err, fake.calls)
	}
	fake.status = embed.StatusReady
	order, scores, err := r.Rerank(ctx, "q", []string{"a", "b"})
	if err != nil || len(order) != 2 || order[0] != 1 || len(scores) != 2 || fake.calls != 1 {
		t.Fatalf("就绪以后该真去调：%v %v %v，%d 次", order, scores, err, fake.calls)
	}
	// 没有重排能力的嵌入器（普通的 guardLocal）
	if _, _, err := rerankApp("go-models", &guardLocal{status: embed.StatusReady}, "编的重排", 5).memoryReranker().Rerank(ctx, "q", []string{"a"}); !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("没有重排能力：%v", err)
	}
	// 慢的重排：时限 50 毫秒，卡住的调用到点就放手，错里有 DeadlineExceeded（reorder 认成"超时"）
	fake.rerank = func(ctx context.Context, q string, docs []string) ([]int, []float64, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	slow := rerankApp("go-models", fake, "编的重排", 0.05).memoryReranker()
	t0 := time.Now()
	_, _, err = slow.Rerank(ctx, "q", []string{"a", "b"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > 2*time.Second {
		t.Fatalf("该在 50 毫秒的自己的时限到点返回 DeadlineExceeded：%v，用了 %v", err, time.Since(t0))
	}
}

// 装配：写了 memory.rerank.model → 记忆的重排口子接上了包装：明确回忆走到它，每轮自动想起不重排
func TestOpenWiresReranker(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	file := guardFixture(t, e, true)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO facts VALUES ('101', 'fact', 'active', '主人的备用电池是五号的', 'owner', 'owner', '', 1, 1, NULL, NULL)`,
		`INSERT INTO facts VALUES ('102', 'fact', 'active', '主人说电池要放在干燥的地方', 'owner', 'owner', '', 1, 1, NULL, NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	cfgFile := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	extra := `
memory:
  rerank:
    model: 编的重排
    k: 7
`
	if err := os.WriteFile(cfgFile, append(b, extra...), 0600); err != nil {
		t.Fatal(err)
	}
	opt := e.options()
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		return &rerankLocal{guardLocal: guardLocal{cfg: cfg}, rerank: func(_ context.Context, _ string, docs []string) ([]int, []float64, error) {
			order := make([]int, len(docs))
			scores := make([]float64, len(docs))
			for i := range docs {
				order[i] = len(docs) - 1 - i
				scores[i] = float64(len(docs) - i)
			}
			return order, scores, nil
		}}
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	m := a.memories[testChar]
	owner := memory.Audience{Channel: "console", Conversation: "console:编的", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}
	r := m.Recall(context.Background(), owner, memory.Query{Text: "电池", Mode: memory.ModeExplicit, TopK: 12})
	if !r.Reranked || r.RerankSkipped != "" {
		t.Fatalf("明确回忆该走到重排包装：%v %q", r.Reranked, r.RerankSkipped)
	}
	if r := m.Recall(context.Background(), owner, memory.Query{Text: "电池", Mode: memory.ModeNormal, Addressed: true}); r.RerankSkipped != "" {
		t.Fatalf("每轮自动想起不该走重排：%q", r.RerankSkipped)
	}
}

// go-models 后端起来了，但 memory.rerank.model 写的 id 服务里没有：重排回 rerank-model-unknown，
// app 提醒一条（同一个键只留一条），错误原样交回去，让记忆照原顺序并写原因码
func TestLazyRerankerModelUnknownRaisesAlert(t *testing.T) {
	t.Parallel()
	fake := &rerankLocal{guardLocal: guardLocal{status: embed.StatusReady}}
	fake.rerank = func(context.Context, string, []string) ([]int, []float64, error) {
		return nil, nil, errors.Join(embed.ErrRerankModelUnknown, errors.New("memory.rerank.model 写的 bge-typo 服务里没有"))
	}
	a := rerankApp("go-models", fake, "bge-typo", 5)
	r := a.memoryReranker()
	for i := 0; i < 2; i++ {
		if _, _, err := r.Rerank(context.Background(), "q", []string{"a", "b"}); !errors.Is(err, embed.ErrRerankModelUnknown) {
			t.Fatalf("错误该原样交回去：%v", err)
		}
	}
	open := a.alerts.Open()
	if len(open) != 1 || open[0].Key != "memory:rerank-model-unknown" || !strings.Contains(open[0].Body, "bge-typo") {
		t.Fatalf("该有一条提醒，写明是哪个 id：%+v", open)
	}
	// 别的错误不提醒
	fake2 := &rerankLocal{guardLocal: guardLocal{status: embed.StatusReady}}
	fake2.rerank = func(context.Context, string, []string) ([]int, []float64, error) {
		return nil, nil, errors.New("编的别的错")
	}
	b := rerankApp("go-models", fake2, "bge-x", 5)
	b.memoryReranker().Rerank(context.Background(), "q", []string{"a", "b"})
	if open := b.alerts.Open(); len(open) != 0 {
		t.Fatalf("别的错误不该提醒这一条：%+v", open)
	}
}
