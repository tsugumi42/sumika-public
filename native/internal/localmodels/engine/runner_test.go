package engine

import (
	"context"
	"testing"
	"time"
)

// 会话的 Run 万一 panic，行锁要放开：HTTP 层 recover 以后进程还活着，锁不放，这个模型以后每个请求都排在没人放的锁后面。
func TestRowLockReleasedWhenRunPanics(t *testing.T) {
	spec := smallSpec()
	sess := newEmbedSession(spec.Dimension)
	sess.onRun = func(n int) error {
		if n == 1 {
			panic("会话炸了")
		}
		return nil
	}
	e, _ := newTestEmbedder(t, spec, sess, EmbedderOptions{})
	func() {
		defer func() {
			if recover() == nil {
				t.Error("第一次 Run 该 panic 出来")
			}
		}()
		_, _ = e.Embed(context.Background(), texts("第一句"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := e.Embed(ctx, texts("第二句")); err != nil {
		t.Fatalf("panic 之后行锁该已放开，第二次请求却是：%v", err)
	}
}
