package desktopread

import (
	"context"
	"strings"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// BackfillProvider 由宿主注入账号和同一协调器；读服务不取得适配器或另建工人。
type BackfillProvider struct {
	Account         string
	Project         func(store.ChannelGap) api.GapView
	Alignment       func(context.Context) (api.AlignmentView, error)
	RequestGap      func(context.Context, api.GapRef) (api.BackfillJob, error)
	RequestAccount  func(context.Context) (api.BackfillJob, error)
	Job             func(string) (api.BackfillJob, error)
	SetAcknowledged func(context.Context, api.GapRef, bool) (api.GapView, error)
}

func (s *Service) WithBackfills(p BackfillProvider) *Service { s.backfills = &p; return s }

func (s *Service) gapScope(q api.GapQuery) (*store.Store, store.GapWindow, error) {
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 200 {
		return nil, store.GapWindow{}, api.BackfillFailure("invalid")
	}
	st, scope, err := s.scope(api.ConversationRequest{Key: q.ConversationKey, Character: q.Character, Limit: 1})
	if err != nil {
		return nil, store.GapWindow{}, err
	}
	if q.ConversationKey.Platform == "qq" && strings.HasPrefix(q.ConversationKey.Conversation, "qq:private:") && s.backfills != nil && s.backfills.Account != "" && !strings.HasPrefix(q.ConversationKey.Conversation, "qq:private:"+s.backfills.Account+":") {
		return nil, store.GapWindow{}, api.BackfillFailure("invalid")
	}
	return st, store.GapWindow{Scope: scope, FromMS: q.FromMS, ToMS: q.ToMS, Cursor: string(q.Cursor), Limit: limit}, nil
}
func (s *Service) readGaps(ctx context.Context, st *store.Store, q store.GapWindow) (api.List[api.GapView], error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case s.queryGate <- struct{}{}:
	case <-ctx.Done():
		return api.List[api.GapView]{}, ctx.Err()
	}
	defer func() { <-s.queryGate }()
	page, err := st.ReadGapWindow(ctx, q)
	if err != nil {
		return api.List[api.GapView]{}, err
	}
	out := api.List[api.GapView]{Items: []api.GapView{}, NextCursor: api.Cursor(page.NextCursor), HasMore: page.HasMore}
	for _, g := range page.Items {
		v := GapProjection(g)
		if s.backfills != nil {
			v = s.backfills.Project(g)
		}
		out.Items = append(out.Items, v)
	}
	return out, ctx.Err()
}
func GapProjection(g store.ChannelGap) api.GapView {
	return api.GapView{GapRef: api.GapRef{ConversationKey: keyOf(g.Conversation), ID: g.ID, FromMS: g.FromMS, ToMS: g.ToMS}, Reason: g.Reason, State: g.State, Tries: g.Tries, LastTryMS: g.LastTryMS, LastManualMS: g.LastManualMS, CreatedMS: g.CreatedMS, UpdatedMS: g.UpdatedMS, AcknowledgedMS: g.AcknowledgedMS, OriginalCount: 1, ReasonCode: "readonly"}
}
func (s *Service) ReadGaps(ctx context.Context, q api.GapQuery) (api.Result[api.List[api.GapView]], error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return failed[api.List[api.GapView]](api.Failed, err)
	}
	defer done()
	st, w, err := s.gapScope(q)
	if err != nil {
		return failed[api.List[api.GapView]](api.Failed, err)
	}
	v, err := s.readGaps(ctx, st, w)
	if err != nil {
		return failed[api.List[api.GapView]](api.Failed, err)
	}
	return api.Result[api.List[api.GapView]]{State: api.Ready, Value: v}, nil
}
func (s *Service) WatchGaps(ctx context.Context, q api.GapQuery) (api.Subscription[api.List[api.GapView]], error) {
	st, w, err := s.gapScope(q)
	if err != nil {
		return nil, err
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	return watch(ctx, []store.Named{{Platform: w.Scope.Platform, Store: st}}, nil, func(ctx context.Context) (api.List[api.GapView], error) { return s.readGaps(ctx, st, w) }, done), nil
}
func (s *Service) WatchAlignment(ctx context.Context) (api.Subscription[api.AlignmentView], error) {
	if s.backfills == nil {
		return nil, api.BackfillFailure("unsupported")
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.stores.For("qq")
	if err != nil {
		done()
		return nil, err
	}
	return watchKey(ctx, []store.Named{{Platform: "qq", Store: st}}, nil, "@alignment", s.backfills.Alignment, done), nil
}
func (s *Service) RequestGapBackfill(ctx context.Context, ref api.GapRef) (api.BackfillJob, error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return api.BackfillJob{}, err
	}
	defer done()
	if _, _, err = s.gapScope(api.GapQuery{ConversationKey: ref.ConversationKey}); err != nil {
		return api.BackfillJob{}, err
	}
	if s.backfills == nil || s.backfills.RequestGap == nil {
		return api.BackfillJob{}, api.BackfillFailure("readonly")
	}
	return s.backfills.RequestGap(ctx, ref)
}
func (s *Service) RequestAlignment(ctx context.Context) (api.BackfillJob, error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return api.BackfillJob{}, err
	}
	defer done()
	if s.backfills == nil || s.backfills.RequestAccount == nil {
		return api.BackfillJob{}, api.BackfillFailure("readonly")
	}
	return s.backfills.RequestAccount(ctx)
}
func (s *Service) WatchBackfill(ctx context.Context, id string) (api.Subscription[api.BackfillJob], error) {
	if s.backfills == nil || s.backfills.Job == nil {
		return nil, api.BackfillFailure("unavailable")
	}
	if _, err := s.backfills.Job(id); err != nil {
		return nil, err
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.stores.For("qq")
	if err != nil {
		done()
		return nil, err
	}
	return watchKey(ctx, []store.Named{{Platform: "qq", Store: st}}, nil, "@alignment", func(context.Context) (api.BackfillJob, error) { return s.backfills.Job(id) }, done), nil
}

var _ api.GapReader = (*Service)(nil)
var _ api.BackfillActions = (*Service)(nil)

// ReadAlignmentGaps 的账号由宿主固定，不接受调用者指定账号或绕过 Character 的消息内容。
func (s *Service) ReadAlignmentGaps(ctx context.Context, cursor api.Cursor, limit int) (api.Result[api.List[api.GapView]], error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return failed[api.List[api.GapView]](api.Failed, err)
	}
	defer done()
	if s.backfills == nil || s.backfills.Account == "" {
		return failed[api.List[api.GapView]](api.Failed, api.BackfillFailure("unavailable"))
	}
	if limit == 0 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case s.queryGate <- struct{}{}:
	case <-ctx.Done():
		return failed[api.List[api.GapView]](api.Failed, ctx.Err())
	}
	defer func() { <-s.queryGate }()
	st, err := s.stores.For("qq")
	if err != nil {
		return failed[api.List[api.GapView]](api.Failed, err)
	}
	p, err := st.ReadAccountGaps(ctx, s.backfills.Account, string(cursor), limit)
	if err != nil {
		return failed[api.List[api.GapView]](api.Failed, err)
	}
	out := api.List[api.GapView]{Items: []api.GapView{}, HasMore: p.HasMore, NextCursor: api.Cursor(p.NextCursor)}
	for _, g := range p.Items {
		out.Items = append(out.Items, s.backfills.Project(g))
	}
	return api.Result[api.List[api.GapView]]{State: api.Ready, Value: out}, nil
}
