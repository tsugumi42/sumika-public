package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

type Repository interface {
	CreateTask(context.Context, store.TaskRecord) (store.TaskRecord, bool, error)
	Task(context.Context, string) (store.TaskRecord, error)
	OwnedTasks(context.Context, kernel.Scope, int) ([]store.TaskRecord, error)
	FinishTask(context.Context, store.TaskRecord, bool) error
	PendingTasks(context.Context, kernel.Scope) ([]store.TaskRecord, error)
	CommitTasks(context.Context, kernel.Scope, string, []string) error
	RecoverTasks(context.Context) error
}
type Runner interface {
	StartInternal(context.Context, turn.InternalRun) (turn.RunHandle, error)
	// SteerInternal 给还在跑的子运行追加一句指示；已结束返回 turn.ErrRunFinished，没有返回 turn.ErrRunNotFound。
	SteerInternal(scope kernel.Scope, runID, text string) error
}
type Options struct {
	Enabled                                                            bool
	Definitions                                                        []Definition
	Repository                                                         Repository
	Runner                                                             Runner
	Lifetime                                                           kernel.Lifetime
	Budget                                                             kernel.Budget
	Results                                                            kernel.ResultStore
	ParentValid                                                        func(context.Context, kernel.Invocation) bool
	ParentContext                                                      func(context.Context, kernel.Invocation) (context.Context, error)
	ParentWorkspace                                                    func(context.Context, kernel.Invocation) (turn.Workspace, error)
	Notify                                                             func(kernel.Scope)
	MaxDepth, MaxActivePerRoot, MaxActiveGlobal, MaxActivePerRequester int
	CreateTimeout, ResultTimeout                                       time.Duration
}
type Request struct{ Agent, Description, Prompt string }
type Result struct {
	TaskID, Status, Text string
	Ref                  kernel.ResultRef
	Error                string
}
type active struct {
	cancel    context.CancelFunc
	done      chan struct{}
	requester string
	owner     ecosystem.Access
	parent    kernel.Invocation
	scope     kernel.Scope // 子运行自己的会话身份，追加指示按它找运行
}
type Manager struct {
	opts            Options
	mu              sync.Mutex
	tasks           map[string]*active
	root, requester map[string]int
	closed          bool
}

