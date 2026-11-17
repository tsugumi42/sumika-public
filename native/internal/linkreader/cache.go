package linkreader

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"sumika-native/internal/store"
)

type CacheOptions struct {
	TTLHours int
	Now      func() time.Time
}
type ContentFetcher interface {
	Fetch(context.Context, Link) (Result, error)
	Resolve(context.Context, Link) (Link, error)
}

// StagedFetcher 先提供分级元信息；只有获许可的消费者才要求完整正文。
type StagedFetcher interface {
	Metadata(context.Context, Link) (Result, error)
	Complete(context.Context, Link, Result) (Result, error)
}
type flight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	result  Result
	err     error
}

// Service 的生命周期由宿主管理；Close 必须早于关库。
type Service struct {
	stores  store.Stores
	fetcher ContentFetcher
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	flights map[string]*flight
	closed  bool
	wg      sync.WaitGroup
}

func NewService(stores store.Stores, fetcher ContentFetcher, o CacheOptions) (*Service, error) {
	if o.TTLHours == 0 {
		o.TTLHours = 24
	}
	if o.TTLHours < 0 || o.TTLHours > 87600 || stores == nil || fetcher == nil {
		return nil, failure("invalid_options", "cache")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{stores: stores, fetcher: fetcher, ttl: time.Duration(o.TTLHours) * time.Hour, now: o.Now, flights: map[string]*flight{}}, nil
}
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for _, f := range s.flights {
		f.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Service) Get(ctx context.Context, raw string) (Result, error) {
	l, ok := Recognize(raw)
	if !ok {
		return Result{}, failure("invalid_target", "")
	}
	r, err := s.get(ctx, l)
	if err == nil {
		// 按这一次用户写的重写（缓存里存的是第一次那人写的，或者空）；CleanURL 照旧是小写的键
		r.DisplayURL = githubDisplay(raw, l)
	}
	return r, err
}

// GetPermitted 不让受限展开消费者先抓完整 pixiv 内容再过滤。
func (s *Service) GetPermitted(ctx context.Context, raw string, permit func(*Result) bool) (Result, error) {
	l, ok := Recognize(raw)
	if !ok {
		return Result{}, failure("invalid_target", "")
	}
	if l.Platform != "pixiv" {
		return s.get(ctx, l)
	}
	r, err := s.getStage(ctx, l, true)
	if err != nil {
		return Result{}, err
	}
	if !permit(&r) {
		return r, nil
	}
	return s.get(ctx, l)
}

func (s *Service) get(ctx context.Context, l Link) (Result, error) { return s.getStage(ctx, l, false) }
func (s *Service) getStage(ctx context.Context, l Link, metadata bool) (Result, error) {
	key := l.Clean
	if metadata {
		key += "\x00metadata"
	}
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return Result{}, failure("closed", "cache")
		}
		f := s.flights[key]
		if f != nil && f.waiters == 0 {
			// 已取消的取数退出后才能换任务，避免同键新旧请求重叠。
			done := f.done
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-done:
				continue
			}
		}
		if f == nil {
			// singleflight 不管理等待者引用数；这里最后一个离开才取消共享任务。
			work, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			f = &flight{done: make(chan struct{}), cancel: cancel}
			s.flights[key] = f
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer cancel()
				r, err := s.load(work, l, metadata)
				s.mu.Lock()
				f.result, f.err = r, err
				if s.flights[key] == f {
					delete(s.flights, key)
				}
				close(f.done)
				s.mu.Unlock()
			}()
		}
		f.waiters++
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			f.waiters--
			if f.waiters == 0 {
				f.cancel()
			}
			s.mu.Unlock()
		}()
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-f.done:
			// Result 含 map、切片和指针，各等待者不能修改别人的快照。
			if f.err != nil {
				return Result{}, f.err
			}
			b, err := json.Marshal(f.result)
			if err != nil {
				return Result{}, err
			}
			var r Result
			err = json.Unmarshal(b, &r)
			return r, err
		}
	}
}
func (s *Service) load(ctx context.Context, l Link, metadata bool) (Result, error) {
	st := s.stores.TraceStore()
	if l.Kind == "short" {
		target, at, found, err := st.LinkAlias(ctx, l.Clean)
		if err != nil {
			return Result{}, err
		}
		var resolved Link
		var ok bool
		if found && s.now().Before(at.Add(s.ttl)) {
			resolved, ok = Recognize(target)
			if !ok || resolved.Kind == "short" {
				return Result{}, failure("invalid_response", "alias")
			}
		} else {
			resolved, err = s.fetcher.Resolve(ctx, l)
			if err != nil {
				return Result{}, err
			}
			canonical, valid := Recognize(resolved.Clean)
			if !valid || canonical.Kind == "short" || canonical.Clean != resolved.Clean {
				return Result{}, failure("invalid_response", "alias")
			}
			resolved = canonical
			if err = st.PutLinkAlias(ctx, l.Clean, resolved.Clean, s.now()); err != nil {
				return Result{}, err
			}
		}
		return s.getStage(ctx, resolved, metadata)
	}
	cached, found, err := st.LinkResult(ctx, l.Clean)
	if err != nil {
		return Result{}, err
	}
	if found && s.now().Before(cached.FetchedAt.Add(s.ttl)) {
		var r Result
		if err = json.Unmarshal(cached.JSON, &r); err != nil {
			return Result{}, err
		}
		if !validResult(l, r) || !r.FetchedAt.Equal(cached.FetchedAt) {
			return Result{}, failure("invalid_response", "cache")
		}
		return r, nil
	}
	var r Result
	if metadata {
		staged, ok := s.fetcher.(StagedFetcher)
		if !ok {
			return Result{}, failure("staged_fetch_unavailable", "metadata")
		}
		r, err = staged.Metadata(ctx, l)
		if err != nil {
			return Result{}, err
		}
		if r.CleanURL != l.Clean || r.Platform != l.Platform || r.ID != l.ID || r.Kind != l.Kind || r.FetchedAt.IsZero() || r.Source == "" {
			return Result{}, failure("invalid_response", "metadata")
		}
		r.Stage = "metadata"
		return r, nil
	}
	if staged, ok := s.fetcher.(StagedFetcher); ok && l.Platform == "pixiv" {
		meta, e := s.getStage(ctx, l, true)
		if e != nil {
			return Result{}, e
		}
		r, err = staged.Complete(ctx, l, meta)
	} else {
		r, err = s.fetcher.Fetch(ctx, l)
	}
	if err != nil {
		return Result{}, err
	}
	if !validResult(l, r) {
		return Result{}, failure("invalid_response", "cache")
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	if r.SkipCache {
		return r, nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	err = st.PutLinkResult(ctx, store.LinkResult{CleanURL: l.Clean, FetchedAt: r.FetchedAt, JSON: b})
	return r, err
}
func validResult(l Link, r Result) bool {
	return r.CleanURL == l.Clean && r.Platform == l.Platform && r.Kind == l.Kind && r.ID == l.ID && !r.FetchedAt.IsZero() && r.Source != "" && (r.Text.State == "available" || r.Title.State == "available")
}

// Associate 与取数分开，失败的链接也可计数；result 为 nil 时不伪造成功详情。
func (s *Service) Associate(ctx context.Context, conversation string, seq int64, raw string, result *Result) (int, error) {
	l, ok := Recognize(raw)
	if !ok {
		return 0, failure("invalid_target", "")
	}
	st, err := s.stores.ForConversation(conversation)
	if err != nil {
		return 0, err
	}
	var b []byte
	if result != nil {
		if !validResult(l, *result) {
			return 0, failure("invalid_response", "association")
		}
		b, err = json.Marshal(result)
		if err != nil {
			return 0, err
		}
	}
	return st.RecordLink(ctx, conversation, seq, l.Clean, b)
}
func (s *Service) Detail(ctx context.Context, conversation string, seq int64, raw string) (Result, bool, error) {
	l, ok := Recognize(raw)
	if !ok {
		return Result{}, false, failure("invalid_target", "")
	}
	st, err := s.stores.ForConversation(conversation)
	if err != nil {
		return Result{}, false, err
	}
	b, found, err := st.MessageLink(ctx, conversation, seq, l.Clean)
	if err != nil || !found || len(b) == 0 {
		return Result{}, false, err
	}
	var r Result
	err = json.Unmarshal(b, &r)
	return r, err == nil, err
}
