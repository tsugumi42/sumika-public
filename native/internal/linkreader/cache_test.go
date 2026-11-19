package linkreader

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

type cacheFetcher struct {
	fetch   func(context.Context, Link) (Result, error)
	resolve func(context.Context, Link) (Link, error)
}

func (f cacheFetcher) Fetch(c context.Context, l Link) (Result, error) { return f.fetch(c, l) }
func (f cacheFetcher) Resolve(c context.Context, l Link) (Link, error) { return f.resolve(c, l) }
func cacheStores(t *testing.T) (*store.Set, paths.Paths) {
	t.Helper()
	p, e := paths.Init(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	st, e := store.OpenGo(p, "local", "qq")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	return st, p
}
func cacheService(t *testing.T, st *store.Set, f ContentFetcher, now func() time.Time) *Service {
	t.Helper()
	s, e := NewService(st, f, CacheOptions{Now: now})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func sampleResult(l Link, at time.Time) Result {
	r := newResult(l, "offline")
	r.Text = available("正文甲")
	r.FetchedAt = at.UTC()
	r.Media = available([]Media{{Type: "video", URL: available("https://video.twimg.com/a?token=sample&expires=90001"), Size: missing[int64]("not_returned")}})
	r.Blocks = []Block{{Type: "text", Text: "正文甲"}, {Type: "video", Media: &r.Media.Value[0]}}
	return r
}

const cacheURL = "https://x.com/example/status/90001"

func TestCacheRestartExpiryAndFailure(t *testing.T) {
	ctx := context.Background()
	st, p := cacheStores(t)
	at := time.Date(2026, 10, 30, 0, 0, 0, 123, time.UTC)
	now := at
	calls := 0
	fail := false
	invalid := false
	f := cacheFetcher{fetch: func(_ context.Context, l Link) (Result, error) {
		calls++
		if fail {
			return Result{}, failure("network", "offline")
		}
		r := sampleResult(l, now)
		if invalid {
			r.CleanURL = "https://x.com/example/status/90002"
		}
		return r, nil
	}}
	s := cacheService(t, st, f, func() time.Time { return now })
	want, e := s.Get(ctx, cacheURL+"?s=20")
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	if e = st.Close(); e != nil {
		t.Fatal(e)
	}
	st, e = store.OpenGo(p, "local", "qq")
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	s = cacheService(t, st, f, func() time.Time { return now })
	now = at.Add(24*time.Hour - time.Nanosecond)
	got, e := s.Get(ctx, cacheURL+"?t=tracking")
	if e != nil || !reflect.DeepEqual(got, want) || calls != 1 {
		t.Fatalf("restart/media: calls=%d err=%v got=%+v", calls, e, got)
	}
	now = at.Add(24 * time.Hour)
	fail = true
	_, e = s.Get(ctx, cacheURL)
	wantError(t, e, "network")
	if calls != 2 {
		t.Fatalf("exact expiry calls=%d", calls)
	}
	cached, found, e := st.TraceStore().LinkResult(ctx, cacheURL)
	if e != nil || !found || !cached.FetchedAt.Equal(at) {
		t.Fatalf("failed refresh changed success: %+v %v", cached, e)
	}
	_, e = s.Get(ctx, cacheURL)
	wantError(t, e, "network")
	if calls != 3 {
		t.Fatalf("failure cached: %d", calls)
	}
	_, e = s.Get(ctx, "https://x.com/example/status/90002")
	wantError(t, e, "network")
	_, found, e = st.TraceStore().LinkResult(ctx, "https://x.com/example/status/90002")
	if e != nil || found {
		t.Fatal("initial failure persisted", e)
	}
	fail = false
	invalid = true
	_, e = s.Get(ctx, cacheURL)
	wantError(t, e, "invalid_response")
	invalid = false
	now = now.Add(time.Second)
	got, e = s.Get(ctx, cacheURL)
	if e != nil || !got.FetchedAt.Equal(now) {
		t.Fatal("retry failed", e)
	}
}
func waitFlight(t *testing.T, s *Service, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		f := s.flights[key]
		ok := f != nil && f.waiters == n
		s.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %d waiters on %s", n, key)
		}
		runtime.Gosched()
	}
}
func releaseOnCleanup(t *testing.T, ch chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case <-ch:
		default:
			close(ch)
		}
	})
}
func TestCacheConcurrentWaiterCancellation(t *testing.T) {
	st, _ := cacheStores(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	f := cacheFetcher{fetch: func(ctx context.Context, l Link) (Result, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return sampleResult(l, time.Now()), nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}}
	s := cacheService(t, st, f, nil)
	releaseOnCleanup(t, release)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, e := s.Get(ctx, cacheURL); first <- e }()
	<-started
	const n = 12
	out := make(chan Result, n)
	errs := make(chan error, n)
	for range n {
		go func() { r, e := s.Get(context.Background(), cacheURL+"?s=tracking"); out <- r; errs <- e }()
	}
	waitFlight(t, s, cacheURL, n+1)
	cancel()
	if e := <-first; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	waitFlight(t, s, cacheURL, n)
	if calls.Load() != 1 {
		t.Fatalf("requests=%d", calls.Load())
	}
	close(release)
	for range n {
		r := <-out
		if e := <-errs; e != nil || r.Text.Value != "正文甲" {
			t.Fatal(r, e)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("requests=%d", calls.Load())
	}
}
func TestCacheAllWaitersCancelAndClose(t *testing.T) {
	for _, closeService := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "close"}[closeService], func(t *testing.T) {
			st, _ := cacheStores(t)
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			abort := make(chan struct{})
			f := cacheFetcher{fetch: func(ctx context.Context, l Link) (Result, error) {
				close(started)
				select {
				case <-ctx.Done():
				case <-abort:
				}
				close(canceled)
				<-release
				return sampleResult(l, time.Now()), nil
			}}
			s := cacheService(t, st, f, nil)
			releaseOnCleanup(t, release)
			releaseOnCleanup(t, abort)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := make(chan error, 1)
			go func() { _, e := s.Get(ctx, cacheURL); out <- e }()
			<-started
			closed := make(chan struct{})
			if closeService {
				go func() { s.Close(); close(closed) }()
			} else {
				cancel()
			}
			select {
			case <-canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("shared task was not canceled")
			}
			close(release)
			if e := <-out; e == nil {
				t.Fatal("canceled task succeeded")
			}
			if closeService {
				<-closed
				_, e := s.Get(context.Background(), cacheURL)
				wantError(t, e, "closed")
			} else {
				s.Close()
			}
			_, found, e := st.TraceStore().LinkResult(context.Background(), cacheURL)
			if e != nil || found {
				t.Fatal("late cache write", e)
			}
		})
	}
}
func TestCacheShortLinksConverge(t *testing.T) {
	st, p := cacheStores(t)
	var resolves, fetches atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	target := link(t, cacheURL)
	f := cacheFetcher{resolve: func(context.Context, Link) (Link, error) { resolves.Add(1); return target, nil }, fetch: func(ctx context.Context, l Link) (Result, error) {
		if fetches.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return sampleResult(l, time.Now()), nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}}
	s := cacheService(t, st, f, nil)
	releaseOnCleanup(t, release)
	out := make(chan error, 4)
	go func() { _, e := s.Get(context.Background(), "https://t.co/one?utm_source=a"); out <- e }()
	<-started
	for _, raw := range []string{"https://t.co/one?utm_source=b", "https://t.co/two", cacheURL} {
		go func() { _, e := s.Get(context.Background(), raw); out <- e }()
	}
	waitFlight(t, s, "https://t.co/one", 2)
	waitFlight(t, s, cacheURL, 3)
	close(release)
	for range 4 {
		if e := <-out; e != nil {
			t.Fatal(e)
		}
	}
	if resolves.Load() != 2 || fetches.Load() != 1 {
		t.Fatalf("resolve=%d fetch=%d", resolves.Load(), fetches.Load())
	}
	s.Close()
	st.Close()
	st, e := store.OpenGo(p, "local", "qq")
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	s = cacheService(t, st, f, nil)
	if _, e = s.Get(context.Background(), "https://t.co/one"); e != nil || resolves.Load() != 2 || fetches.Load() != 1 {
		t.Fatal("alias restart", e, resolves.Load(), fetches.Load())
	}
}
func TestLinkAssociationCountsAndScopes(t *testing.T) {
	st, p := cacheStores(t)
	s := cacheService(t, st, cacheFetcher{}, nil)
	ctx := context.Background()
	r := sampleResult(link(t, cacheURL), time.Now())
	conversations := []string{"console:main", "console:group:90001", "qq:private:90001:90002", "qq:group:90001", "qq:private:90003:90002"}
	for _, conv := range conversations {
		for seq := int64(1); seq <= 3; seq++ {
			previous, e := s.Associate(ctx, conv, seq, cacheURL+"?s="+string(rune('0'+seq)), &r)
			if e != nil || previous != int(seq-1) {
				t.Fatalf("%s seq=%d previous=%d err=%v", conv, seq, previous, e)
			}
		}
		previous, e := s.Associate(ctx, conv, 1, cacheURL+"?t=replay", &r)
		if e != nil || previous != 0 {
			t.Fatal("replay", previous, e)
		}
		previous, e = s.Associate(ctx, conv, 4, cacheURL, nil)
		if e != nil || previous != 3 {
			t.Fatal("replay counted", previous, e)
		}
	}
	if _, e := s.Associate(ctx, "unknown:group:90001", 1, cacheURL, &r); e == nil {
		t.Fatal("unknown platform accepted")
	}
	if _, found, e := s.Detail(ctx, "qq:group:90002", 1, cacheURL); e != nil || found {
		t.Fatal("cross-conversation detail", e)
	}
	s.Close()
	st.Close()
	st, e := store.OpenGo(p, "local", "qq")
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	s = cacheService(t, st, cacheFetcher{}, nil)
	got, found, e := s.Detail(ctx, "qq:group:90001", 1, cacheURL)
	if e != nil || !found || !reflect.DeepEqual(got, r) {
		t.Fatal("detail restart", got, found, e)
	}
	previous, e := s.Associate(ctx, "qq:group:90001", 5, cacheURL, nil)
	if e != nil || previous != 4 {
		t.Fatal("count restart", previous, e)
	}
	for _, name := range []string{"local", "qq"} {
		platform, _ := st.Platform(name)
		var count int
		if e := platform.Events.QueryRow(`SELECT count(*) FROM message_links WHERE conversation LIKE 'qq:%'`).Scan(&count); e != nil {
			t.Fatal(e)
		}
		if (name == "local" && count != 0) || (name == "qq" && count != 13) {
			t.Fatalf("%s qq rows=%d", name, count)
		}
	}
}
func TestCacheContentKeysRemainDistinct(t *testing.T) {
	st, _ := cacheStores(t)
	calls := 0
	f := cacheFetcher{fetch: func(_ context.Context, l Link) (Result, error) { calls++; return sampleResult(l, time.Now()), nil }}
	s := cacheService(t, st, f, nil)
	for _, raw := range []string{cacheURL + "?unknown=1", cacheURL + "?unknown=2", "https://www.bilibili.com/video/av90001?p=1&t=1", "https://www.bilibili.com/video/av90001?p=2&t=1", "https://www.bilibili.com/video/av90001?p=1&t=2"} {
		if _, e := s.Get(context.Background(), raw); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 5 {
		t.Fatal("content keys merged", calls)
	}
}

type joinedContext struct {
	context.Context
	once   sync.Once
	joined chan struct{}
}

func (c *joinedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}

func TestCacheCanceledTaskExitsBeforeReplacement(t *testing.T) {
	st, _ := cacheStores(t)
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var calls, active, peak atomic.Int32
	f := cacheFetcher{fetch: func(ctx context.Context, l Link) (Result, error) {
		n := calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		if n == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return Result{}, ctx.Err()
		}
		return sampleResult(l, time.Now()), nil
	}}
	s := cacheService(t, st, f, nil)
	releaseOnCleanup(t, release)
	first, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstOut := make(chan error, 1)
	go func() { _, err := s.Get(first, cacheURL); firstOut <- err }()
	<-started
	s.mu.Lock()
	old := s.flights[cacheURL]
	s.mu.Unlock()
	cancelFirst()
	if err := <-firstOut; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-canceled
	second, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	joining := &joinedContext{Context: second, joined: make(chan struct{})}
	secondOut := make(chan error, 1)
	go func() { _, err := s.Get(joining, cacheURL); secondOut <- err }()
	<-joining.joined
	// 旧取数停在返回屏障；新请求已经进入等待，不能先换任务。
	s.mu.Lock()
	retained := s.flights[cacheURL] == old
	s.mu.Unlock()
	if !retained {
		t.Fatal("replacement started before canceled task exited")
	}
	cancelSecond()
	if err := <-secondOut; !errors.Is(err, context.Canceled) {
		t.Fatal("replacement wait is not cancelable", err)
	}
	thirdOut := make(chan error, 1)
	go func() { _, err := s.Get(context.Background(), cacheURL); thirdOut <- err }()
	close(release)
	if err := <-thirdOut; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || peak.Load() != 1 {
		t.Fatalf("calls=%d peak=%d", calls.Load(), peak.Load())
	}
}
