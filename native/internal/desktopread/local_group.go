package desktopread

import (
	"context"
	"sumika-native/internal/api"
)

var _ api.LocalGroupWriter = (*Service)(nil)

// WithLocalGroups 只在可写后台装配；archive 的读取服务不持有写入实现。
func (s *Service) WithLocalGroups(groups api.LocalGroupWriter) *Service {
	s.groups = groups
	return s
}
func (s *Service) CreateLocalGroup(ctx context.Context, req api.LocalGroupCreate) (api.ConversationView, error) {
	if s.groups == nil {
		return api.ConversationView{}, api.ErrLocalGroupUnsupported
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return api.ConversationView{}, err
	}
	defer done()
	return s.groups.CreateLocalGroup(ctx, req)
}
func (s *Service) SendLocalGroup(ctx context.Context, req api.LocalGroupSend) error {
	if s.groups == nil {
		return api.ErrLocalGroupUnsupported
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	return s.groups.SendLocalGroup(ctx, req)
}
func (s *Service) StopLocalGroup(ctx context.Context, key api.ConversationKey) error {
	if s.groups == nil {
		return api.ErrLocalGroupUnsupported
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	return s.groups.StopLocalGroup(ctx, key)
}
