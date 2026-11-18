package memory

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/embed"
)

// engineEmbed 是模型名自己定的 statusEmbed，另外数一共算了几条条目（Embed 被问了几次）
type engineEmbed struct {
	*statusEmbed
	model string
	calls atomic.Int32
}

func (e *engineEmbed) Model() string { return e.model }
func (e *engineEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls.Add(1)
	return e.statusEmbed.Embed(ctx, texts)
}

func newEngineEmbed(model string, status embed.Status) *engineEmbed {
	return &engineEmbed{statusEmbed: &statusEmbed{status: status}, model: model}
}

// 规格-Go本地模型 2.7 第一种切换（开发路径，生产不会走到；GM7 验收 4）：库里是 Python 算的 gte 向量，换成 Go 引擎的 gte（对外同一个模型名），
// 向量按 model、VectorPrompt、内容指纹判过期，不看引擎：打开以后一条都不缺、新引擎一次也没被要去算条目的向量、日志里没有「向量补齐」。
// 对照：换成别的模型名（生产的第一次切换是 Qwen → gte）同样的做法要全部重算，日志里有「向量补齐 3 条（模型 …）」——证明上面的断言不是空的
func TestSwitchEngineKeepsVectors(t *testing.T) {
	t.Parallel()
	const gte = "gte-multilingual-base"
	file := readOnlyFixture(t)
	open := func(e *engineEmbed) (*Memory, *logSink) {
		sink := &logSink{}
		m, err := Open(context.Background(), file, e, nil, Options{Character: "suzune", Logf: sink.logf})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		return m, sink
	}
	python := newEngineEmbed(gte, embed.StatusReady)
	ma, _ := open(python)
	waitFilled(t, ma, 3)
	ma.warmSentences(context.Background()) // 守卫用的句向量也补齐：两种引擎之间要比的只有条目的向量，句向量别混进来
	if err := ma.Close(); err != nil {
		t.Fatal(err)
	}
	if python.calls.Load() == 0 {
		t.Fatal("测试前提：第一次打开该用 Python 引擎把向量补齐")
	}

	t.Run("同一个模型名，换了引擎：不重算", func(t *testing.T) {
		goEngine := newEngineEmbed(gte, embed.StatusStarting) // 打开的时候嵌入还没就绪（Open 不同步向量），就绪以后才排补向量那一趟
		m, sink := open(goEngine)
		goEngine.set(func(e *statusEmbed) { e.status = embed.StatusReady })
		m.EmbeddingReady() // 新引擎就绪：给补向量排一趟（没有缺的，什么也不该算）
		waitVectors(t, m)
		if miss, err := m.index.MissingVectors(m.Items()); err != nil || miss != 0 {
			t.Fatalf("换引擎后该一条都不缺：缺 %d %v", miss, err)
		}
		if n := goEngine.calls.Load(); n != 0 {
			t.Fatalf("新引擎不该被要去算条目的向量：算了 %d 次", n)
		}
		if strings.Contains(sink.String(), "向量补齐") {
			t.Fatalf("日志里不该有「向量补齐」：\n%s", sink.String())
		}
		if hits := recallHere(m); len(hits) != 1 || hits[0].Vector == 0 {
			t.Fatalf("旧向量该照样用来召回：%+v", hits)
		}
	})
	t.Run("对照：换了模型名，全部重算", func(t *testing.T) {
		other := newEngineEmbed("qwen3-embedding-0.6b-local", embed.StatusStarting)
		m, sink := open(other)
		other.set(func(e *statusEmbed) { e.status = embed.StatusReady })
		m.EmbeddingReady()
		waitFilled(t, m, 3)
		waitVectors(t, m)
		if n := strings.Count(sink.String(), "向量补齐 3 条（模型 qwen3-embedding-0.6b-local）"); n != 1 {
			t.Fatalf("换模型该有一行「向量补齐 3 条」：%d 行\n%s", n, sink.String())
		}
		if other.calls.Load() == 0 {
			t.Fatal("换模型该由新模型重算")
		}
	})
}
