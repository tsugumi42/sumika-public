// Package turnbudget 实现持久化根预算；预留、派发和结算各自是一次 trace 事务。
package turnbudget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"sumika-native/internal/kernel"
)

var ErrExhausted = errors.New("budget_exhausted")
var ErrConflict = errors.New("budget reservation conflict")

// Storage 由 store.Store 实现；独立测试与宿主使用相同事务边界。
type Storage interface {
	UpdateBudget(context.Context, string, string, func([]byte) ([]byte, error)) error
	ReadBudget(context.Context, string) ([]byte, error)
}
type Ledger struct {
	Store  Storage
	Now    func() time.Time
	mu     sync.Mutex
	active map[string]map[*context.CancelFunc]struct{}
}
type entry struct {
	Reservation kernel.Reservation
	Dispatched  bool
	Charge      *kernel.Charge
}
type state struct {
	Snapshot kernel.BudgetSnapshot
	Owner    kernel.Scope
	Stopped  bool
	Children map[string]kernel.Invocation `json:",omitempty"`
	Entries  map[string]*entry
}

var _ kernel.Budget = (*Ledger)(nil)

func (l *Ledger) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func ValidateLimits(x kernel.Limits) error {
	if x.MaxRequests <= 0 || x.MaxTools <= 0 || x.MaxTokens <= 0 || x.MaxOutputTokens <= 0 || x.Deadline.IsZero() || x.MaxCostMicros != nil && *x.MaxCostMicros < 0 {
		return errors.New("budget limits must be positive with a deadline; cost must be nonnegative")
	}
	return nil
}

// OpenRoot 只由受理宿主调用；相同根绝不重置余额或延长截止时间。
func (l *Ledger) OpenRoot(ctx context.Context, root string, owner kernel.Scope, limits kernel.Limits) error {
	if root == "" {
		return errors.New("budget root is empty")
	}
	if err := ValidateLimits(limits); err != nil {
		return err
	}
	return l.Store.UpdateBudget(ctx, root, "", func(data []byte) ([]byte, error) {
		if len(data) != 0 {
			var s state
			if err := json.Unmarshal(data, &s); err != nil {
				return nil, err
			}
			stored, requested := s.Snapshot.Limits, limits
			sameDeadline := stored.Deadline.Equal(requested.Deadline)
			requested.Deadline = stored.Deadline
			if !sameDeadline || !reflect.DeepEqual(stored, requested) || s.Owner != owner {
				return nil, ErrConflict
			}
			return data, nil
		}
		zero := int64(0)
		return json.Marshal(state{Snapshot: kernel.BudgetSnapshot{RootID: root, Limits: limits, CostMicros: &zero}, Owner: owner, Entries: map[string]*entry{}})
	})
}

func (l *Ledger) change(ctx context.Context, root, id string, f func(*state) error) error {
	return l.Store.UpdateBudget(ctx, root, id, func(data []byte) ([]byte, error) {
		if len(data) == 0 {
			return nil, errors.New("budget root not admitted")
		}
		var s state
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, err
		}
		if err := f(&s); err != nil {
			return nil, err
		}
		return json.Marshal(s)
	})
}

// BindChild 只登记可信宿主已签发的委托；记账归属不授予工具或来源权限。
func (l *Ledger) BindChild(ctx context.Context, parent, child kernel.Invocation) error {
	domain := child.Scope
	domain.Conversation, domain.AuthorityID, domain.AuthorityRevision = parent.Scope.Conversation, parent.Scope.AuthorityID, parent.Scope.AuthorityRevision
	if parent.Kind != kernel.KindForeground || child.Kind != kernel.KindChild || parent.RootID == "" || parent.RunID == "" || child.RootID != parent.RootID || child.ParentRunID != parent.RunID || child.RunID == "" || child.RunID == parent.RunID || child.Generation != parent.Generation || child.Scope.AuthorityID == "" || child.Scope.AuthorityRevision == 0 || domain != parent.Scope {
		return ErrConflict
	}
	return l.change(ctx, parent.RootID, "", func(s *state) error {
		if s.Owner != parent.Scope {
			return ErrConflict
		}
		if blocked(s, l.now()) {
			return ErrExhausted
		}
		if old, ok := s.Children[child.RunID]; ok {
			if old != child {
				return ErrConflict
			}
			return nil
		}
		if s.Children == nil {
			s.Children = make(map[string]kernel.Invocation)
		}
		s.Children[child.RunID] = child
		return nil
	})
}

