package checkpoint

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"sumika-native/internal/kernel"
)

// Progress 的计数只包含已持久发布的文件；OutcomeSaved 表示这些结局已另行持久保存。
type Progress struct {
	TargetID, InverseID, State string
	Done, Total                int
	Completed, Remaining       []string
	OutcomeSaved               bool
	Error                      string
}

type restoreOperation struct {
	owner    kernel.Scope
	progress Progress
	cancel   context.CancelFunc
	done     chan struct{}
}

type restoreProgress struct {
	sync.Mutex
	active  map[kernel.Scope]*restoreOperation
	waiting map[*ProgressWatch]struct{}
	last    *restoreOperation
}

func Remaining(rec Record) []string {
	if len(rec.Done) == len(rec.After) {
		return nil
	}
	done := make(map[string]bool, len(rec.Done))
	for _, name := range rec.Done {
		done[name] = true
	}
	var out []string
	for name := range rec.After {
		if !done[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func recordProgress(rec Record) Progress {
	return Progress{TargetID: rec.InverseOf, InverseID: rec.ID, State: rec.Status, Done: len(rec.Done), Total: len(rec.After), Completed: slices.Clone(rec.Done), Remaining: Remaining(rec), OutcomeSaved: rec.Status == "ready" || rec.Status == "partial"}
}

func cloneProgress(p Progress) Progress {
	p.Completed, p.Remaining = slices.Clone(p.Completed), slices.Clone(p.Remaining)
	return p
}

// 查询不占文件变更门，才能在 Restore 持门时继续响应。冷启动从逆记录还原结局。
func (s *Service) Progress(ctx context.Context, owner kernel.Scope, id string) (Progress, error) {
	if err := s.authorize(ctx, owner, "read"); err != nil {
		return Progress{}, err
	}
	if !hexID(id, 32) {
		return Progress{}, ErrUnavailable
	}
	s.progress.Lock()
	op := s.progress.active[owner]
	if op == nil && s.progress.last != nil && s.progress.last.owner == owner {
		op = s.progress.last
	}
	if op != nil && (op.progress.TargetID == id || op.progress.InverseID == id) {
		p := cloneProgress(op.progress)
		s.progress.Unlock()
		return p, nil
	}
	s.progress.Unlock()
	records, err := s.List(ctx, owner)
	if err != nil {
		return Progress{}, err
	}
	for _, rec := range records {
		if rec.InverseOf != "" && (rec.InverseOf == id || rec.ID == id) {
			return recordProgress(rec), nil
		}
		if rec.ID == id {
			return Progress{TargetID: id, State: "idle"}, nil
		}
	}
	return Progress{}, ErrUnavailable
}

// 取消返回前等待恢复函数的全部 defer（包、目录和 worker 句柄）真实结束。
func (s *Service) Cancel(ctx context.Context, owner kernel.Scope, id string) (Progress, error) {
	if err := s.authorize(ctx, owner, "write"); err != nil {
		return Progress{}, err
	}
	if !hexID(id, 32) {
		return Progress{}, ErrUnavailable
	}
	s.progress.Lock()
	op := s.progress.active[owner]
	if op == nil || (op.progress.TargetID != id && op.progress.InverseID != id) {
		s.progress.Unlock()
		return Progress{}, ErrUnavailable
	}
	op.cancel()
	s.progress.Unlock()
	select {
	case <-op.done:
		s.progress.Lock()
		p := cloneProgress(op.progress)
		s.progress.Unlock()
		return p, nil
	case <-ctx.Done():
		return Progress{}, ctx.Err()
	}
}

func (s *Service) updateProgress(op *restoreOperation, update func(*Progress)) {
	s.progress.Lock()
	update(&op.progress)
	s.progress.Unlock()
}

// ProgressWatch 在创建时绑定活动操作，或等待下一次匹配操作；历史结局只供查询。
type ProgressWatch struct {
	service     *Service
	ctx         context.Context
	owner       kernel.Scope
	id          string
	op          *restoreOperation
	stopWaiting func() bool
}

func (s *Service) WatchProgress(ctx context.Context, owner kernel.Scope, id string) (*ProgressWatch, error) {
	if _, err := s.Progress(ctx, owner, id); err != nil {
		return nil, err
	}
	s.progress.Lock()
	defer s.progress.Unlock()
	w := &ProgressWatch{service: s, ctx: ctx, owner: owner, id: id}
	if op := s.progress.active[owner]; w.matches(op) {
		w.op = op
	} else {
		if s.progress.waiting == nil {
			s.progress.waiting = make(map[*ProgressWatch]struct{})
		}
		s.progress.waiting[w] = struct{}{}
		// Run 尚未启动也要随 context 释放，不能把关闭的订阅一直留在服务中。
		w.stopWaiting = context.AfterFunc(ctx, w.removeWaiting)
	}
	return w, nil
}

func (w *ProgressWatch) matches(op *restoreOperation) bool {
	return op != nil && op.owner == w.owner && (op.progress.TargetID == w.id || op.progress.InverseID == w.id)
}

func (w *ProgressWatch) removeWaiting() {
	w.service.progress.Lock()
	delete(w.service.progress.waiting, w)
	w.service.progress.Unlock()
}

// Run 定期发布累计状态；绑定后持有该操作，后来的恢复不能替换它的终态。
func (w *ProgressWatch) Run(emit func(Progress) error) error {
	defer func() {
		if w.stopWaiting != nil {
			w.stopWaiting()
			w.removeWaiting()
		}
	}()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := w.service.authorize(w.ctx, w.owner, "read"); err != nil {
			return err
		}
		s := w.service
		s.progress.Lock()
		p, finished := Progress{TargetID: w.id, State: "idle"}, false
		if w.op != nil {
			p = cloneProgress(w.op.progress)
			select {
			case <-w.op.done:
				finished = true
			default:
			}
		}
		s.progress.Unlock()
		if err := emit(p); err != nil {
			return err
		}
		if finished {
			return nil
		}
		select {
		case <-ticker.C:
		case <-w.ctx.Done():
			return w.ctx.Err()
		}
	}
}
