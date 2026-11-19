package desktopread

import (
	"context"
	"errors"
	"strings"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/store"
)

var ErrForwardRef = errors.New("聊天记录不属于这个会话里可见的事件")

type ForwardFetcher interface {
	Fetch(context.Context, string, string, []string, time.Duration) (string, error)
	State(string) (string, int)
}

func (s *Service) WithForwardFetcher(f ForwardFetcher) *Service { s.forwards = f; return s }

func hasForward(segs []types.Seg, id string) bool {
	for _, seg := range segs {
		if seg.Kind == types.SegForward && seg.Ref == id {
			return true
		}
	}
	return false
}

// 路径的每一环都来自上一层的 forward 段；hash 和资源 id 自己都不是能力凭据。
func (s *Service) forwardPath(ctx context.Context, req api.ForwardRequest) (store.ForwardRecord, bool, error) {
	if req.Source.Conversation.Platform != "qq" || req.Source.Event.Platform != "qq" || req.ID == "" || len(req.Via) >= types.ForwardDepth {
		return store.ForwardRecord{}, false, ErrForwardRef
	}
	st, scope, err := s.scope(api.ConversationRequest{Key: req.Source.Conversation, Limit: 1})
	if err != nil {
		return store.ForwardRecord{}, false, err
	}
	row, ok, err := st.ReadDisplayEvent(ctx, scope, req.Source.Event.EventID)
	if err != nil {
		return store.ForwardRecord{}, false, err
	}
	if !ok {
		return store.ForwardRecord{}, false, ErrForwardRef
	}
	segs := parseEnvelope(row.Segments).env.Segs
	path := append(append([]string(nil), req.Via...), req.ID)
	seen := map[string]bool{}
	var result store.ForwardRecord
	for i, id := range path {
		if seen[id] || !hasForward(segs, id) {
			return result, false, ErrForwardRef
		}
		seen[id] = true
		result, ok, err = st.ReadForward(ctx, id)
		if err != nil {
			return result, false, err
		}
		if !ok {
			if i != len(path)-1 {
				return result, false, ErrForwardRef
			}
			return result, false, nil
		}
		segs = nil
		for _, node := range result.Tree.Nodes {
			for _, seg := range node.Segs {
				if seg.Kind == types.SegForward {
					segs = append(segs, seg)
				}
			}
		}
	}
	return result, true, nil
}
func (s *Service) ReadForward(ctx context.Context, req api.ForwardRequest) (api.Result[api.ForwardView], error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return failed[api.ForwardView](api.Failed, err)
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case s.queryGate <- struct{}{}:
	case <-ctx.Done():
		return failed[api.ForwardView](api.Failed, ctx.Err())
	}
	defer func() { <-s.queryGate }()
	record, ok, err := s.forwardPath(ctx, req)
	if err != nil {
		if errors.Is(err, store.ErrForwardTooLarge) {
			return api.Result[api.ForwardView]{State: api.Failed, Value: api.ForwardView{State: "too_large"}, Err: err}, err
		}
		return failed[api.ForwardView](api.Failed, err)
	}
	v := api.ForwardView{ID: req.ID, State: "not_receiving"}
	if !ok {
		if s.forwards != nil {
			v.State, v.Depth = s.forwards.State(req.ID)
		}
		return api.Result[api.ForwardView]{State: api.Ready, Value: v}, nil
	}
	tree := record.Tree
	v.Group, v.Missing, v.Fetched, v.State = tree.Group, tree.Missing, record.Fetched, "ready"
	if len(tree.Nodes) > types.ForwardNodes {
		v.Missing += len(tree.Nodes) - types.ForwardNodes
		tree.Nodes = tree.Nodes[:types.ForwardNodes]
	}
	st, ok := s.stores.Platform("qq")
	if !ok {
		return failed[api.ForwardView](api.Failed, ErrQQStoreClosed)
	}
	media, err := st.ForwardMediaRows(ctx, req.ID)
	if err != nil {
		return failed[api.ForwardView](api.Failed, err)
	}
	path := append(append([]string(nil), req.Via...), req.ID)
	for ni, n := range tree.Nodes {
		var meta map[int]store.MediaFetch
		for si := range n.Segs {
			if m, ok := media[[2]int{ni, si}]; ok {
				if meta == nil {
					meta = make(map[int]store.MediaFetch)
				}
				meta[si] = m
			}
		}
		segs := displaySegments(req.Source, parseSegments(parsed{env: envelope{Segs: n.Segs}, ok: true}), nil, meta)
		for si := range segs {
			d := &segs[si]
			if d.Download != nil {
				d.Download.Forward, d.Download.Node = append([]string(nil), path...), ni
			}
			if d.Cover != nil {
				d.Cover.Forward = append([]string(nil), path...)
			}
			if d.Media != nil {
				d.Media.Forward = append([]string(nil), path...)
			}
			d.MediaState = meta[si].State
			if n.Segs[si].Raw == "forward_limit" {
				d.MediaState = "forward_limit"
			}
			if d.Kind == types.SegReply {
				d.Summary = n.Segs[si].Raw
			}
		}
		at := n.Time
		if at > 0 && at < 100_000_000_000 {
			at *= 1000
		} else if at < 0 {
			at = 0
		}
		v.Nodes = append(v.Nodes, api.ForwardNode{UID: n.UID, Name: store.CleanDisplayName(strings.ReplaceAll(n.Name, "\ufffc", "")), At: at, Segments: segs})
	}
	all := make([]api.MessageView, len(v.Nodes))
	for i, n := range v.Nodes {
		all[i].Segments = n.Segments
	}
	if err := s.fillMessageForwards(ctx, req.Source.Conversation, all); err != nil {
		return failed[api.ForwardView](api.Failed, err)
	}
	return api.Result[api.ForwardView]{State: api.Ready, Value: v}, nil
}
func (s *Service) FetchForward(ctx context.Context, req api.ForwardRequest, wait time.Duration) (api.Result[api.ForwardView], error) {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return failed[api.ForwardView](api.Failed, err)
	}
	defer done()
	current, err := s.ReadForward(ctx, req)
	if err != nil || current.Value.State == "ready" || s.forwards == nil {
		return current, err
	}
	state, err := s.forwards.Fetch(ctx, req.ID, req.Source.Conversation.Conversation, req.Via, wait)
	if err != nil {
		return failed[api.ForwardView](api.Failed, err)
	}
	current, err = s.ReadForward(ctx, req)
	if err == nil && current.Value.State != "ready" {
		current.Value.State = state
	}
	return current, err
}