func New(o Options) (*Manager, error) {
	for _, limit := range []struct {
		value             *int
		fallback, maximum int
	}{{&o.MaxDepth, 1, 1}, {&o.MaxActivePerRoot, 4, 4}, {&o.MaxActiveGlobal, 8, 8}, {&o.MaxActivePerRequester, 2, 2}} {
		if *limit.value == 0 {
			*limit.value = limit.fallback
		}
		if *limit.value < 1 || *limit.value > limit.maximum {
			return nil, fmt.Errorf("子任务容量越界")
		}
	}
	for _, timeout := range []*time.Duration{&o.CreateTimeout, &o.ResultTimeout} {
		if *timeout == 0 {
			*timeout = 5 * time.Second
		}
		if *timeout < 0 || *timeout > 5*time.Second {
			return nil, fmt.Errorf("子任务工具期限越界")
		}
	}
	if o.Enabled && (o.Repository == nil || o.Runner == nil || o.Lifetime == nil || o.Budget == nil || o.Results == nil || o.ParentValid == nil || o.ParentContext == nil) {
		return nil, fmt.Errorf("子任务缺少内核、预算、寿命或持久化端口")
	}
	seen := map[string]bool{}
	for _, d := range o.Definitions {
		if seen[d.ID] || d.ID == "" || d.ModelID == "" || len(d.Instructions) > 32768 || d.Timeout <= 0 {
			return nil, fmt.Errorf("代理定义非法")
		}
		seen[d.ID] = true
	}
	return &Manager{opts: o, tasks: map[string]*active{}, root: map[string]int{}, requester: map[string]int{}}, nil
}
func (m *Manager) Start(ctx context.Context, a ecosystem.Access, parent kernel.Invocation, q Request) (Result, error) {
	ctx, stopCreate := context.WithTimeout(ctx, m.opts.CreateTimeout)
	defer stopCreate()
	if !m.opts.Enabled || parent.Scope != a.Scope() || parent.RunID == "" || parent.RootID == "" || parent.ToolID == "" || parent.Kind != "foreground" {
		return Result{}, ecosystem.ErrDenied
	}
	if err := a.Check(ctx, "task:"+q.Agent); err != nil {
		return Result{}, err
	}
	if !m.opts.ParentValid(ctx, parent) {
		return Result{}, ecosystem.ErrDenied
	}
	if utf8.RuneCountInString(q.Description) > 120 || q.Description == "" || q.Prompt == "" || len(q.Prompt) > 16384 || estimatedTokens(q.Prompt) > 4000 {
		return Result{}, fmt.Errorf("任务输入越界")
	}
	var def Definition
	found := false
	for _, d := range m.opts.Definitions {
		if d.ID == q.Agent {
			def = d
			found = true
			break
		}
	}
	if !found {
		return Result{}, fmt.Errorf("invalid_agent")
	}
	if m.opts.ParentWorkspace != nil {
		workspace, err := m.opts.ParentWorkspace(ctx, parent)
		if err != nil {
			return Result{}, err
		}
		def.Workspace = workspace
	}
	for _, cap := range append(slices.Clone(def.Capabilities), publicDelegation(q.Agent)...) {
		if err := a.Check(ctx, cap); err != nil {
			if errors.Is(err, ecosystem.ErrDenied) {
				// 父没有这项能力（比如网页没开就派 research）：说清缺什么，模型才会换成别的子代理或自己做
				return Result{}, fmt.Errorf("%s 子代理需要 %s 能力，当前没有开启：%w", q.Agent, cap, err)
			}
			return Result{}, err
		}
	}
	snap, err := m.opts.Budget.Snapshot(ctx, parent.RootID)
	if err != nil {
		return Result{}, err
	}
	// 子代理自己没有请求、工具、令牌、累计输出的额度，只带父根现在的余额（父根不设上限时就是不设上限）；
	// 它的请求次数由定义的软预算管（过线收尾提示，1.5 倍强停），见 turn.InternalRun.SoftRequests
	limits := kernel.Limits{
		MaxRequests:     snap.Limits.MaxRequests - snap.Requests,
		MaxTools:        snap.Limits.MaxTools - snap.Tools,
		MaxTokens:       snap.Limits.MaxTokens - snap.Tokens - snap.ReservedTokens,
		MaxOutputTokens: snap.Limits.MaxOutputTokens - snap.OutputTokens - snap.ReservedOutputTokens,
	}
	if limits.MaxRequests <= 0 || limits.MaxTools <= 0 || limits.MaxTokens <= 0 || limits.MaxOutputTokens <= 0 {
		return Result{}, fmt.Errorf("quota_exceeded")
	}
	deadline := time.Now().Add(def.Timeout)
	if !snap.Limits.Deadline.IsZero() && snap.Limits.Deadline.Before(deadline) {
		deadline = snap.Limits.Deadline
	}
	limits.Deadline = deadline
	if snap.Limits.MaxCostMicros != nil {
		remaining := *snap.Limits.MaxCostMicros
		if snap.CostMicros != nil {
			remaining -= *snap.CostMicros
		}
		if remaining < 0 {
			return Result{}, fmt.Errorf("quota_exceeded")
		}
		limits.MaxCostMicros = &remaining
	}
	id := ecosystem.ID()
	inv := parent
	inv.Kind = "child"
	inv.ParentRunID = parent.RunID
	inv.RunID = id
	inv.StepID = ""
	inv.ToolID = ""
	inv.ProviderCallID = ""
	inv.AttemptID = ""
	// 子会话和 return owner 分开保存，不伪造 console 会话来取得权限。
	parentCtx, err := m.opts.ParentContext(ctx, parent)
	if err != nil {
		return Result{}, err
	}
	childAccess, err := a.Delegate(ctx, "internal:"+id, def.Capabilities, def.Sources)
	if err != nil {
		return Result{}, err
	}
	inv.Scope = childAccess.Scope()
	started := false
	defer func() {
		if !started {
			childAccess.Revoke()
		}
	}()
	// 开始时间只进存档载荷，不进 Digest：重试同一次工具调用仍判为同一个任务。
	payload, _ := json.Marshal(struct {
		Request
		StartedAt int64
	}{q, time.Now().UnixMilli()})
	record := store.TaskRecord{ID: id, ParentRun: parent.RunID, ToolID: parent.ToolID, Digest: ecosystem.Digest([]any{q, parent.Scope, parent.RootID}), Owner: parent.Scope, RootID: parent.RootID, State: "running", Payload: string(payload)}
	requester := ecosystem.Digest([]string{parent.Scope.Platform, parent.Scope.AccountID, parent.Scope.ActorKind, parent.Scope.ActorID})
	m.mu.Lock()
	if m.closed || len(m.tasks) >= m.opts.MaxActiveGlobal || m.root[parent.RootID] >= m.opts.MaxActivePerRoot || m.requester[requester] >= m.opts.MaxActivePerRequester {
		m.mu.Unlock()
		return Result{}, fmt.Errorf("busy")
	}
	record, created, err := m.opts.Repository.CreateTask(ctx, record)
	if err != nil {
		m.mu.Unlock()
		return Result{}, err
	}
	if !created {
		m.mu.Unlock()
		return Result{TaskID: record.ID, Status: record.State}, nil
	}
	childCtx, cancel := context.WithDeadline(parentCtx, deadline)
	var activity kernel.Activity
	err = m.opts.Budget.BindChild(ctx, parent, inv)
	if err == nil {
		childCtx, activity, err = m.opts.Lifetime.Begin(childCtx, inv)
	}
	if err != nil {
		cancel()
		m.mu.Unlock()
		record.State = "failed"
		record.Result = err.Error()
		persist, stop := context.WithTimeout(context.WithoutCancel(ctx), m.opts.ResultTimeout)
		saveErr := m.opts.Repository.FinishTask(persist, record, true)
		stop()
		return Result{}, errors.Join(err, saveErr)
	}
	running := &active{cancel: cancel, done: make(chan struct{}), owner: a, parent: parent, requester: requester, scope: inv.Scope}
	m.tasks[id] = running
	m.root[parent.RootID]++
	m.requester[requester]++
	m.mu.Unlock()
	started = true
	go m.run(childCtx, activity, running, record, inv, def, limits, q, childAccess)
	return Result{TaskID: id, Status: "running"}, nil
}

