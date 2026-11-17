package desktopread

import (
	"context"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

type VideoDownloads interface {
	RequestVideo(int64, int, []types.Seg) (api.VideoDownloadProgress, error)
	WatchVideo(context.Context, int64, int, types.Seg) (api.Subscription[api.VideoDownloadProgress], error)
	CancelVideo(context.Context, int64, int) error
	RequestForwardVideo(string, int, int, string, []types.Seg) (api.VideoDownloadProgress, error)
	WatchForwardVideo(context.Context, string, int, int, types.Seg) (api.Subscription[api.VideoDownloadProgress], error)
	CancelForwardVideo(context.Context, string, int, int) error
}

func (s *Service) WithVideoDownloads(v VideoDownloads) *Service { s.videos = v; return s }

// 下载只认可见事件中的正文段，或沿该事件逐层可达的转发节点；不借外层会话猜原群。
func (s *Service) videoDownloadRow(ctx context.Context, ref api.VideoDownloadRef) (store.DisplayRow, []types.Seg, error) {
	if ref.Index < 0 || ref.Node < 0 || ref.Source.Conversation.Platform != "qq" || ref.Source.Event.Platform != "qq" || ref.Source.Event.EventID == "" || len(ref.Forward) == 0 && ref.Node != 0 {
		return store.DisplayRow{}, nil, ErrMediaRef
	}
	st, scope, err := s.scope(api.ConversationRequest{Key: ref.Source.Conversation, Limit: 1})
	if err != nil {
		return store.DisplayRow{}, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case s.queryGate <- struct{}{}:
	case <-ctx.Done():
		return store.DisplayRow{}, nil, ctx.Err()
	}
	defer func() { <-s.queryGate }()
	row, ok, err := st.ReadDisplayEvent(ctx, scope, ref.Source.Event.EventID)
	if err != nil {
		return row, nil, err
	}
	segs := parseEnvelope(row.Segments).env.Segs
	if len(ref.Forward) > 0 {
		last := len(ref.Forward) - 1
		r, exists, err := s.forwardPath(ctx, api.ForwardRequest{Source: ref.Source, ID: ref.Forward[last], Via: ref.Forward[:last]})
		if err != nil {
			return row, nil, err
		}
		if !exists || ref.Node >= len(r.Tree.Nodes) {
			return row, nil, ErrMediaRef
		}
		segs = r.Tree.Nodes[ref.Node].Segs
	}
	if !ok || ref.Index >= len(segs) || (segs[ref.Index].Kind != types.SegVideo && segs[ref.Index].Kind != types.SegFile) || segs[ref.Index].Raw == "forward_limit" {
		return row, nil, ErrMediaRef
	}
	return row, segs, nil
}

func (s *Service) RequestVideoDownload(ctx context.Context, ref api.VideoDownloadRef) (api.VideoDownloadProgress, error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return api.VideoDownloadProgress{}, err
	}
	defer done()
	row, segs, err := s.videoDownloadRow(ctx, ref)
	if err != nil {
		return api.VideoDownloadProgress{}, err
	}
	if s.videos == nil {
		return api.VideoDownloadProgress{}, api.ErrVideoDownloadUnavailable
	}
	if err := ctx.Err(); err != nil {
		return api.VideoDownloadProgress{}, err
	}
	if len(ref.Forward) > 0 {
		return s.videos.RequestForwardVideo(ref.Forward[len(ref.Forward)-1], ref.Node, ref.Index, ref.Source.Conversation.Conversation, segs)
	}
	return s.videos.RequestVideo(row.Seq, ref.Index, segs)
}
func (s *Service) WatchVideoDownload(ctx context.Context, ref api.VideoDownloadRef) (api.Subscription[api.VideoDownloadProgress], error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	row, segs, err := s.videoDownloadRow(ctx, ref)
	if err != nil {
		cancel()
		done()
		return nil, err
	}
	if s.videos == nil {
		cancel()
		done()
		return nil, api.ErrVideoDownloadUnavailable
	}
	var sub api.Subscription[api.VideoDownloadProgress]
	if len(ref.Forward) > 0 {
		sub, err = s.videos.WatchForwardVideo(ctx, ref.Forward[len(ref.Forward)-1], ref.Node, ref.Index, segs[ref.Index])
	} else {
		sub, err = s.videos.WatchVideo(ctx, row.Seq, ref.Index, segs[ref.Index])
	}
	if err != nil {
		cancel()
		done()
		return nil, err
	}
	// 服务或界面任一关闭都等待底层订阅，不依赖消费者继续读消息。
	closed := make(chan struct{})
	go func() { <-ctx.Done(); sub.Close(); done(); close(closed) }()
	return &videoDownloadSub{Subscription: sub, cancel: cancel, closed: closed}, nil
}

type videoDownloadSub struct {
	api.Subscription[api.VideoDownloadProgress]
	cancel context.CancelFunc
	closed chan struct{}
}

func (s *videoDownloadSub) Close() { s.cancel(); <-s.closed }

func (s *Service) CancelVideoDownload(ctx context.Context, ref api.VideoDownloadRef) error {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	row, _, err := s.videoDownloadRow(ctx, ref)
	if err != nil {
		return err
	}
	if s.videos == nil {
		return api.ErrVideoDownloadUnavailable
	}
	if len(ref.Forward) > 0 {
		return s.videos.CancelForwardVideo(ctx, ref.Forward[len(ref.Forward)-1], ref.Node, ref.Index)
	}
	return s.videos.CancelVideo(ctx, row.Seq, ref.Index)
}

var _ api.VideoDownloadActions = (*Service)(nil)