func forwardBrief(r store.ForwardRecord) *api.ForwardBrief {
	b := &api.ForwardBrief{Title: "聊天记录", Count: r.Count, Missing: r.Tree.Missing, State: "ready"}
	var names []string
	seen := map[string]bool{}
	for _, raw := range r.Names {
		name := store.CleanDisplayName(strings.ReplaceAll(raw, "\ufffc", ""))
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
			if len(names) == 4 {
				break
			}
		}
	}
	for i, n := range r.Tree.Nodes {
		name := store.CleanDisplayName(strings.ReplaceAll(n.Name, "\ufffc", ""))
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		if i < 3 {
			var text strings.Builder
			for _, s := range n.Segs {
				switch s.Kind {
				case types.SegText:
					text.WriteString(s.Text)
				case types.SegAt:
					if s.Target == types.AtAll {
						text.WriteString("@全体成员")
					} else if s.Target != "" && strings.Trim(s.Target, "0123456789") == "" {
						text.WriteString("@" + config.MaskID(s.Target))
					} else {
						text.WriteString("@某人")
					}
				case types.SegImage, types.SegEmoji:
					text.WriteString("[图片]")
				case types.SegFace, types.SegSuperface:
					text.WriteString("[表情]")
				case types.SegForward:
					text.WriteString("[合并转发]")
				case types.SegReply:
				case types.SegVoice:
					text.WriteString("[语音]")
				case types.SegVideo:
					text.WriteString("[视频]")
				case types.SegFile:
					text.WriteString("[文件]")
				case types.SegCard:
					text.WriteString("[卡片]")
				default:
					text.WriteString("[其他消息]")
				}
			}
			line := store.CleanDisplayName(strings.ReplaceAll(text.String(), "\ufffc", ""))
			rs := []rune(line)
			if len(rs) > 20 {
				line = string(rs[:20]) + "…"
			}
			if name != "" {
				line = name + "：" + line
			}
			b.Lines[i] = line
		}
	}
	if len(names) > 0 {
		if len(names) > 3 {
			b.Title = names[0] + "和" + names[1] + "等的聊天记录"
		} else {
			b.Title = strings.Join(names, "和") + "的聊天记录"
		}
	}
	return b
}
func (s *Service) fillSegmentForwards(ctx context.Context, segs []api.DisplaySegment) error {
	var ids []string
	seen := map[string]bool{}
	for _, d := range segs {
		if d.Kind == types.SegForward && d.Ref != "" && !seen[d.Ref] {
			seen[d.Ref] = true
			ids = append(ids, d.Ref)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	st, ok := s.stores.Platform("qq")
	if !ok {
		return ErrForwardRef
	}
	summaries, err := st.ForwardSummaries(ctx, ids)
	if err != nil {
		return err
	}
	for i := range segs {
		d := &segs[i]
		if d.Kind != types.SegForward {
			continue
		}
		if r, ok := summaries[d.Ref]; ok {
			d.Forward = s.forwardBriefs.get(r)
		} else {
			state := "not_receiving"
			if s.forwards != nil {
				state, _ = s.forwards.State(d.Ref)
			}
			d.Forward = &api.ForwardBrief{Title: "聊天记录", State: state}
		}
	}
	return nil
}
func (s *Service) fillMessageForwards(ctx context.Context, key api.ConversationKey, messages []api.MessageView) error {
	if key.Platform != "qq" {
		return nil
	}
	var all []api.DisplaySegment
	for _, m := range messages {
		for _, d := range m.Segments {
			if d.Kind == types.SegForward {
				all = append(all, d)
			}
		}
	}
	if err := s.fillSegmentForwards(ctx, all); err != nil {
		return err
	}
	n := 0
	for i := range messages {
		for j := range messages[i].Segments {
			if messages[i].Segments[j].Kind == types.SegForward {
				messages[i].Segments[j].Forward = all[n].Forward
				n++
			}
		}
	}
	return nil
}

func (s *Service) forwardMediaHas(ctx context.Context, ref api.MediaRef, video bool) (bool, error) {
	if len(ref.Forward) == 0 || len(ref.Forward) > types.ForwardDepth {
		return false, ErrMediaRef
	}
	last := len(ref.Forward) - 1
	r, ok, err := s.forwardPath(ctx, api.ForwardRequest{Source: ref.Source, ID: ref.Forward[last], Via: ref.Forward[:last]})
	if errors.Is(err, ErrForwardRef) {
		return false, ErrMediaRef
	}
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrMediaRef
	}
	st, found := s.stores.Platform("qq")
	if !found {
		return false, ErrQQStoreClosed
	}
	meta, err := st.ForwardMediaRows(ctx, r.Tree.ID)
	if err != nil {
		return false, err
	}
	for ni, n := range r.Tree.Nodes {
		for si, seg := range n.Segs {
			allowed := seg.Kind == types.SegImage || seg.Kind == types.SegEmoji || seg.Kind == types.SegCard
			if video {
				allowed = seg.Kind == types.SegVideo
			}
			m := meta[[2]int{ni, si}]
			if seg.Kind == types.SegFile && m.State == store.MediaDone && m.Hash == seg.Hash {
				allowed = m.Format == "image"
				if video {
					allowed = m.Format == "video"
				}
			}
			if !video && (seg.Kind == types.SegVideo || seg.Kind == types.SegFile && m.Format == "video") && m.State == store.MediaDone && m.Hash == seg.Hash && m.CoverHash == ref.Hash {
				return true, nil
			}
			if allowed && seg.Hash == ref.Hash {
				return true, nil
			}
		}
	}
	return false, nil
}
