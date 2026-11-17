// Package desktopread 实现桌面的存储只读投影，不依赖窗口或后台装配。
package desktopread

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/api"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

type Service struct {
	stores        *store.Set
	data          string                                         // 数据根：Go 媒体和小黄脸缓存；空 = 媒体未接入
	openMedia     func(string, string, string) (*os.File, error) // 绑定当前数据组；测试替换计数，保证消息页只查库
	mediaReader   *mediastore.Reader
	characters    []api.CharacterView
	ids           []string
	runtime       *Runtime
	readMark      bool // 能不能标已读（WithReadMark）；默认关
	groups        api.LocalGroupWriter
	mu            sync.Mutex
	baselines     map[string]int64
	loaded        map[string]map[string]bool
	active        map[string]int
	queryGate     chan struct{}
	version       uint64
	forwards      ForwardFetcher
	videos        VideoDownloads
	backfills     *BackfillProvider
	forwardBriefs forwardBriefCache // queryGate 内读写，最多 2000 个 (id,fetched_at)。
	// 测试在最新页与区间重查之间安排提交，检查两处元数据不能相互倒退。
	afterPageRead func(context.Context)
	charErrs      map[string]error // 卡读不到的角色：概览里单独报失败，不拿假资料顶

	lifeMu   sync.Mutex
	closed   bool
	inflight sync.WaitGroup
	base     context.Context
	stop     context.CancelFunc
}

func New(stores *store.Set, characters []api.CharacterView, runtime *Runtime) *Service {
	results := make([]api.Result[api.CharacterView], len(characters))
	for i, c := range characters {
		results[i] = api.Result[api.CharacterView]{State: api.Ready, Value: c}
	}
	return NewResults(stores, results, runtime)
}

// NewResults 和 New 一样，但允许某个角色的资料读失败：它的 ID 仍参与可见范围，概览里报失败和原因。
func NewResults(stores *store.Set, characters []api.Result[api.CharacterView], runtime *Runtime) *Service {
	s := &Service{stores: stores, runtime: runtime, baselines: map[string]int64{}, loaded: map[string]map[string]bool{}, active: map[string]int{}, queryGate: make(chan struct{}, 1), charErrs: map[string]error{}}
	s.base, s.stop = context.WithCancel(context.Background())
	for _, c := range characters {
		s.characters = append(s.characters, c.Value)
		s.ids = append(s.ids, c.Value.ID)
		if c.State != api.Ready {
			err := c.Err
			if err == nil {
				err = errors.New("角色资料读不到")
			}
			s.charErrs[c.Value.ID] = err
		}
	}
	return s
}
func keyOf(conv string) api.ConversationKey {
	platform, _ := store.PlatformOfConversation(conv)
	k := api.ConversationKey{Platform: platform, Conversation: conv, Scene: "private"}
	if platform == "local" {
		k.Channel = "console"
		if _, group := store.LocalGroupID(conv); group {
			k.Scene = "group"
		}
	} else if platform == "qq" {
		k.Channel = "qq"
		if strings.HasPrefix(conv, "qq:group:") {
			k.Scene = "group"
		}
	}
	return k
}
func (s *Service) scope(req api.ConversationRequest) (*store.Store, store.DisplayScope, error) {
	if req.Limit <= 0 || req.Limit > 1000 {
		return nil, store.DisplayScope{}, store.ErrDisplayParameter
	}
	k := keyOf(req.Key.Conversation)
	if k != req.Key {
		return nil, store.DisplayScope{}, store.ErrDisplayScope
	}
	st, err := s.stores.ForConversation(k.Conversation)
	return st, store.DisplayScope{Platform: k.Platform, Conversation: k.Conversation, Characters: s.ids, Character: req.Character}, err
}