// Owner 是还在跑的子任务是谁派的。宿主用它把子任务的审批卡放回派它的那个会话，主人才看得到、批得了。
func (m *Manager) Owner(taskID string) (kernel.Scope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.tasks[taskID]; t != nil {
		return t.parent.Scope, true
	}
	return kernel.Scope{}, false
}

func estimatedTokens(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x2e80 {
			n += 4
		} else {
			n++
		}
	}
	return (n + 3) / 4
}
func (m *Manager) run(ctx context.Context, activity kernel.Activity, live *active, record store.TaskRecord, inv kernel.Invocation, def Definition, limits kernel.Limits, q Request, childAccess ecosystem.Access) {
	defer childAccess.Revoke()
	var finishErr error
	defer func() {
		live.cancel()
		m.mu.Lock()
		delete(m.tasks, record.ID)
		m.root[record.RootID]--
		m.requester[live.requester]--
		if m.root[record.RootID] == 0 {
			delete(m.root, record.RootID)
		}
		if m.requester[live.requester] == 0 {
			delete(m.requester, live.requester)
		}
		close(live.done)
		m.mu.Unlock()
		activity.End(finishErr)
	}()
	handle, err := m.opts.Runner.StartInternal(ctx, turn.InternalRun{Identity: inv, Workspace: def.Workspace, Goal: def.Instructions + "\n\n" + q.Prompt, Limits: limits, SoftRequests: def.SoftRequests, ModelID: def.ModelID})
	var out turn.Outcome
	if err == nil {
		out = <-handle.Done
		err = out.Err
	}
	result := Result{TaskID: record.ID, Status: "completed"}
	if out.Result != nil {
		result.Text = out.Result.Reply
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.Status = "timed_out"
	case ctx.Err() != nil:
		result.Status = "cancelled"
	case err != nil:
		result.Status = "failed"
	case out.Result != nil && out.Result.Stopped:
		result.Status = "cancelled"
	case strings.TrimSpace(result.Text) == "":
		result.Status = "failed"
	}
	if err != nil {
		result.Error = err.Error()
	}
	complete := len(result.Text) <= 512<<10
	if !complete {
		end := 512 << 10
		for !utf8.RuneStart(result.Text[end]) {
			end--
		}
		result.Text = result.Text[:end]
		result.Status = "partial"
	}
	persist, stop := context.WithTimeout(context.WithoutCancel(ctx), m.opts.ResultTimeout)
	defer stop()
	if result.Text != "" {
		resultInvocation := live.parent
		resultInvocation.ToolID = "task-result:" + record.ID
		resultInvocation.ProviderCallID = ""
		result.Ref, finishErr = m.opts.Results.Put(persist, resultInvocation, "text/plain; charset=utf-8", strings.NewReader(result.Text), complete)
		if finishErr != nil {
			result.Status = "partial"
			result.Error = finishErr.Error()
		}
	}
	if len(result.Text) > 16384 {
		end := 16384
		for !utf8.RuneStart(result.Text[end]) {
			end--
		}
		result.Text = result.Text[:end]
	}
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		finishErr = marshalErr
		return
	}
	record.Result = string(raw)
	record.State = result.Status
	held := ctx.Err() != nil || live.owner.Check(persist, "task:"+q.Agent) != nil || !m.opts.ParentValid(persist, live.parent)
	if err := m.opts.Repository.FinishTask(persist, record, held); err != nil {
		finishErr = errors.Join(finishErr, err)
		return
	}
	if !held && m.opts.Notify != nil {
		m.opts.Notify(record.Owner)
	}
}

