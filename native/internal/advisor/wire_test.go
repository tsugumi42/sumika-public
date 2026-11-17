package advisor

// 线上字节对拍（规格-模型对话编解码 设计第 1 条）：顾问审阅的请求体（[]llm.Message、系统提示 + 审阅材料）。
// 重录：先在 testdata/wire/intended.md 登记，再 WIRE_UPDATE=1 跑。

import (
	"context"
	"testing"

	bodywire "sumika-native/internal/llm/llmtest/wire"
)

func TestWireAdvisorReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.s.Observe(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	f.drain()
	f.mu.Lock()
	bodies := append([]string(nil), f.bodies...)
	f.mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("审阅应该发一次请求，发了 %d 次", len(bodies))
	}
	bodywire.Check(t, "testdata/wire", "advisor-review", []byte(bodies[0]))
}
