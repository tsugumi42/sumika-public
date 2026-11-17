package app

import (
	"context"
	"errors"
	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/store"
)

func (a *App) setGapAcknowledged(ctx context.Context, ref api.GapRef, acknowledged bool) (api.GapView, error) {
	if a.opt.Mode == Archive {
		return api.GapView{}, api.BackfillFailure("readonly")
	}
	if !a.backfillAllowed(ref.ConversationKey.Conversation) {
		return api.GapView{}, api.BackfillFailure("invalid")
	}
	st, err := a.set.For("qq")
	if err != nil {
		return api.GapView{}, err
	}
	g, err := readGapRef(ctx, st, ref)
	if err != nil {
		return api.GapView{}, err
	}
	if g.State != "blocked" {
		return api.GapView{}, api.BackfillFailure("invalid")
	}
	g.AcknowledgedMS, err = st.SetGapAcknowledged(ctx, g, acknowledged, a.now().UnixMilli())
	if errors.Is(err, store.ErrStaleGap) {
		return api.GapView{}, api.BackfillFailure("stale-gap")
	}
	if err != nil {
		return api.GapView{}, err
	}
	if a.backfill != nil {
		return a.backfill.projectGap(g), nil
	}
	return desktopread.GapProjection(g), nil
}