func (l *Ledger) Stop(ctx context.Context, root string) error {
	return l.change(ctx, root, "", func(s *state) error { s.Stopped = true; return nil })
}

// RestartDeadline 把一个还没花过任何额度的根的时限推迟到 deadline（只往后推，不缩短）。
// 排队等待的时间不该算进这句话自己的时限：真正开始运行时重算一次。已经有预留的根不动。
func (l *Ledger) RestartDeadline(ctx context.Context, root string, deadline time.Time) error {
	return l.change(ctx, root, "", func(s *state) error {
		if len(s.Entries) == 0 && !s.Stopped && deadline.After(s.Snapshot.Limits.Deadline) {
			s.Snapshot.Limits.Deadline = deadline
		}
		return nil
	})
}

func (l *Ledger) Snapshot(ctx context.Context, root string) (kernel.BudgetSnapshot, error) {
	data, err := l.Store.ReadBudget(ctx, root)
	if err != nil {
		return kernel.BudgetSnapshot{}, err
	}
	var s state
	err = json.Unmarshal(data, &s)
	// 已派发但未结算包含崩溃窗口；重开后也不能显示完整零用量。
	for _, entry := range s.Entries {
		if entry.Dispatched && entry.Reservation.Requests > 0 && entry.Charge == nil {
			s.Snapshot.Incomplete = true
		}
	}
	return s.Snapshot, err
}

func blocked(s *state, now time.Time) bool {
	b := s.Snapshot
	x := b.Limits
	return s.Stopped || !now.Before(x.Deadline) || b.Tokens+b.ReservedTokens > x.MaxTokens || b.OutputTokens+b.ReservedOutputTokens > x.MaxOutputTokens || x.MaxCostMicros != nil && (b.CostMicros == nil || *b.CostMicros > *x.MaxCostMicros)
}

// ValidInvocation 不从可重复 provider call id 或角色名推导预算身份。
func ValidInvocation(in kernel.Invocation) bool {
	if in.RootID == "" || in.RunID == "" {
		return false
	}
	switch in.Kind {
	case "foreground", "child", "advisor", "verify", "compact", "memory", "guard", "goal_judge", "link_translate":
		return true
	}
	return false
}

func (l *Ledger) Reserve(ctx context.Context, r kernel.Reservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.ID == "" || !ValidInvocation(r.Invocation) || r.Requests < 0 || r.Tools < 0 || r.Tokens < 0 || r.OutputTokens < 0 || r.OutputTokens > r.Tokens || r.Requests+r.Tools == 0 || r.CostMicros != nil && *r.CostMicros < 0 {
		return errors.New("invalid budget reservation")
	}
	return l.change(ctx, r.Invocation.RootID, r.ID, func(s *state) error {
		if old := s.Entries[r.ID]; old != nil {
			if !reflect.DeepEqual(old.Reservation, r) {
				return ErrConflict
			}
			return nil
		}
		b := &s.Snapshot
		x := b.Limits
		// 独立子授权只有经过宿主绑定才可扣原根；不得拿另一会话或代次借余额。
		if r.Invocation.Scope != s.Owner {
			bound, ok := s.Children[r.Invocation.RunID]
			if !ok || r.Invocation.Kind != kernel.KindChild || bound.Scope != r.Invocation.Scope || bound.ParentRunID != r.Invocation.ParentRunID || bound.Generation != r.Invocation.Generation {
				return ErrConflict
			}
		}
		if blocked(s, l.now()) || r.Requests > x.MaxRequests-b.Requests || r.Tools > x.MaxTools-b.Tools || r.Tokens > x.MaxTokens-b.Tokens-b.ReservedTokens || r.OutputTokens > x.MaxOutputTokens-b.OutputTokens-b.ReservedOutputTokens {
			return ErrExhausted
		}
		if x.MaxCostMicros != nil && r.Requests > 0 {
			if r.CostMicros == nil {
				return fmt.Errorf("%w: provider has no hard USD cost bound", ErrExhausted)
			}
			reserved := int64(0)
			for _, e := range s.Entries {
				if e.Charge == nil && e.Reservation.CostMicros != nil {
					reserved += *e.Reservation.CostMicros
				}
			}
			if b.CostMicros == nil || *x.MaxCostMicros == 0 || *r.CostMicros > *x.MaxCostMicros-*b.CostMicros-reserved {
				return ErrExhausted
			}
		}
		b.Requests += r.Requests
		b.Tools += r.Tools
		b.ReservedTokens += r.Tokens
		b.ReservedOutputTokens += r.OutputTokens
		s.Entries[r.ID] = &entry{Reservation: r}
		return nil
	})
}

