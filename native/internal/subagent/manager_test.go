package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

type fixtureBudget struct{ snapshot kernel.BudgetSnapshot }

func (b fixtureBudget) BindChild(context.Context, kernel.Invocation, kernel.Invocation) error {
	return nil
}

func (b fixtureBudget) Reserve(context.Context, kernel.Reservation) error   { return nil }
func (b fixtureBudget) Dispatch(context.Context, string) error              { return nil }
func (b fixtureBudget) Settle(context.Context, string, kernel.Charge) error { return nil }
func (b fixtureBudget) Snapshot(context.Context, string) (kernel.BudgetSnapshot, error) {
	return b.snapshot, nil
}

type fixtureActivity struct{ end func(error) }

func (a fixtureActivity) End(err error) { a.end(err) }

type fixtureLifetime struct {
	mu     sync.Mutex
	active int
	ended  chan struct{}
}

func (l *fixtureLifetime) Begin(ctx context.Context, _ kernel.Invocation) (context.Context, kernel.Activity, error) {
	l.mu.Lock()
	l.active++
	l.mu.Unlock()
	return ctx, fixtureActivity{func(error) { l.mu.Lock(); l.active--; l.mu.Unlock(); l.ended <- struct{}{} }}, nil
}
func (l *fixtureLifetime) Seal()                       {}
func (l *fixtureLifetime) Drain(context.Context) error { return nil }

type fixtureResults struct {
	mu     sync.Mutex
	values map[string]string
	refs   map[string]kernel.ResultRef
}

func (f *fixtureResults) Put(ctx context.Context, inv kernel.Invocation, media string, r io.Reader, complete bool) (kernel.ResultRef, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return kernel.ResultRef{}, err
	}
	ref := kernel.ResultRef{ID: ecosystem.ID(), Owner: inv.Scope, RunID: inv.RunID, Bytes: int64(len(b)), Complete: complete, MediaType: media}
	f.mu.Lock()
	f.values[ref.ID] = string(b)
	f.refs[ref.ID] = ref
	f.mu.Unlock()
	return ref, nil
}
func (f *fixtureResults) Read(ctx context.Context, s kernel.Scope, id string, offset, limit int64) (kernel.ResultPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref, ok := f.refs[id]
	if !ok || ref.Owner != s {
		return kernel.ResultPage{}, ecosystem.ErrDenied
	}
	text := f.values[id]
	end := min(int64(len(text)), offset+limit)
	return kernel.ResultPage{Ref: ref, Text: text[offset:end], Start: offset, End: end, Next: end, EOF: end == int64(len(text))}, nil
}

type fixtureRunner struct {
	started chan turn.InternalRun
	release chan struct{}
	mu      sync.Mutex
	steered []string // 收到的追加指示，格式 "<会话>|<运行号>|<文字>"
}

func (r *fixtureRunner) SteerInternal(scope kernel.Scope, runID, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steered = append(r.steered, scope.Conversation+"|"+runID+"|"+text)
	return nil
}