// owned 查任务并核对归属：同一会话授权、同一父运行和根、仍持有该代理的 task 能力。
// 任务不存在返回 errNoTask，别人的任务返回 ErrDenied；task_result、task_cancel 把两者都当"不可访问"，task_message 要把"没有"说清楚。
func (m *Manager) owned(ctx context.Context, a ecosystem.Access, parent kernel.Invocation, id string) (store.TaskRecord, error) {
	t, err := m.opts.Repository.Task(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t, errNoTask
		}
		return t, err
	}
	if t.Owner != a.Scope() || t.ParentRun != parent.RunID || t.RootID != parent.RootID {
		return t, ecosystem.ErrDenied
	}
	var q Request
	if err = json.Unmarshal([]byte(t.Payload), &q); err != nil {
		return t, err
	}
	if err = a.Check(ctx, "task:"+q.Agent); err != nil {
		return t, ecosystem.ErrDenied
	}
	return t, nil
}

var errNoTask = errors.New("没有这个任务")

func (m *Manager) Read(ctx context.Context, a ecosystem.Access, parent kernel.Invocation, id string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, m.opts.ResultTimeout)
	defer cancel()
	t, err := m.owned(ctx, a, parent, id)
	if errors.Is(err, errNoTask) {
		return Result{}, ecosystem.ErrDenied
	}
	if err != nil {
		return Result{}, err
	}
	if t.Result == "" {
		return Result{TaskID: t.ID, Status: t.State}, nil
	}
	var out Result
	err = json.Unmarshal([]byte(t.Result), &out)
	return out, err
}
func (m *Manager) Cancel(ctx context.Context, a ecosystem.Access, parent kernel.Invocation, id string) error {
	if _, err := m.Read(ctx, a, parent, id); err != nil {
		return err
	}
	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.cancel()
	}
	m.mu.Unlock()
	return nil
}
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	done := make([]chan struct{}, 0, len(m.tasks))
	for _, t := range m.tasks {
		t.cancel()
		done = append(done, t.done)
	}
	m.mu.Unlock()
	for _, ch := range done {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// publicDelegation：research 要联网读公开资料，另需父有公开委派授权
func publicDelegation(agent string) []string {
	if agent == "research" {
		return []string{"public-delegation"}
	}
	return nil
}

// Message 给自己派出的、还在跑的子任务追加一句指示，在它下一次请求之前送到。
// 已结束的、没有的、别人的任务都不送；追加指示不改变子任务的授权和预算。
func (m *Manager) Message(ctx context.Context, a ecosystem.Access, parent kernel.Invocation, id, text string) (map[string]string, error) {
	if !m.opts.Enabled || parent.Scope != a.Scope() || parent.Kind != "foreground" {
		return nil, ecosystem.ErrDenied
	}
	if strings.TrimSpace(text) == "" || len(text) > 16384 || estimatedTokens(text) > 4000 {
		return nil, fmt.Errorf("追加指示不能为空，也不能超过 4000 个 token")
	}
	ctx, cancel := context.WithTimeout(ctx, m.opts.ResultTimeout)
	defer cancel()
	t, err := m.owned(ctx, a, parent, id)
	if err != nil {
		return nil, err
	}
	finished := fmt.Errorf("任务已结束（%s），不能再追加指示；需要继续可以用 task 再派一个", t.State)
	m.mu.Lock()
	live := m.tasks[id]
	m.mu.Unlock()
	if live == nil || t.State != "running" {
		return nil, finished
	}
	switch err = m.opts.Runner.SteerInternal(live.scope, id, text); {
	case errors.Is(err, turn.ErrRunFinished), errors.Is(err, turn.ErrRunNotFound):
		return nil, finished
	case err != nil:
		return nil, err
	}
	return map[string]string{"status": "queued", "note": "指示已排队，子任务下一步之前会看到；用 task_result 看它的进展"}, nil
}

// MaxWait 是 task_result 阻塞等待的上限。
const MaxWait = 300 * time.Second

// Wait 在任务还在跑时最多等 d（不超过 MaxWait）：任务结束、调用被取消或到点就返回 nil，调用方随后照常读状态。
// 先核对归属，别人的、没有的任务和 task_result 一样返回不可访问，不会借等待探出它们存不存在。
func (m *Manager) Wait(ctx context.Context, a ecosystem.Access, parent kernel.Invocation, id string, d time.Duration) error {
	if _, err := m.Read(ctx, a, parent, id); err != nil {
		return err
	}
	m.mu.Lock()
	live := m.tasks[id]
	m.mu.Unlock()
	if live == nil {
		return nil
	}
	timer := time.NewTimer(min(d, MaxWait))
	defer timer.Stop()
	select {
	case <-live.done:
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// TaskInfo 是 task_list 的一行。ThisRun 为真的才能用 task_result、task_message、task_cancel 操作（它们认派出它的那次回合）。
type TaskInfo struct {
	TaskID      string `json:"task_id"`
	Agent       string `json:"agent"`
	Status      string `json:"status"`
	StartedAt   string `json:"started_at,omitempty"`
	Description string `json:"description"`
	ThisRun     bool   `json:"this_run"`
}

// List 列出本会话授权下自己派出的任务（新的在前，最多 50 条）；只列自己的，别的会话和角色看不到。
func (m *Manager) List(ctx context.Context, a ecosystem.Access, parent kernel.Invocation) ([]TaskInfo, error) {
	if !m.opts.Enabled || parent.Scope != a.Scope() || parent.Kind != "foreground" {
		return nil, ecosystem.ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, m.opts.ResultTimeout)
	defer cancel()
	records, err := m.opts.Repository.OwnedTasks(ctx, a.Scope(), 50)
	if err != nil {
		return nil, err
	}
	out := make([]TaskInfo, 0, len(records))
	for _, t := range records {
		var p struct {
			Request
			StartedAt int64
		}
		if err = json.Unmarshal([]byte(t.Payload), &p); err != nil {
			return nil, err
		}
		if a.Check(ctx, "task:"+p.Agent) != nil {
			continue
		}
		info := TaskInfo{TaskID: t.ID, Agent: p.Agent, Status: t.State, Description: p.Description, ThisRun: t.ParentRun == parent.RunID && t.RootID == parent.RootID}
		if p.StartedAt > 0 {
			info.StartedAt = time.UnixMilli(p.StartedAt).Format(time.RFC3339)
		}
		out = append(out, info)
	}
	return out, nil
}