func (l *Ledger) Dispatch(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.change(ctx, "", id, func(s *state) error {
		e := s.Entries[id]
		if e == nil {
			return errors.New("reservation not found")
		}
		if e.Dispatched {
			return ErrConflict
		}
		if e.Charge != nil {
			return ErrConflict
		}
		if blocked(s, l.now()) {
			return ErrExhausted
		}
		e.Dispatched = true
		return nil
	})
}

func (l *Ledger) Settle(ctx context.Context, id string, c kernel.Charge) error {
	if c.Input < 0 || c.Output < 0 || c.CacheRead < 0 || c.CacheWrite < 0 || c.Reasoning < 0 || c.CostMicros != nil && *c.CostMicros < 0 {
		return errors.New("invalid budget charge")
	}
	return l.change(ctx, "", id, func(s *state) error {
		e := s.Entries[id]
		if e == nil {
			return errors.New("reservation not found")
		}
		if e.Charge != nil {
			if reflect.DeepEqual(*e.Charge, c) {
				return nil
			}
			return ErrConflict
		}
		if e.Dispatched != c.Dispatched {
			return ErrConflict
		}
		b := &s.Snapshot
		r := e.Reservation
		switch {
		case !c.Dispatched:
			b.Requests -= r.Requests
			b.ReservedTokens -= r.Tokens
			b.ReservedOutputTokens -= r.OutputTokens
		case c.UsageSeen:
			b.ReservedTokens -= r.Tokens
			b.ReservedOutputTokens -= r.OutputTokens
			// Output 已含 reasoning，Input 已减去缓存；不能重复计费。
			b.Tokens += c.Input + c.CacheRead + c.CacheWrite + c.Output
			b.OutputTokens += c.Output
		default:
			b.Incomplete = true
		}
		if c.Dispatched && r.Requests > 0 {
			if c.CostMicros == nil {
				b.CostMicros = nil
				b.Incomplete = true
			} else if b.CostMicros != nil {
				*b.CostMicros += *c.CostMicros
			}
		}
		e.Charge = &c
		return nil
	})
}

// Track 登记同根在途执行；只保留当前活动，不把旧 root 的闭包永久留在内存。
func (l *Ledger) Track(ctx context.Context, root string) (context.Context, func()) {
	run, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	if l.active == nil {
		l.active = make(map[string]map[*context.CancelFunc]struct{})
	}
	if l.active[root] == nil {
		l.active[root] = make(map[*context.CancelFunc]struct{})
	}
	l.active[root][&cancel] = struct{}{}
	l.mu.Unlock()
	return run, func() {
		cancel()
		l.mu.Lock()
		delete(l.active[root], &cancel)
		if len(l.active[root]) == 0 {
			delete(l.active, root)
		}
		l.mu.Unlock()
	}
}

func (l *Ledger) Exhaust(ctx context.Context, root string) error {
	err := l.Stop(context.WithoutCancel(ctx), root)
	l.mu.Lock()
	for cancel := range l.active[root] {
		(*cancel)()
	}
	l.mu.Unlock()
	return err
}