// displayName 是消息上方的发言人名字。主人也用原本的群名片或昵称（2026-11-02 主人：群里引用写"主人"不对，要原本的名字；
// 认主人靠 ActorKind，界面拿它给名字上强调色），存下的名字都没有才写"主人"；
// 别人是 store 查出来的群名片或昵称，都查不到才退回号码。
func (s *Service) displayName(r store.DisplayRow) string {
	// 宿主自动发的输入（目标续轮、定期任务）用主人的身份跑，但不是主人说的话：界面上标成系统发的
	if r.ActorKind == "owner" {
		switch r.Origin {
		case "goal":
			return "目标续轮"
		case "loop":
			return "定期任务"
		}
	}
	if r.ActorKind == "system" {
		return "系统"
	}
	if r.ActorKind == "owner" {
		if r.DisplayName != "" {
			return r.DisplayName
		}
		return "主人"
	}
	if r.ActorKind == "character" {
		for _, c := range s.characters {
			if c.ID == r.ActorID {
				return c.Name
			}
		}
	}
	if r.DisplayName != "" {
		return r.DisplayName
	}
	return r.ActorID
}
func (s *Service) project(k api.ConversationKey, r store.DisplayRow) api.MessageView {
	ref := api.EventRef{Conversation: k, Event: api.EventKey{Platform: k.Platform, EventID: r.ID}}
	m := api.MessageView{Ref: ref, Seq: r.Seq, ActorKind: r.ActorKind, ActorID: r.ActorID, ActorUID: r.ActorUID, DisplayName: s.displayName(r), Kind: r.Kind, Origin: r.Origin, CreatedAt: r.CreatedAt, Text: r.Text, Recalled: r.Recalled}
	m.Service = serviceText(r, m.DisplayName)
	m.Process = s.processOf(k, r)
	if p := parseEnvelope(r.Segments); p.ok {
		// 只认角色自己说的话上的标记：别的发言人的 segments 里带这个键也不算（标记由宿主写，但读的一侧不信任何人说的话）
		if pm := p.env.Proactive; pm != nil && k.Platform == "local" && r.ActorKind == "character" && r.Origin == "character" {
			m.Proactive = &api.ProactiveMark{Thread: pm.Thread, Reason: pm.Reason}
		}
		m.Unsent = p.env.Unsent
		m.Partial = p.env.Partial
		m.Aborted = p.env.Aborted
		m.Segments = displaySegments(ref, p, r.AtNames, r.MediaMeta)
	}
	if r.HasPlan {
		m.Unsent, m.Partial, m.Aborted = r.Unsent, r.Partial, r.Aborted
	}
	if r.ReplyTo != "" {
		target := ref
		target.Event.EventID = r.ReplyTo
		m.ReplyTo = &target
		m.Reply.State = api.Unsupported
		if r.Reply != nil {
			m.Reply = api.Result[api.QuoteView]{State: api.Ready, Value: api.QuoteView{Ref: target, ActorKind: r.Reply.ActorKind, DisplayName: s.displayName(*r.Reply), Text: r.Reply.Text, Recalled: r.Reply.Recalled}}
			if p := parseEnvelope(r.Reply.Segments); p.ok {
				m.Reply.Value.Segments = displaySegments(target, p, r.Reply.AtNames, nil)
			}
			// 第一个引用块的片段（reply_to 就是它的 id）；和她看得到被引用那条同时才交给界面
			if p := parseEnvelope(r.Segments); p.ok {
				for _, lp := range p.env.LocalParts {
					if lp.Quote != nil && lp.Quote.ID == r.ReplyTo {
						fillSnippet(&m.Reply.Value, lp.Quote)
						break
					}
				}
			}
		}
	}
	if p := parseEnvelope(r.Segments); p.ok && len(p.env.LocalParts) > 0 {
		m.Parts = s.projectParts(k, r, p.env.LocalParts)
	}
	for _, x := range r.Reactions {
		m.Reactions = append(m.Reactions, api.ReactionView{ID: x.ID, Ref: x.Ref, ActorKind: x.ActorKind, ActorID: x.ActorID, DisplayName: s.displayName(store.DisplayRow{ActorKind: x.ActorKind, ActorID: x.ActorID, DisplayName: x.DisplayName}), At: x.At})
	}
	return m
}
func (s *Service) pageOf(k api.ConversationKey, p store.DisplayPage) api.MessagePage {
	out := api.MessagePage{List: api.List[api.MessageView]{NextCursor: api.Cursor(p.NextCursor), HasMore: p.HasMore}, HighWater: p.HighWater}
	for _, r := range p.Items {
		out.Items = append(out.Items, s.project(k, r))
	}
	if p.Before != nil {
		v := s.project(k, *p.Before)
		out.Before = &v
	}
	return out
}
func loadKey(req api.ConversationRequest) string {
	return req.Key.Platform + "\x00" + req.Key.Conversation + "\x00" + req.Character
}
func (s *Service) remember(req api.ConversationRequest, items []api.MessageView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := loadKey(req)
	if s.loaded[key] == nil {
		return
	}
	for _, m := range items {
		s.loaded[key][m.Ref.Event.EventID] = true
	}
}
func (s *Service) ReadMessages(ctx context.Context, req api.ConversationRequest, cursor api.Cursor) (api.Result[api.MessagePage], error) {
	st, scope, err := s.scope(req)
	if err != nil {
		return api.Result[api.MessagePage]{State: api.Failed, Err: err}, err
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return api.Result[api.MessagePage]{State: api.Failed, Err: err}, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case s.queryGate <- struct{}{}:
	case <-ctx.Done():
		return api.Result[api.MessagePage]{State: api.Failed, Err: ctx.Err()}, ctx.Err()
	}
	defer func() { <-s.queryGate }()
	p, err := st.ReadDisplay(ctx, scope, string(cursor), req.Limit)
	if err != nil {
		return api.Result[api.MessagePage]{State: api.Failed, Err: err}, err
	}
	out := s.pageOf(req.Key, p)
	if err := s.fillMessageForwards(ctx, req.Key, out.Items); err != nil {
		return failed[api.MessagePage](api.Failed, err)
	}
	if err := ctx.Err(); err != nil {
		return api.Result[api.MessagePage]{State: api.Failed, Err: err}, err
	}
	s.version++
	for i := range out.Items {
		out.Items[i].Version = s.version
	}
	s.remember(req, out.Items)
	return api.Result[api.MessagePage]{State: api.Ready, Value: out}, nil
}

