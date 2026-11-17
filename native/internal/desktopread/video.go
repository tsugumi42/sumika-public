package desktopread

import (
	"context"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

// mediaRow 在查询闸内只查库，句柄读取与复制在闸外；视频只接受视频自身的 hash，封面不能拿去播放。
func (s *Service) mediaRow(ctx context.Context, ref api.MediaRef, video bool) (store.DisplayRow, error) {
	key := ref.Source.Conversation
	if ref.Source.Event.Platform != key.Platform || ref.Source.Event.EventID == "" {
		return store.DisplayRow{}, ErrMediaRef
	}
	st, scope, err := s.scope(api.ConversationRequest{Key: key, Limit: 1})
	if err != nil {
		return store.DisplayRow{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case s.queryGate <- struct{}{}:
	case <-ctx.Done():
		return store.DisplayRow{}, ctx.Err()
	}
	row, ok, err := st.ReadDisplayEvent(ctx, scope, ref.Source.Event.EventID)
	<-s.queryGate
	if err != nil {
		return row, err
	}
	if !ok {
		return row, ErrMediaRef
	}
	if len(ref.Forward) > 0 {
		// 有路径就只认路径终点，不能退回顶层事件里碰巧相同的 hash。
		ok, err := s.forwardMediaHas(ctx, ref, video)
		if err != nil || ok {
			return row, err
		}
	} else if video {
		for idx, seg := range parseEnvelope(row.Segments).segs {
			if (seg.seg.Kind == types.SegVideo || seg.seg.Kind == types.SegFile && row.MediaMeta[idx].Format == "video" && row.MediaMeta[idx].Hash == ref.Hash) && seg.media == ref.Hash {
				return row, nil
			}
		}
	} else if rowHasHash(row, ref.Hash) || row.Reply != nil && rowHasHash(*row.Reply, ref.Hash) {
		return row, nil
	}
	return row, ErrMediaRef
}

func (s *Service) PrepareVideo(ctx context.Context, ref api.MediaRef) (string, error) {
	if !validHash(ref.Hash) {
		return "", store.ErrDisplayParameter
	}
	if s.data == "" {
		return "", ErrNoData
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	if _, err := s.mediaRow(ctx, ref, true); err != nil {
		return "", err
	}
	file, err := s.openMedia(s.data, ref.Source.Conversation.Platform, ref.Hash)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return mediastore.PrepareVideoCopy(ctx, s.data, ref.Source.Conversation.Platform, file)
}
