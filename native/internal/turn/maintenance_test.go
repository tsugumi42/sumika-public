package turn

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/kernel"
)

// 恢复独占的是会话准入；正在等模型、排队和邻会话都必须经过真实入口验证。
func TestLocalMaintenanceAdmission(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	var held atomic.Bool
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, onBody: func(ctx context.Context, body string) {
		if strings.Contains(body, "编造阻塞工作") && held.CompareAndSwap(false, true) {
			entered <- struct{}{}
			<-ctx.Done()
		}
	}})
	first := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "编造阻塞工作", "maintenance-first", kernel.InputStart)
	rec, err := h.hub.Submit(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if release, err := h.hub.AcquireLocalMaintenance(t.Context(), first.Input.Identity.Scope); !errors.Is(err, ErrInputBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("活动模型未阻止回退：%v", err)
	}
	if _, err := h.hub.StopRun(t.Context(), kernel.StopRequest{Scope: first.Input.Identity.Scope, RunID: rec.RunID, ThroughSequence: rec.Sequence}); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, h.hub, first.Input.Identity.Scope, rec.RunID)
	release, err := h.hub.AcquireLocalMaintenance(t.Context(), first.Input.Identity.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	queued := lifecycleSubmission(t, h, first.Input.Conversation, "编造被拦输入", "maintenance-queued", kernel.InputQueue)
	if _, err := h.hub.Submit(t.Context(), queued); !errors.Is(err, ErrInputBusy) {
		t.Fatalf("恢复期间排队输入穿过准入：%v", err)
	}
	neighbor := lifecycleSubmission(t, h, first.Input.Conversation+":topic:neighbor", "编造邻会话", "maintenance-neighbor", kernel.InputStart)
	near, err := h.hub.Submit(t.Context(), neighbor)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, h.hub, neighbor.Input.Identity.Scope, near.RunID)
	release()
	after := lifecycleSubmission(t, h, first.Input.Conversation, "编造恢复完成", "maintenance-after", kernel.InputStart)
	done, err := h.hub.Submit(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, h.hub, after.Input.Identity.Scope, done.RunID)
}
