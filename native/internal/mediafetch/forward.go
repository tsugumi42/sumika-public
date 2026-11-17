package mediafetch

import (
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"time"
)

// OfferForward 只唤醒允许重试的持久化任务；重复引用不能重置终局状态或次数。
func (f *Fetcher) OfferForward(tree types.ForwardTree, conv string) error {
	for node, n := range tree.Nodes {
		for _, j := range jobsOf(0, n.Segs) {
			s := n.Segs[j.idx]
			if s.Raw == "forward_limit" {
				continue
			}
			m, err := f.o.Store.RegisterForwardMedia(tree.ID, node, conv, store.MediaFetch{Idx: j.idx, Kind: s.Kind, URLSig: j.sig, State: store.MediaPending, UpdatedAt: f.nowMs()})
			if err != nil {
				return err
			}
			if m.State != store.MediaPending || m.Tries >= maxAttempts || m.NextAt > f.nowMs() {
				continue
			}
			j.forward, j.node, j.conv, j.tries = tree.ID, node, conv, m.Tries
			j.refreshed = s.Kind != types.SegFile
			f.offerForward(j)
		}
	}
	return nil
}
func (f *Fetcher) offerForward(j *job) {
	if !f.claim(j) {
		return
	}
	select {
	case f.queue <- j:
	default:
		f.release(j)
		f.mu.Lock()
		f.overflow = true
		f.mu.Unlock()
	}
}
func (f *Fetcher) sweepForward() {
	if f.o.Platform != "qq" {
		return
	}
	rows, err := f.o.Store.ForwardMediaPending(f.ctx, f.clock.Now().Add(-7*24*time.Hour).UnixMilli(), f.nowMs())
	if err != nil {
		if f.live() {
			f.errorf("转发图片补扫失败：%v", err)
		}
		return
	}
	for _, m := range rows {
		r, ok, err := f.o.Store.ReadForward(f.ctx, m.ID)
		if err != nil {
			f.errorf("转发媒体补扫读树失败：%v", err)
			return
		}
		if !ok || m.Node < 0 || m.Node >= len(r.Tree.Nodes) {
			continue
		}
		var j *job
		for _, candidate := range jobsOf(0, r.Tree.Nodes[m.Node].Segs) {
			if candidate.idx == m.Idx {
				j = candidate
				break
			}
		}
		if j == nil {
			continue
		}
		j.forward, j.node, j.conv, j.tries = m.ID, m.Node, m.Conversation, m.Tries
		j.refreshed = j.kind != types.SegFile
		if m.State == store.MediaFailed {
			j.maxTries = j.tries + 1
		}
		f.offerForward(j)
	}
}