func (r *fixtureRunner) StartInternal(ctx context.Context, in turn.InternalRun) (turn.RunHandle, error) {
	r.started <- in
	done := make(chan turn.Outcome, 1)
	go func() {
		<-r.release
		done <- turn.Outcome{TurnID: in.Identity.RunID, Result: &turn.Result{Reply: "编造的研究结论及出处"}}
	}()
	return turn.RunHandle{RunID: in.Identity.RunID, Done: done}, nil
}
func TestChildOwnerLimitsAndStop(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	repo := store.EcosystemStore{DB: set.TraceStore().Trace}
	s := kernel.Scope{CharacterID: "fixture", Conversation: "original-conversation", ScopeID: "owner", Platform: "local", Channel: "console", AccountID: "50001", Scene: "private", ActorKind: "owner", ActorID: "50002", AuthorityID: ecosystem.ID(), AuthorityRevision: 1}
	a, err := ecosystem.NewAuthority().Issue(s, []string{"task:reader", "task:mailbox", "local.read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	parentCtx, cancelParent := context.WithCancel(t.Context())
	defer cancelParent()
	parent := kernel.Invocation{Scope: s, Kind: "foreground", RootID: "fixture-root", RunID: "fixture-parent", ToolID: "fixture-tool", Generation: 1}
	runner := &fixtureRunner{started: make(chan turn.InternalRun, 8), release: make(chan struct{})}
	lifetime := &fixtureLifetime{ended: make(chan struct{}, 8)}
	results := &fixtureResults{values: map[string]string{}, refs: map[string]kernel.ResultRef{}}
	budget := fixtureBudget{kernel.BudgetSnapshot{Limits: kernel.Limits{MaxRequests: 24, MaxTools: 60, MaxTokens: 524288, MaxOutputTokens: 32768}}}
	notifications := 0
	m, err := New(Options{Enabled: true, Definitions: Definitions("fixture-model"), Repository: repo, Runner: runner, Lifetime: lifetime, Budget: budget, Results: results, ParentValid: func(context.Context, kernel.Invocation) bool { return parentCtx.Err() == nil }, ParentContext: func(context.Context, kernel.Invocation) (context.Context, error) { return parentCtx, nil }, Notify: func(kernel.Scope) { notifications++ }})
	if err != nil {
		t.Fatal(err)
	}
	q := Request{Agent: "reader", Description: "只读研究", Prompt: "查编造文件，不带父历史"}
	exhausted := budget
	exhausted.snapshot.Requests = exhausted.snapshot.Limits.MaxRequests
	m.opts.Budget = exhausted
	if _, err := m.Start(t.Context(), a, parent, q); err == nil {
		t.Fatal("父根请求额度耗尽仍创建孩子")
	}
	m.opts.Budget = budget
	toolCtx, cancelTool := context.WithCancel(t.Context())
	first, err := m.Start(toolCtx, a, parent, q)
	if err != nil {
		t.Fatal(err)
	}
	cancelTool()
	in := <-runner.started
	if in.Identity.Scope.Conversation == parent.Scope.Conversation || in.Identity.RootID != parent.RootID || in.Identity.Kind != "child" || strings.Contains(in.Goal, "父秘密") {
		t.Fatal("子身份或上下文混入父域")
	}
	duplicate, err := m.Start(t.Context(), a, parent, q)
	if err != nil || duplicate.TaskID != first.TaskID {
		t.Fatal("父 ToolID 去重失败", duplicate, err)
	}
	forged := parent
	forged.Kind = "child"
	forged.ToolID = "nested"
	if _, err := m.Start(t.Context(), a, forged, q); err == nil {
		t.Fatal("孩子再次派活")
	}
	secondParent := parent
	secondParent.ToolID = "second"
	if _, err := m.Start(t.Context(), a, secondParent, q); err != nil {
		t.Fatal(err)
	}
	<-runner.started
	otherScope := s
	otherScope.Conversation = "other-conversation"
	otherScope.AuthorityID = ecosystem.ID()
	otherGrant, err := ecosystem.NewAuthority().Issue(otherScope, []string{"task:reader", "local.read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	otherParent := parent
	otherParent.Scope = otherScope
	otherParent.RootID = "other-root"
	otherParent.RunID = "other-run"
	otherParent.ToolID = "other-tool"
	if _, err = m.Start(t.Context(), otherGrant, otherParent, q); err == nil {
		t.Fatal("同一派活人换会话绕过容量")
	}
	third := parent
	third.ToolID = "third"
	if _, err := m.Start(t.Context(), a, third, q); err == nil {
		t.Fatal("请求者容量超限未拒绝")
	}
	if err := m.Cancel(t.Context(), a, parent, first.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(t.Context(), a, third, q); err == nil {
		t.Fatal("cancel 后真实执行未退却返还容量")
	}
	cancelParent()
	close(runner.release)
	<-lifetime.ended
	<-lifetime.ended
	if notifications != 0 {
		t.Fatal("父停后唤醒")
	}
	result, err := m.Read(t.Context(), a, parent, first.TaskID)
	if err != nil || result.Status != "cancelled" {
		t.Fatal(result, err)
	}
	other := parent
	other.RunID = "unrelated"
	if _, err := m.Read(t.Context(), a, other, first.TaskID); err == nil {
		t.Fatal("其他父能读")
	}
	var state string
	if err := repo.DB.QueryRow(`SELECT state FROM kernel_task_mailbox WHERE task_id=?`, first.TaskID).Scan(&state); err != nil || state != "held" {
		t.Fatal("停止结果未 held", state, err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := set.TraceStore().Trace.QueryRow(`SELECT count(*) FROM kernel_tasks`).Scan(&rows); err != nil || rows != 2 {
		t.Fatal(rows, err)
	}
	raw, _ := json.Marshal(result)
	if err = set.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Read(t.Context(), a, parent, first.TaskID); err == nil || errors.Is(err, ecosystem.ErrDenied) {
		t.Fatal("数据库错误被隐藏成不存在", err)
	}
	t.Logf("tasks=%d held=%s result_bytes=%d notifications=%d", rows, state, len(raw), notifications)
}

// 父没有网页能力就派 research：旧代码只回"不可访问或不存在"，模型分不清是哪里不行；现在说清缺哪项能力，原错误仍可 errors.Is。
func TestStartNamesMissingCapability(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	s := kernel.Scope{CharacterID: "fixture", Conversation: "c", ScopeID: "owner", Platform: "local", Channel: "console", AccountID: "50001", Scene: "private", ActorKind: "owner", ActorID: "50002", AuthorityID: ecosystem.ID(), AuthorityRevision: 1}
	a, err := ecosystem.NewAuthority().Issue(s, []string{"task:research", "task:reader", "local.read"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	parent := kernel.Invocation{Scope: s, Kind: "foreground", RootID: "r", RunID: "run", ToolID: "tool", Generation: 1}
	budget := fixtureBudget{kernel.BudgetSnapshot{Limits: kernel.Limits{MaxRequests: 24, MaxTools: 60, MaxTokens: 524288, MaxOutputTokens: 32768}}}
	m, err := New(Options{Enabled: true, Definitions: Definitions("fixture-model"), Repository: store.EcosystemStore{DB: set.TraceStore().Trace}, Runner: &fixtureRunner{started: make(chan turn.InternalRun, 1), release: make(chan struct{})}, Lifetime: &fixtureLifetime{ended: make(chan struct{}, 1)}, Budget: budget, Results: &fixtureResults{values: map[string]string{}, refs: map[string]kernel.ResultRef{}}, ParentValid: func(context.Context, kernel.Invocation) bool { return true }, ParentContext: func(ctx context.Context, _ kernel.Invocation) (context.Context, error) { return ctx, nil }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Start(t.Context(), a, parent, Request{Agent: "research", Description: "查资料", Prompt: "查编造的公开资料"})
	if !errors.Is(err, ecosystem.ErrDenied) || !strings.Contains(err.Error(), "research") || !strings.Contains(err.Error(), "web:search") {
		t.Fatalf("应指出 research 缺 web:search 且仍是 ErrDenied：%v", err)
	}
}

// 工具说明要让模型知道三种子代理各能做什么、怎么取结果，不然只能靠猜（实测它拿 research 去数文件）。
func TestTaskDeclarationExplainsAgents(t *testing.T) {
	d := Declarations[0].Description
	for _, want := range []string{"reader", "worker", "research", "task_result", "task_cancel"} {
		if !strings.Contains(d, want) {
			t.Fatalf("task 说明里没有 %s：%s", want, d)
		}
	}
}
