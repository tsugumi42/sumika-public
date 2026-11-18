package subagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

type lifecycleFixture struct {
	m        *Manager
	runner   *fixtureRunner
	lifetime *fixtureLifetime
	a        ecosystem.Access
	scope    kernel.Scope
	parent   kernel.Invocation
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	repo := store.EcosystemStore{DB: set.TraceStore().Trace}
	s := kernel.Scope{CharacterID: "fixture", Conversation: "original-conversation", ScopeID: "owner", Platform: "local", Channel: "console", AccountID: "50001", Scene: "private", ActorKind: "owner", ActorID: "50002", AuthorityID: ecosystem.ID(), AuthorityRevision: 1}
	a, err := ecosystem.NewAuthority().Issue(s, []string{"task:reader", "task:mailbox", "local.read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	parentCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	runner := &fixtureRunner{started: make(chan turn.InternalRun, 8), release: make(chan struct{})}
	lifetime := &fixtureLifetime{ended: make(chan struct{}, 8)}
	results := &fixtureResults{values: map[string]string{}, refs: map[string]kernel.ResultRef{}}
	budget := fixtureBudget{kernel.BudgetSnapshot{Limits: kernel.Limits{MaxRequests: 24, MaxTools: 60, MaxTokens: 524288, MaxOutputTokens: 32768}}}
	m, err := New(Options{Enabled: true, Definitions: Definitions("fixture-model"), Repository: repo, Runner: runner, Lifetime: lifetime, Budget: budget, Results: results, ParentValid: func(context.Context, kernel.Invocation) bool { return parentCtx.Err() == nil }, ParentContext: func(context.Context, kernel.Invocation) (context.Context, error) { return parentCtx, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return &lifecycleFixture{m: m, runner: runner, lifetime: lifetime, a: a, scope: s, parent: kernel.Invocation{Scope: s, Kind: "foreground", RootID: "fixture-root", RunID: "fixture-parent", ToolID: "fixture-tool", Generation: 1}}
}

func (f *lifecycleFixture) start(t *testing.T) Result {
	t.Helper()
	r, err := f.m.Start(t.Context(), f.a, f.parent, Request{Agent: "reader", Description: "只读研究", Prompt: "查资料"})
	if err != nil {
		t.Fatal(err)
	}
	<-f.runner.started
	return r
}

func TestTaskMessageOwnershipAndLifecycle(t *testing.T) {
	f := newLifecycleFixture(t)
	task := f.start(t)

	// 还在跑：指示送进子运行，且按子运行自己的会话找它
	if _, err := f.m.Message(t.Context(), f.a, f.parent, task.TaskID, "改查第二份资料"); err != nil {
		t.Fatal(err)
	}
	f.runner.mu.Lock()
	got := append([]string(nil), f.runner.steered...)
	f.runner.mu.Unlock()
	if len(got) != 1 || !strings.HasSuffix(got[0], "|"+task.TaskID+"|改查第二份资料") || strings.HasPrefix(got[0], "original-conversation|") {
		t.Fatal("追加指示没有送到子运行", got)
	}

	steeredCount := func() int { f.runner.mu.Lock(); defer f.runner.mu.Unlock(); return len(f.runner.steered) }

	// 不存在：说清没有
	if _, err := f.m.Message(t.Context(), f.a, f.parent, "no-such-task", "x"); err == nil || !strings.Contains(err.Error(), "没有这个任务") {
		t.Fatal("不存在的任务应报没有这个任务", err)
	}
	// 别人的：别的会话、别的父运行都不送
	otherScope := f.scope
	otherScope.Conversation = "other-conversation"
	otherScope.AuthorityID = ecosystem.ID()
	otherAccess, err := ecosystem.NewAuthority().Issue(otherScope, []string{"task:reader", "task:mailbox", "local.read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	otherParent := f.parent
	otherParent.Scope = otherScope
	if _, err = f.m.Message(t.Context(), otherAccess, otherParent, task.TaskID, "夺权"); err == nil {
		t.Fatal("别的会话给我的任务发了指示")
	}
	laterRun := f.parent
	laterRun.RunID = "unrelated"
	if _, err = f.m.Message(t.Context(), f.a, laterRun, task.TaskID, "夺权"); err == nil {
		t.Fatal("别的父运行给任务发了指示")
	}
	// 子代理自己（Kind=child）不能发
	child := f.parent
	child.Kind = "child"
	if _, err = f.m.Message(t.Context(), f.a, child, task.TaskID, "x"); err == nil {
		t.Fatal("子运行给兄弟发了指示")
	}
	if _, err = f.m.Message(t.Context(), f.a, f.parent, task.TaskID, "   "); err == nil {
		t.Fatal("空指示被接受")
	}
	if steeredCount() != 1 {
		t.Fatal("被拒的指示不该送到子运行", steeredCount())
	}

	// 已结束：报已结束并提示 task
	close(f.runner.release)
	<-f.lifetime.ended
	_, err = f.m.Message(t.Context(), f.a, f.parent, task.TaskID, "晚了")
	if err == nil || !strings.Contains(err.Error(), "已结束") || !strings.Contains(err.Error(), "task") {
		t.Fatal("已结束的任务应报已结束并提示再派", err)
	}
	if steeredCount() != 1 {
		t.Fatal("已结束的任务收到了指示")
	}
}

func TestTaskResultWait(t *testing.T) {
	f := newLifecycleFixture(t)
	task := f.start(t)

	// 等到超时：任务还在跑，按时返回
	begin := time.Now()
	if err := f.m.Wait(t.Context(), f.a, f.parent, task.TaskID, 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d < 70*time.Millisecond || d > 3*time.Second {
		t.Fatal("超时等待时长不对", d)
	}
	if r, err := f.m.Read(t.Context(), f.a, f.parent, task.TaskID); err != nil || r.Status != "running" {
		t.Fatal(r, err)
	}

	// 等待中被取消：立即返回
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- f.m.Wait(ctx, f.a, f.parent, task.TaskID, time.Minute) }()
	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("被取消的等待应返回错误")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后等待没有立即返回")
	}

	// 等到结束：结束后立即返回，不等满时限
	errc = make(chan error, 1)
	begin = time.Now()
	go func() { errc <- f.m.Wait(t.Context(), f.a, f.parent, task.TaskID, time.Minute) }()
	time.Sleep(50 * time.Millisecond)
	close(f.runner.release)
	<-f.lifetime.ended
	select {
	case err := <-errc:
		if err != nil || time.Since(begin) > 10*time.Second {
			t.Fatal(err, time.Since(begin))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("任务结束后等待没有返回")
	}
	if r, err := f.m.Read(t.Context(), f.a, f.parent, task.TaskID); err != nil || r.Status != "completed" {
		t.Fatal(r, err)
	}

	// 别人的和不存在的：等待不泄露存在与否，和 task_result 一样不可访问
	if err := f.m.Wait(t.Context(), f.a, f.parent, "no-such-task", time.Second); err == nil {
		t.Fatal("不存在的任务不该能等")
	}
}

func TestTaskListOnlyOwn(t *testing.T) {
	f := newLifecycleFixture(t)
	if got, err := f.m.List(t.Context(), f.a, f.parent); err != nil || len(got) != 0 {
		t.Fatal("没派过任务应为空列表", got, err)
	}
	task := f.start(t)
	list, err := f.m.List(t.Context(), f.a, f.parent)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if e := list[0]; e.TaskID != task.TaskID || e.Agent != "reader" || e.Status != "running" || e.Description != "只读研究" || e.StartedAt == "" || !e.ThisRun {
		t.Fatal("列表行内容不对", e)
	}
	// 之后的回合：还能看到，但标明不能操作
	later := f.parent
	later.RunID = "later-run"
	list, err = f.m.List(t.Context(), f.a, later)
	if err != nil || len(list) != 1 || list[0].ThisRun {
		t.Fatal(list, err)
	}
	// 别的会话授权：看不到
	otherScope := f.scope
	otherScope.Conversation = "other-conversation"
	otherScope.AuthorityID = ecosystem.ID()
	other, err := ecosystem.NewAuthority().Issue(otherScope, []string{"task:reader", "task:mailbox", "local.read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	otherParent := f.parent
	otherParent.Scope = otherScope
	if got, err := f.m.List(t.Context(), other, otherParent); err != nil || len(got) != 0 {
		t.Fatal("别的会话看到了我的任务", got, err)
	}
	// 身份和授权对不上：拒绝
	if _, err := f.m.List(t.Context(), other, f.parent); err == nil {
		t.Fatal("拿别人的授权列我的任务")
	}
	close(f.runner.release)
	<-f.lifetime.ended
	list, _ = f.m.List(t.Context(), f.a, f.parent)
	if len(list) != 1 || list[0].Status != "completed" {
		t.Fatal(list)
	}
}
