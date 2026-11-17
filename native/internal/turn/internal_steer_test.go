package turn

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/kernel"
)

// 子运行跑到一半收到追加指示：下一次模型请求里带有这段话和"上级追加"的前缀，不带"主人亲口说"的前缀。
func TestSteerInternalReachesNextRequest(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var mu sync.Mutex
	var bodies []string
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `,` + textReply + `]`,
		mutate: func(cfg *Config) {
			cfg.InternalAuthorize = func(context.Context, kernel.Invocation, string) error { return nil }
		},
		onBody: func(ctx context.Context, body string) {
			if !strings.Contains(body, "编造追加指示任务") {
				return
			}
			mu.Lock()
			bodies = append(bodies, body)
			first := len(bodies) == 1
			mu.Unlock()
			if first {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		},
	})
	parent := lifecycleSubmission(t, h, consoleConv(h.c.CharacterID), "编造父运行", "parent", kernel.InputStart)
	rec, err := h.hub.Submit(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	parentRun := inputRunOf(t, h.hub, rec)
	if out := recv(t, parentRun.outcome, "父运行完成"); out.Err != nil {
		t.Fatal(out.Err)
	}
	inv := parentRun.input.Identity
	inv.Scope.Conversation = "internal:steer-child"
	inv.Scope.AuthorityID, inv.Scope.AuthorityRevision = "made-authority", 1
	inv.Kind, inv.RunID, inv.ParentRunID = kernel.KindChild, "made-steer-child", rec.RunID
	inv.RootID = inv.RunID
	limits := h.hub.Cfg.BudgetConfig.Limits(time.Now())
	if err := AdmitBudgetRoot(t.Context(), h.hub.Cfg.Budget, inv, limits); err != nil {
		t.Fatal(err)
	}
	handle, err := h.hub.StartInternal(t.Context(), InternalRun{Identity: inv, Goal: "编造追加指示任务", ModelID: h.c.Characters[h.c.CharacterID].Model, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	recv(t, entered, "子运行第一次请求")

	// 拒绝的情形：别的会话身份、不存在的运行、不是子运行的前台运行
	other := inv.Scope
	other.Conversation = "internal:other"
	if err := h.hub.SteerInternal(other, inv.RunID, "夺权"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("别的会话身份给子运行追加了指示：%v", err)
	}
	if err := h.hub.SteerInternal(inv.Scope, "no-such-run", "x"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("不存在的运行：%v", err)
	}
	if err := h.hub.SteerInternal(parentRun.input.Identity.Scope, rec.RunID, "x"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("前台运行被当成子运行：%v", err)
	}

	if err := h.hub.SteerInternal(inv.Scope, inv.RunID, "改查第二份资料"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if out := recv(t, handle.Done, "子运行完成"); out.Err != nil {
		t.Fatal(out.Err)
	}
	mu.Lock()
	got := append([]string(nil), bodies...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("追加指示后没有下一次请求：%d", len(got))
	}
	if strings.Contains(got[0], "改查第二份资料") {
		t.Fatal("指示在送到之前就出现在请求里")
	}
	if !strings.Contains(got[1], "改查第二份资料") || !strings.Contains(got[1], "派你做这个任务的上级") {
		t.Fatal("下一步请求里没有带前缀的追加指示")
	}
	if strings.Contains(got[1], "所有者亲口说的") {
		t.Fatal("子任务的追加指示被写成主人亲口说的")
	}
	// 已收尾：报已结束
	if err := h.hub.SteerInternal(inv.Scope, inv.RunID, "晚了"); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("已结束的子运行：%v", err)
	}
}
