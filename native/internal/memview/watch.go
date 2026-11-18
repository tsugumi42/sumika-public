package memview

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/memory"
)

type subscription[T any] struct {
	out    chan api.Snapshot[T]
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (s *subscription[T]) Updates() <-chan api.Snapshot[T] { return s.out }
func (s *subscription[T]) Close()                          { s.once.Do(s.cancel); <-s.done }

const topChoices = 20 // 下拉里放的会话、关于谁各多少个（多的靠搜索）

// 某个角色的概览没算成（5 秒时限到了、闸被别的查询占着、库一时打不开）以后，库没有新提交也隔 Options.Retry（默认 10 秒）再算一次；
// 不然这个角色会一直显示"读不了"，直到库下一次被提交

// WatchMemory：概览订阅。订阅活着才轮询（每 Poll 一次 PRAGMA data_version，走专用连接，不占查询的闸）；
// 值变了只重算那个角色，Rev 加一，再发布一份新快照。关订阅（或服务）就停，Close 等 worker 退出
func (s *Service) WatchMemory(ctx context.Context) (api.Subscription[api.MemoryOverview], error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	sub := &subscription[api.MemoryOverview]{out: make(chan api.Snapshot[api.MemoryOverview]), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(sub.done)
		defer close(sub.out)
		defer done()
		defer cancel()
		s.watchLoop(ctx, sub)
	}()
	return sub, nil
}

func (s *Service) watchLoop(ctx context.Context, sub *subscription[api.MemoryOverview]) {
	cache := map[string]api.MemoryCharacter{}
	version := map[string]int64{}
	rev := map[string]uint64{}
	dirty := map[string]bool{}
	retryAt := map[string]time.Time{} // 概览没算成的角色：到点再算
	for _, c := range s.chars {
		dirty[c.ID] = true
	}
	var snapVersion uint64
	var last api.MemoryOverview
	have := false
	tick := time.NewTicker(s.poll())
	defer tick.Stop()
	for first := true; ; first = false {
		if !first {
			for _, c := range s.chars {
				if v, ok := s.dataVersion(ctx, c.ID); ok && v != version[c.ID] {
					version[c.ID] = v
					rev[c.ID]++
					dirty[c.ID] = true
				}
			}
			now := time.Now() // 重试是计时，不用 Options.Now（那是给“现在几点”的显示用的，测试里固定）
			for id, at := range retryAt {
				if !now.Before(at) {
					dirty[id] = true
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		anyDirty := false
		for _, d := range dirty {
			anyDirty = anyDirty || d
		}
		if first || anyDirty {
			var overviewErr error
			for _, c := range s.chars {
				if !dirty[c.ID] {
					continue
				}
				// 先记版本再数：数的过程中有提交，下一次轮询会看到版本变了，再重算
				if v, ok := s.dataVersion(ctx, c.ID); ok {
					version[c.ID] = v
				}
				_, failedBefore := retryAt[c.ID]
				delete(retryAt, c.ID)
				ch, err := s.character(ctx, c, rev[c.ID])
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					overviewErr = err
					retryAt[c.ID] = time.Now().Add(s.retry())
				} else if failedBefore {
					// 失败期间库里的东西页面没看到：算成一次变化，列表空着的话会重取
					rev[c.ID]++
					ch.Rev = rev[c.ID]
				}
				cache[c.ID] = ch
				dirty[c.ID] = false
			}
			ov := api.MemoryOverview{}
			for _, c := range s.chars {
				ov.Characters = append(ov.Characters, cache[c.ID])
			}
			// 重试了一遍还是同样的结果：不再发一份一样的快照
			if !have || !reflect.DeepEqual(ov, last) {
				snapVersion++
				res := api.Result[api.MemoryOverview]{State: api.Ready, Value: ov}
				if overviewErr != nil && have {
					res = api.Result[api.MemoryOverview]{State: api.Failed, Value: last, Err: overviewErr, Stale: true}
				}
				last, have = ov, true
				select {
				case sub.out <- api.Snapshot[api.MemoryOverview]{Version: snapVersion, Result: res}:
				case <-ctx.Done():
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// dataVersion：这个角色库现在的 data_version。库还没打开就先打开；没有库、打不开都当没有（false）
func (s *Service) dataVersion(ctx context.Context, id string) (int64, bool) {
	b, err := s.browser(id)
	if err != nil {
		return 0, false
	}
	v, err := b.DataVersion(ctx)
	return v, err == nil
}

// character 算一个角色的概览：计数、迁移与否、隔离回执、两个下拉。没有库 HasDB 为假；打不开 / 读不了写原因，不抛错
func (s *Service) character(ctx context.Context, c Character, rev uint64) (api.MemoryCharacter, error) {
	out := api.MemoryCharacter{ID: c.ID, Name: memory.CleanLine(c.Name, 40), Rev: rev, ConfirmNeed: memory.ConfirmOccasionsFor(c.Browse.ConfirmOccasions)}
	b, err := s.browser(c.ID)
	if errors.Is(err, errNoDB) {
		return out, nil
	}
	if err != nil {
		out.HasDB, out.Err = true, err.Error()
		return out, err
	}
	out.HasDB = true
	var counts memory.BrowseCounts
	var convs, subjects []memory.ValueCount
	err = s.run(ctx, func(ctx context.Context) (err error) {
		if counts, err = b.Counts(ctx); err != nil {
			return err
		}
		if convs, err = b.TopValues(ctx, "conversation", topChoices); err != nil {
			return err
		}
		subjects, err = b.TopValues(ctx, "subject", topChoices)
		return err
	})
	if err != nil {
		out.Err = err.Error()
		return out, err
	}
	out.Total, out.Migrated = counts.Total, counts.Schema.Migrated()
	for kind, groups := range counts.Facts {
		for g, n := range groups {
			out.Counts = append(out.Counts, api.MemoryCount{Kind: memory.CleanLine(kind, 30), Group: g, N: n})
		}
	}
	for g, n := range counts.Episodes {
		out.Counts = append(out.Counts, api.MemoryCount{Kind: "episode", Group: g, N: n})
	}
	sort.Slice(out.Counts, func(i, j int) bool {
		a, b := out.Counts[i], out.Counts[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Group < b.Group
	})
	q := counts.Quarantine
	out.Quarantine = api.MemoryQuarantine{Active: q.Active, Facts: q.Facts, Candidates: q.Candidates, Episodes: q.Episodes}
	for _, v := range convs {
		out.Conversations = append(out.Conversations, api.MemoryChoice{Value: v.Value, Label: s.convLabel(v.Value), N: v.N})
	}
	for _, v := range subjects {
		out.Subjects = append(out.Subjects, api.MemoryChoice{Value: v.Value, Label: whoLabel(s.who(v.Value)), N: v.N})
	}
	return out, nil
}
