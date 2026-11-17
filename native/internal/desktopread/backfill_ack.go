package desktopread

import (
	"context"
	"sumika-native/internal/api"
)

func (s *Service) SetGapAcknowledged(ctx context.Context, ref api.GapRef, acknowledged bool) (api.GapView, error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return api.GapView{}, err
	}
	defer done()
	if _, _, err = s.gapScope(api.GapQuery{ConversationKey: ref.ConversationKey}); err != nil {
		return api.GapView{}, err
	}
	if s.backfills == nil || s.backfills.SetAcknowledged == nil {
		return api.GapView{}, api.BackfillFailure("readonly")
	}
	return s.backfills.SetAcknowledged(ctx, ref, acknowledged)
}

var _ api.GapAcknowledgements = (*Service)(nil)