type subscription[T any] struct {
	out    chan api.Snapshot[T]
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (s *subscription[T]) Updates() <-chan api.Snapshot[T] { return s.out }
func (s *subscription[T]) Close()                          { s.once.Do(s.cancel); <-s.done }

// 一个 worker 交付初始状态和刷新；关闭等 worker 退出，不等待消费者收消息。
func watch[T any](ctx context.Context, stores []store.Named, runtime *Runtime, read func(context.Context) (T, error), cleanup ...func()) api.Subscription[T] {
	return watchKey(ctx, stores, runtime, "", read, cleanup...)
}

// watchKey 复用同一订阅生命周期，只让指定失效身份触发读取；空键仍观察全部变更。
func watchKey[T any](ctx context.Context, stores []store.Named, runtime *Runtime, key string, read func(context.Context) (T, error), cleanup ...func()) api.Subscription[T] {
	ctx, cancel := context.WithCancel(ctx)
	sub := &subscription[T]{out: make(chan api.Snapshot[T]), cancel: cancel, done: make(chan struct{})}
	changes := make([]*store.ChangeSub, 0, len(stores))
	for _, st := range stores {
		changes = append(changes, st.Store.SubscribeChanges())
	}
	var runtimeChanges *runtimeSub
	var alerts *alert.Sub
	if runtime != nil {
		runtimeChanges = runtime.subscribe()
		if runtime.alerts != nil {
			alerts, _ = runtime.alerts.SubscribeWithOpen()
		}
	}
	go func() {
		defer close(sub.done)
		defer close(sub.out)
		defer cancel()
		defer func() {
			for _, fn := range cleanup {
				fn()
			}
		}()
		defer func() {
			for _, c := range changes {
				c.Close()
			}
		}()
		if runtimeChanges != nil {
			defer runtimeChanges.close()
		}
		if alerts != nil {
			defer alerts.Close()
		}
		cases := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())}}
		for _, c := range changes {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(c.Wake())})
		}
		if runtimeChanges != nil {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(runtimeChanges.wake)})
		}
		alertIndex := -1
		if alerts != nil {
			alertIndex = len(cases)
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(alerts.C())})
		}
		wakeDirty := false
		wait := func() bool {
			chosen, _, ok := reflect.Select(cases)
			if chosen == 0 {
				return false
			}
			if chosen == alertIndex {
				if !ok {
					return false
				}
				wakeDirty = true
			}
			return true
		}
		var version uint64
		var last T
		have := false
		first := true
		for {
			if ctx.Err() != nil {
				return
			}
			changed := wakeDirty
			wakeDirty = false
			for _, c := range changes {
				dirty := c.Take()
				if (key == "" && len(dirty) > 0) || dirty[key] {
					changed = true
				}
			}
			if runtimeChanges != nil && runtimeChanges.take() {
				changed = true
			}
			if !first && !changed {
				if !wait() {
					return
				}
				continue
			}
			first = false
			query, cancelQuery := context.WithTimeout(ctx, 5*time.Second)
			v, err := read(query)
			cancelQuery()
			if ctx.Err() != nil {
				return
			}
			version++
			result := api.Result[T]{State: api.Ready, Value: v}
			if err != nil {
				result = api.Result[T]{State: api.Failed, Value: last, Err: err, Stale: have}
			} else {
				last = v
				have = true
			}
			select {
			case sub.out <- api.Snapshot[T]{Version: version, Result: result}:
			case <-ctx.Done():
				return
			}
			if !wait() {
				return
			}
		}
	}()
	return sub
}
func (s *Service) WatchConversation(ctx context.Context, req api.ConversationRequest) (api.Subscription[api.ConversationSnapshot], error) {
	st, scope, err := s.scope(req)
	if err != nil {
		return nil, err
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	key := loadKey(req)
	s.mu.Lock()
	if s.loaded[key] == nil {
		s.loaded[key] = map[string]bool{}
	}
	s.active[key]++
	s.mu.Unlock()
	cleanup := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.active[key]--
		if s.active[key] == 0 {
			delete(s.active, key)
			delete(s.loaded, key)
		}
	}
	var water int64
	initial := true
	return watch(ctx, []store.Named{{Platform: req.Key.Platform, Store: st}}, nil, func(ctx context.Context) (api.ConversationSnapshot, error) {
		select {
		case s.queryGate <- struct{}{}:
		case <-ctx.Done():
			return api.ConversationSnapshot{}, ctx.Err()
		}
		defer func() { <-s.queryGate }()
		p, err := st.ReadDisplay(ctx, scope, "", req.Limit)
		if err != nil {
			return api.ConversationSnapshot{}, err
		}
		if s.afterPageRead != nil {
			s.afterPageRead(ctx)
		}
		out := api.ConversationSnapshot{Conversation: api.ConversationView{Key: req.Key}, Page: s.pageOf(req.Key, p)}
		if !initial {
			nextWater := water
			for nextWater < p.HighWater {
				batch, e := st.ReadDisplaySince(ctx, scope, nextWater, p.HighWater)
				if e != nil {
					return out, e
				}
				for _, r := range batch.Items {
					out.Changed = append(out.Changed, s.project(req.Key, r))
				}
				nextWater = batch.HighWater
			}
		}
		s.remember(req, out.Page.Items)
		s.remember(req, out.Changed)
		s.mu.Lock()
		var ids []string
		for id := range s.loaded[loadKey(req)] {
			ids = append(ids, id)
		}
		s.mu.Unlock()
		rows, err := st.ReadDisplayLoaded(ctx, scope, ids)
		if err != nil {
			return out, err
		}
		byID := map[string]api.MessageView{}
		for _, m := range out.Changed {
			byID[m.Ref.Event.EventID] = m
		}
		for _, r := range rows {
			byID[r.ID] = s.project(req.Key, r)
		}
		// 两次读之间可能已有提交，同版本的页和 Changed 必须使用同一份新行。
		for i, m := range out.Page.Items {
			if latest, ok := byID[m.Ref.Event.EventID]; ok {
				out.Page.Items[i] = latest
			}
		}
		out.Changed = nil
		for _, m := range byID {
			out.Changed = append(out.Changed, m)
		}
		sort.Slice(out.Changed, func(i, j int) bool { return out.Changed[i].Seq < out.Changed[j].Seq })
		all := append(append([]api.MessageView(nil), out.Page.Items...), out.Changed...)
		if err := s.fillMessageForwards(ctx, req.Key, all); err != nil {
			return out, err
		}
		s.version++
		for i := range out.Page.Items {
			out.Page.Items[i].Version = s.version
		}
		for i := range out.Changed {
			out.Changed[i].Version = s.version
		}
		water = p.HighWater
		initial = false
		return out, nil
	}, cleanup, done), nil
}
func (s *Service) WatchOverview(ctx context.Context, req api.OverviewRequest) (api.Subscription[api.Overview], error) {
	if req.Limit <= 0 || req.Limit > 1000 {
		return nil, store.ErrDisplayParameter
	}
	if req.Character != "" {
		found := false
		for _, id := range s.ids {
			if id == req.Character {
				found = true
			}
		}
		if !found {
			return nil, store.ErrDisplayScope
		}
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	return watch(ctx, s.stores.Each(), s.runtime, func(ctx context.Context) (api.Overview, error) {
		select {
		case s.queryGate <- struct{}{}:
		case <-ctx.Done():
			return api.Overview{}, ctx.Err()
		}
		defer func() { <-s.queryGate }()
		var out api.Overview
		if s.runtime != nil {
			out.Runtime = s.runtime.snapshot()
		} else {
			out.Runtime.ReasonCode = "runtime_not_attached"
			out.Runtime.Reason = "宿主状态尚未接入"
		}
		for _, c := range s.characters {
			r := api.Result[api.CharacterView]{State: api.Ready, Value: c}
			if err := s.charErrs[c.ID]; err != nil {
				r = api.Result[api.CharacterView]{State: api.Failed, Value: c, Err: err}
			}
			out.Characters.Items = append(out.Characters.Items, r)
		}
		directory, err := s.stores.ReadDisplayConversations(ctx, s.ids, req.Character, string(req.Cursor), req.Limit)
		if err != nil {
			return out, err
		}
		out.Conversations.NextCursor = api.Cursor(directory.NextCursor)
		out.Conversations.HasMore = directory.HasMore
		for _, v := range directory.Items {
			k := keyOf(v.Conversation)
			cv := api.ConversationView{Key: k, Title: v.Title, Observers: v.Observers, CreatedAt: v.CreatedAt, WorkspaceCWD: v.WorkspaceCWD, WorkspaceRoot: v.WorkspaceRoot}
			if v.Last != nil {
				m := s.project(k, *v.Last)
				cv.Last = &m
			}
			if k.Platform == "qq" {
				st, ok := s.stores.Platform("qq")
				if !ok {
					return out, ErrQQStoreClosed
				}
				key := k.Conversation + "\x00" + req.Character
				s.mu.Lock()
				base, ok := s.baselines[key]
				s.mu.Unlock()
				var ptr *int64
				if ok {
					ptr = &base
				}
				count, at, e := st.ReadDisplayUnread(ctx, store.DisplayScope{Platform: "qq", Conversation: k.Conversation, Characters: s.ids, Character: req.Character}, ptr)
				if e != nil {
					return out, e
				}
				s.mu.Lock()
				if _, exists := s.baselines[key]; !exists {
					s.baselines[key] = at
				}
				s.mu.Unlock()
				cv.HasUnread = true
				cv.Unread = count
			}
			out.Conversations.Items = append(out.Conversations.Items, cv)
		}
		state, reason := api.Ready, ""
		if s.groups == nil {
			state, reason = api.Unsupported, "当前数据源只看历史，不能建群或发消息"
		}
		out.Capabilities = []api.Capability{{Name: "local-group", State: state, Reason: reason}}
		if s.runtime == nil {
			out.Capabilities = append(out.Capabilities, api.Capability{Name: "runtime", State: api.Unsupported, Reason: "宿主状态尚未接入"})
		}
		return out, nil
	}, done), nil
}

// Reader 把展示、媒体与设置合成桌面用的 api.Reader；装配方各自构造好再放进来。
type Reader struct {
	*Service
	SettingsReader
}

var _ api.Reader = Reader{}
