// Package forwardfetch 在接收宿主里异步取合并转发；不占会话入库队列，单工人按外层到内层存树。
package forwardfetch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

type Media interface {
	OfferForward(types.ForwardTree, string) error
}

type Options struct {
	Store   *store.Store
	Reader  types.ForwardReader
	Account string
	Media   Media
	Logf    func(string, ...any)
}

type request struct {
	id, conv string
	via      []string
	done     chan struct{}
	state    string
	depth    int
}

type Fetcher struct {
	o                Options
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	mu               sync.Mutex
	pending          map[string]*request
	queue            chan *request
	sweep            chan struct{}
	outer            map[string]time.Time
	backfillNotify   func()
	backfillSweeping bool
}

func New(o Options) *Fetcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Fetcher{o: o, ctx: ctx, cancel: cancel, pending: map[string]*request{}, queue: make(chan *request, 500), sweep: make(chan struct{}, 1), outer: map[string]time.Time{}}
}

// Start 只起工人；首次连接和重连成功以后由宿主请求补扫，避免查询尚未连上的协议端。
func (f *Fetcher) Start() { f.wg.Add(1); go f.worker() }
func (f *Fetcher) Close() { f.cancel(); f.wg.Wait() }
func (f *Fetcher) Resweep() {
	f.backfillSweep(true)
	select {
	case f.sweep <- struct{}{}:
	default:
	}
}

// Offer 只在内存入队；陌生人和自己的号照取，不看观察记录。
func (f *Fetcher) Offer(conv string, segs []types.Seg) {
	for _, s := range segs {
		if s.Kind == types.SegForward && s.Ref != "" {
			f.enqueue(s.Ref, conv, nil)
		}
	}
}
func (f *Fetcher) enqueue(id, conv string, via []string) *request {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ctx.Err() != nil {
		return nil
	}
	if r := f.pending[id]; r != nil {
		return r
	}
	r := &request{id: id, conv: conv, via: append([]string(nil), via...), done: make(chan struct{}), state: "fetching"}
	select {
	case f.queue <- r:
		f.pending[id] = r
		f.notifyBackfillLocked()
		return r
	default:
		return nil
	}
}
func (f *Fetcher) State(id string) (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.pending[id]; r != nil {
		return r.state, r.depth
	}
	return "unavailable", 0
}
func (f *Fetcher) Fetch(ctx context.Context, id, conv string, via []string, wait time.Duration) (string, error) {
	r := f.enqueue(id, conv, via)
	if r == nil {
		return "unavailable", f.ctx.Err()
	}
	if wait <= 0 {
		return "fetching", nil
	}
	timer := time.NewTimer(min(wait, time.Minute))
	defer timer.Stop()
	select {
	case <-r.done:
		f.mu.Lock()
		state := r.state
		f.mu.Unlock()
		return state, nil
	case <-f.ctx.Done():
		return "connection_lost", f.ctx.Err()
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "fetching", nil
	}
}
func (f *Fetcher) worker() {
	defer f.wg.Done()
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-f.sweep:
			rows, err := f.o.Store.ForwardCandidates(f.ctx, time.Now().Add(-7*24*time.Hour).UnixMilli())
			if err != nil {
				if f.ctx.Err() == nil {
					f.o.Logf("转发补扫失败：%v", err)
				}
				f.backfillSweep(false)
				continue
			}
			for _, r := range rows {
				f.enqueue(r.ID, r.Conversation, nil)
			}
			f.backfillSweep(false)
		case r := <-f.queue:
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Minute)
			budget, images, seen, err := f.remaining(ctx, r)
			if err == nil {
				err = f.walk(ctx, r, r.id, len(r.via)+1, &budget, &images, seen)
			}
			cancel()
			f.mu.Lock()
			r.state = "ready"
			if err != nil {
				r.state = stateOf(err)
			}
			close(r.done)
			delete(f.pending, r.id)
			f.notifyBackfillLocked()
			f.mu.Unlock()
			if err != nil && f.ctx.Err() == nil {
				f.o.Logf("转发取回失败：%s", stateOf(err))
			}
		}
	}
}
func stateOf(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "connection_lost"
	}
	switch err.Error() {
	case "refused", "not_connected", "connection_lost", "timeout":
		return err.Error()
	}
	return "unavailable"
}

