package linktranslate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 第一次调用超时，按 Retries 重新计时再试一次就拿到译文；不再试的话只发原文。
func TestTranslateRetriesOnceAfterTimeout(t *testing.T) {
	var n atomic.Int32
	call := func(ctx context.Context, _, _ string) (string, error) {
		if n.Add(1) == 1 {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "阿尔忒弥斯二号完成了绕月飞行。", nil
	}
	tr := &Translator{Call: call, Timeout: 50 * time.Millisecond, Retries: 1}
	o := tr.Translate(context.Background(), "u", en, "简体中文")
	if o.Status != StatusOK || o.Text == "" || n.Load() != 2 {
		t.Fatalf("重试一次应拿到译文：%+v，调用 %d 次", o, n.Load())
	}

	n.Store(0)
	tr = &Translator{Call: call, Timeout: 50 * time.Millisecond}
	if o := tr.Translate(context.Background(), "u2", en, "简体中文"); o.Status != StatusTimeout || n.Load() != 1 {
		t.Fatalf("Retries 0 不重试：%+v，调用 %d 次", o, n.Load())
	}
}

// 只有超时才重试：模型报错、外层取消都不再调第二次。
func TestTranslateRetryOnlyOnTimeout(t *testing.T) {
	var n atomic.Int32
	tr := &Translator{Call: func(context.Context, string, string) (string, error) { n.Add(1); return "", errors.New("坏了") }, Timeout: time.Second, Retries: 2}
	if o := tr.Translate(context.Background(), "u", en, "简体中文"); o.Status != StatusFailed || n.Load() != 1 {
		t.Fatalf("报错不重试：%+v，调用 %d 次", o, n.Load())
	}
	n.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	tr = &Translator{Call: func(c context.Context, _, _ string) (string, error) { n.Add(1); <-c.Done(); return "", c.Err() }, Timeout: time.Second, Retries: 2}
	// 外层到期也记成超时（timeout），不记成模型报错
	if o := tr.Translate(ctx, "u", en, "简体中文"); o.Text != "" || n.Load() != 1 || o.Status != StatusTimeout || o.Reason != "timeout" {
		t.Fatalf("外层到期不重试、记超时：%+v，调用 %d 次", o, n.Load())
	}
	// 外层被取消：不重试，记 canceled
	n.Store(0)
	cctx, stop := context.WithCancel(context.Background())
	tr = &Translator{Call: func(c context.Context, _, _ string) (string, error) { n.Add(1); stop(); <-c.Done(); return "", c.Err() }, Timeout: time.Second, Retries: 2}
	if o := tr.Translate(cctx, "u3", en, "简体中文"); o.Text != "" || n.Load() != 1 || o.Status != StatusFailed || o.Reason != "canceled" {
		t.Fatalf("外层取消：%+v，调用 %d 次", o, n.Load())
	}
}
