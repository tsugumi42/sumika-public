package runstate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"sumika-native/internal/kernel"
)

var ErrSealed = errors.New("运行准入已关闭")

// Journal 在返回准入前持久化意图；执行退出记录失败时 Drain 如实报错。
type Journal interface {
	ActivityStart(context.Context, string, kernel.Invocation) error
	ActivityEnd(context.Context, string, error) error
}

type Manager struct {
	mu      sync.Mutex
	sealed  bool
	next    uint64
	active  map[string]*activity
	changed chan struct{}
	journal Journal
	// failed 是退出时没记进库的活动（库里还写着它在运行），按活动号放：每条只属于它自己的运行（run、parent），
	// 等 WaitRun 的只拿到自己运行的那几条，别的会话不受牵连；写成功（RetryEnds）才删。
	failed map[string]*endFailure
}

// endFailure 是一次没写成的活动退出。cause 是退出原因，重试时原样再写一遍。
type endFailure struct {
	id, run, parent string
	cause, err      error
}

func (f *endFailure) of(run string) bool {
	return run == "" || f.run == run || f.parent == run
}

type activity struct {
	owner      *Manager
	id         string
	invocation kernel.Invocation
	cancel     context.CancelFunc
	ended      bool
}

func New(j Journal) *Manager {
	return &Manager{active: make(map[string]*activity), changed: make(chan struct{}), journal: j, failed: map[string]*endFailure{}}
}

func (m *Manager) Begin(ctx context.Context, in kernel.Invocation) (context.Context, kernel.Activity, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	switch in.Kind {
	case kernel.KindForeground, kernel.KindChild, kernel.KindAdvisor, kernel.KindVerify, kernel.KindCompact, kernel.KindMemory, kernel.KindGuard:
	default:
		return nil, nil, fmt.Errorf("未知运行用途 %q", in.Kind)
	}
	m.mu.Lock()
	if m.sealed {
		m.mu.Unlock()
		return nil, nil, ErrSealed
	}
	m.next++
	id := fmt.Sprintf("%s/activity/%d", in.RunID, m.next)
	child, cancel := context.WithCancel(ctx)
	a := &activity{owner: m, id: id, invocation: in, cancel: cancel}
	m.active[id] = a
	m.signal()
	m.mu.Unlock()
	// 登记先占准入，再写意图；写库不能拿着封入口的锁，取消必须能打断在途持久化。
	if m.journal != nil {
		if err := m.journal.ActivityStart(child, id, in); err != nil {
			cancel()
			m.mu.Lock()
			delete(m.active, id)
			m.signal()
			m.mu.Unlock()
			return nil, nil, err
		}
	}
	return child, a, nil
}

func (m *Manager) signal() { close(m.changed); m.changed = make(chan struct{}) }
func (m *Manager) Seal()   { m.mu.Lock(); m.sealed = true; m.mu.Unlock() }
func (m *Manager) CancelRun(run string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.active {
		if a.invocation.RunID == run || a.invocation.ParentRunID == run {
			a.cancel()
		}
	}
}
func (m *Manager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.active {
		a.cancel()
	}
}

func (a *activity) End(cause error) {
	m := a.owner
	m.mu.Lock()
	if a.ended {
		m.mu.Unlock()
		panic("Activity.End 调用了两次")
	}
	a.ended = true
	m.mu.Unlock()
	a.cancel()
	var err error
	if m.journal != nil {
		err = m.journal.ActivityEnd(context.Background(), a.id, cause)
	}
	m.mu.Lock()
	if err != nil {
		m.failed[a.id] = &endFailure{id: a.id, run: a.invocation.RunID, parent: a.invocation.ParentRunID, cause: cause, err: err}
	}
	delete(m.active, a.id)
	m.signal()
	m.mu.Unlock()
}

// WaitRun 只等本次激活；旧运行的执行未退，后继输入不能占用同一会话。
// 返回这一次运行自己（含它的子运行）退出时没记进库的错误；别的运行的不算在里面。
func (m *Manager) WaitRun(ctx context.Context, run string) error { return m.wait(ctx, run) }

// Drain 等所有执行退出；返回所有还没写成的退出记录（退出时据此保留库和数据锁）。
func (m *Manager) Drain(ctx context.Context) error { return m.wait(ctx, "") }

// failuresLocked 取属于这个运行的退出失败（run 为空 = 全部），按活动号排，输出稳定。
func (m *Manager) failuresLocked(run string) []*endFailure {
	var out []*endFailure
	for _, f := range m.failed {
		if f.of(run) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func joinFailures(fs []*endFailure) error {
	var errs []error
	for _, f := range fs {
		errs = append(errs, f.err)
	}
	return errors.Join(errs...)
}

// Active 是现在还没退出的活动个数（任何用途的）。
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

// EndFailures 是还没写成的活动退出记录的个数（run 为空 = 全部）。
func (m *Manager) EndFailures(run string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.failuresLocked(run))
}

// EndFailuresExcept 是没写成的活动退出记录里，不属于 owned 里任何一个运行（run、parent）的条数。
func (m *Manager) EndFailuresExcept(owned map[string]bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, f := range m.failed {
		if !owned[f.run] && !owned[f.parent] {
			n++
		}
	}
	return n
}

// RetryEnds 把这个运行没写成的活动退出再写一遍；写成的从失败表里删掉，没写成的留着并更新原因。
// 返回仍然没写成的那些错误（全成功是 nil）。
func (m *Manager) RetryEnds(ctx context.Context, run string) error {
	m.mu.Lock()
	todo := m.failuresLocked(run)
	m.mu.Unlock()
	if m.journal == nil {
		return nil
	}
	var errs []error
	for _, f := range todo {
		err := m.journal.ActivityEnd(ctx, f.id, f.cause)
		m.mu.Lock()
		if err == nil {
			delete(m.failed, f.id)
		} else if cur := m.failed[f.id]; cur != nil {
			cur.err = err
		}
		m.mu.Unlock()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func (m *Manager) wait(ctx context.Context, run string) error {
	for {
		m.mu.Lock()
		waiting := false
		for _, a := range m.active {
			if run == "" || a.invocation.RunID == run || a.invocation.ParentRunID == run {
				waiting = true
				break
			}
		}
		changed := m.changed
		failed := joinFailures(m.failuresLocked(run))
		m.mu.Unlock()
		if !waiting {
			return failed
		}
		select {
		case <-changed:
		case <-ctx.Done():
			// 只有失败诊断才收集 ID；正常等待不为快照分配切片。
			m.mu.Lock()
			ids := make([]string, 0, len(m.active))
			for id, a := range m.active {
				if run == "" || a.invocation.RunID == run || a.invocation.ParentRunID == run {
					ids = append(ids, id)
				}
			}
			failed = joinFailures(m.failuresLocked(run))
			m.mu.Unlock()
			if len(ids) == 0 {
				return failed
			}
			sort.Strings(ids)
			return fmt.Errorf("仍在 draining %v: %w", ids, errors.Join(ctx.Err(), failed))
		}
	}
}

var _ kernel.Lifetime = (*Manager)(nil)