// 单取里层也共用授权根的额度；已存的兄弟层和已下载的图不能让重试重新获得预算。
func (f *Fetcher) remaining(ctx context.Context, r *request) (int, int, map[string]bool, error) {
	budget, images := types.ForwardNodes, 60
	seen := map[string]bool{}
	var reserve func(string, int) error
	reserve = func(id string, depth int) error {
		if id == r.id || seen[id] || depth > types.ForwardDepth {
			return nil
		}
		seen[id] = true
		record, have, err := f.o.Store.ReadForward(ctx, id)
		if err != nil || !have {
			return err
		}
		budget -= len(record.Tree.Nodes)
		for _, n := range record.Tree.Nodes {
			for _, s := range n.Segs {
				if usesForwardImageBudget(s) {
					images--
				}
				if s.Kind == types.SegForward && s.Ref != "" {
					if err := reserve(s.Ref, depth+1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	var err error
	if len(r.via) > 0 {
		err = reserve(r.via[0], 1)
	}
	return max(0, budget), max(0, images), seen, err
}

func usesForwardImageBudget(s types.Seg) bool {
	return (s.Kind == types.SegImage || s.Kind == types.SegEmoji || s.Kind == types.SegCard) && (s.URL != "" || s.Hash != "") && s.Raw != "forward_limit"
}
func (f *Fetcher) walk(ctx context.Context, r *request, id string, depth int, budget, images *int, seen map[string]bool) error {
	if depth > types.ForwardDepth || *budget == 0 || seen[id] {
		return nil
	}
	seen[id] = true
	f.mu.Lock()
	r.depth = depth
	f.mu.Unlock()
	record, have, err := f.o.Store.ReadForward(ctx, id)
	if err != nil {
		return err
	}
	tree := record.Tree
	if !have {
		tree, err = f.o.Reader.Forward(ctx, f.o.Account, id)
	}
	if err != nil && id == r.id && strings.Contains(err.Error(), "payload_empty") {
		via := r.via
		for i := len(via) - 1; i >= max(0, len(via)-4); i-- {
			outer := via[i]
			if time.Since(f.outer[outer]) < 10*time.Minute {
				continue
			}
			f.outer[outer] = time.Now()
			if _, outerErr := f.o.Reader.Forward(ctx, f.o.Account, outer); outerErr != nil {
				continue
			}
			tree, err = f.o.Reader.Forward(ctx, f.o.Account, id)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if len(tree.Nodes) == 0 {
		return errors.New("unavailable")
	}
	if len(tree.Nodes) > *budget {
		tree.Missing += len(tree.Nodes) - *budget
		tree.Nodes = tree.Nodes[:*budget]
	}
	*budget -= len(tree.Nodes)
	for ni := range tree.Nodes {
		for si := range tree.Nodes[ni].Segs {
			s := &tree.Nodes[ni].Segs[si]
			if usesForwardImageBudget(*s) {
				if *images == 0 {
					s.Raw = "forward_limit"
				} else {
					*images--
				}
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !have {
		if err = f.o.Store.PutForward(ctx, tree, time.Now().UnixMilli(), r.conv); err != nil {
			return err
		}
	}
	if f.o.Media != nil {
		if err = f.o.Media.OfferForward(tree, r.conv); err != nil {
			return err
		}
	}
	for _, n := range tree.Nodes {
		for _, s := range n.Segs {
			if s.Kind == types.SegForward && s.Ref != "" {
				if childErr := f.walk(ctx, r, s.Ref, depth+1, budget, images, seen); childErr != nil {
					if ctx.Err() != nil {
						return childErr
					}
					f.o.Logf("转发第 %d 层取回失败：%s", depth+1, stateOf(childErr))
				}
			}
		}
	}
	return nil
}
